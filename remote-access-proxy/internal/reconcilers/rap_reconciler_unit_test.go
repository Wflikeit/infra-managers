// SPDX-FileCopyrightText: (C) 2025 Intel Corporation
// SPDX-License-Identifier: Apache-2.0

package reconcilers

// Unit tests that exercise reconciler logic paths impossible or impractical to reach via real
// inventory (e.g. past expiration_timestamp, transient gRPC faults, invalid Get payloads).
// All stubInventoryClient wiring lives in this file only.

import (
	"context"
	"testing"
	"time"

	computev1 "github.com/open-edge-platform/infra-core/inventory/v2/pkg/api/compute/v1"
	remoteaccessv1 "github.com/open-edge-platform/infra-core/inventory/v2/pkg/api/remoteaccess/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	rec_v2 "github.com/open-edge-platform/orch-library/go/pkg/controller/v2"
)

// stubInventoryClient is a minimal implementation of rapInventoryClient for unit tests.
// It returns a fixed RAC on Get and records every state/binding update call.
type stubInventoryClient struct {
	getResult *remoteaccessv1.RemoteAccessConfiguration
	getErr    error

	stateUpdates   []*remoteaccessv1.RemoteAccessConfiguration
	stateUpdateErr error

	bindingUpdates   []*remoteaccessv1.RemoteAccessConfiguration
	bindingUpdateErr error
}

func (s *stubInventoryClient) GetRemoteAccessConf(
	_ context.Context, _, _ string, _ time.Duration,
) (*remoteaccessv1.RemoteAccessConfiguration, error) {
	return s.getResult, s.getErr
}

func (s *stubInventoryClient) UpdateRemoteAccessConfigState(
	_ context.Context, _, _ string,
	patch *remoteaccessv1.RemoteAccessConfiguration,
	_ time.Duration,
) error {
	s.stateUpdates = append(s.stateUpdates, patch)
	return s.stateUpdateErr
}

func (s *stubInventoryClient) UpdateRemoteAccessConfigBinding(
	_ context.Context, _, _ string,
	patch *remoteaccessv1.RemoteAccessConfiguration,
	_ time.Duration,
) error {
	s.bindingUpdates = append(s.bindingUpdates, patch)
	return s.bindingUpdateErr
}

// newReconcilerWithStub builds a RAPReconciler wired to the given stub. chisel may be nil (noop).
func newReconcilerWithStub(stub *stubInventoryClient, rt RAPRuntime, chisel ChiselUserRegistrar) *RAPReconciler {
	if chisel == nil {
		chisel = noopChiselRegistrar{}
	}
	return &RAPReconciler{
		netClient:        stub,
		runtime:          rt,
		tracingEnabled:   false,
		inventoryTimeout: 5 * time.Second,
		chisel:           chisel,
		ports:            newLocalPortAllocator(),
	}
}

func requireRetry(t *testing.T, d rec_v2.Directive[ReconcilerID]) {
	t.Helper()
	_, isRetry := d.(*rec_v2.Retry[ReconcilerID])
	_, isRetryWith := d.(*rec_v2.RetryWith[ReconcilerID])
	_, isRetryAfter := d.(*rec_v2.RetryAfter[ReconcilerID])
	_, isRetryAt := d.(*rec_v2.RetryAt[ReconcilerID])
	require.True(t, isRetry || isRetryWith || isRetryAfter || isRetryAt,
		"expected Retry-family directive (HandleInventoryError uses Retry.With → RetryWith), got %T", d)
}

// racSpecReadyEnabled returns a synthetic RAC for stub clients (SpecReady, desired enabled).
func racSpecReadyEnabled(tenantID, resID string) *remoteaccessv1.RemoteAccessConfiguration {
	return &remoteaccessv1.RemoteAccessConfiguration{
		ResourceId:          resID,
		TenantId:            tenantID,
		Instance:            &computev1.InstanceResource{ResourceId: "inst-stub"},
		DesiredState:        remoteaccessv1.RemoteAccessState_REMOTE_ACCESS_STATE_ENABLED,
		CurrentState:        remoteaccessv1.RemoteAccessState_REMOTE_ACCESS_STATE_ENABLED,
		ExpirationTimestamp: uint64(time.Now().Add(time.Hour).Unix()),
		LocalPort:           21010,
		ProxyHost:           "proxy.stub.example",
		User:                "root",
		SessionToken:        "chiseluser:chiselpass",
	}
}

// racSpecReadyEnabledMismatchCurrent is SpecReady with desired ENABLED but current CONFIGURED so
// shouldSkip is false and reconcileWithSpec runs persistBinding / setConnectionStatus (tests that
// inject failures on those RPCs must not use desired==current or they only hit the skip path).
func racSpecReadyEnabledMismatchCurrent(tenantID, resID string) *remoteaccessv1.RemoteAccessConfiguration {
	r := racSpecReadyEnabled(tenantID, resID)
	r.CurrentState = remoteaccessv1.RemoteAccessState_REMOTE_ACCESS_STATE_CONFIGURED
	return r
}

// racSpecInvalidMissingInstance is a payload shape Create/Update would reject; used to test SpecInvalid identity.
func racSpecInvalidMissingInstance(tenantID, resID string) *remoteaccessv1.RemoteAccessConfiguration {
	return &remoteaccessv1.RemoteAccessConfiguration{
		ResourceId:          resID,
		TenantId:            tenantID,
		Instance:            nil,
		DesiredState:        remoteaccessv1.RemoteAccessState_REMOTE_ACCESS_STATE_ENABLED,
		CurrentState:        remoteaccessv1.RemoteAccessState_REMOTE_ACCESS_STATE_CONFIGURED,
		ExpirationTimestamp: uint64(time.Now().Add(time.Hour).Unix()),
		LocalPort:           21011,
		ProxyHost:           "proxy.stub.example",
		User:                "root",
		SessionToken:        "u:p",
	}
}

// expiredRAC returns a RemoteAccessConfiguration whose ExpirationTimestamp is in the past.
// All identity and binding fields are populated so the only SpecInvalid reason is expiry.
func expiredRAC(tenantID, resID string) *remoteaccessv1.RemoteAccessConfiguration {
	return &remoteaccessv1.RemoteAccessConfiguration{
		ResourceId:          resID,
		TenantId:            tenantID,
		Instance:            &computev1.InstanceResource{ResourceId: "inst-stub"},
		DesiredState:        remoteaccessv1.RemoteAccessState_REMOTE_ACCESS_STATE_ENABLED,
		CurrentState:        remoteaccessv1.RemoteAccessState_REMOTE_ACCESS_STATE_CONFIGURED,
		ExpirationTimestamp: uint64(time.Now().Add(-1 * time.Hour).Unix()),
		// Binding fields populated so SpecInvalid reason is expiry only (not also "pending").
		LocalPort:    21001,
		ProxyHost:    "proxy.example.test",
		User:         "stubuser",
		SessionToken: "stubuser:stubpass",
	}
}

// TestSpec_RAPReconcile_when_expiration_in_past_then_teardown_and_operational_status verifies
// the SpecInvalid+expiry path when RAM has NOT yet set current_state=ERROR:
//   - DisableSession is called (replica teardown)
//   - UpdateRemoteAccessConfigState is called with configuration_status containing the expiry reason
//   - Reconcile returns Ack
//
// This test uses a stubInventoryClient because inventory rejects RAC creation with
// expiration_timestamp < 10 min in the future, and the field is immutable after creation.
func TestSpec_RAPReconcile_when_expiration_in_past_then_teardown_and_operational_status(t *testing.T) {
	const (
		tenantID = "11111111-1111-1111-1111-111111111111"
		resID    = "rmtacconf-expiredstub"
	)

	stub := &stubInventoryClient{getResult: expiredRAC(tenantID, resID)}
	rt := &recordingRAPRuntime{inner: NewInMemoryRAPRuntime()}
	rec := newReconcilerWithStub(stub, rt, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req := rec_v2.Request[ReconcilerID]{ID: NewReconcilerID(tenantID, resID)}

	requireAck(t, rec.Reconcile(ctx, req))

	// Replica teardown: DisableSession must be called.
	require.NotEmpty(t, rt.disableReasons,
		"reconciler must call DisableSession for expired RAC")
	require.Contains(t, rt.disableReasons[len(rt.disableReasons)-1], "expiration_timestamp is in the past",
		"DisableSession reason must mention expiry")

	// Operational status: UpdateRemoteAccessConfigState must persist expiry text.
	require.NotEmpty(t, stub.stateUpdates,
		"reconciler must call UpdateRemoteAccessConfigState to persist expiry status")
	require.Contains(t,
		stub.stateUpdates[len(stub.stateUpdates)-1].GetConfigurationStatus(),
		"expiration_timestamp is in the past",
		"configuration_status must contain expiry reason")
}

// TestSpec_RAPReconcile_when_expiration_in_past_and_current_state_error_then_ack_only verifies
// the SpecInvalid+expiry path when RAM has ALREADY set current_state=ERROR:
//   - DisableSession is called (replica teardown)
//   - UpdateRemoteAccessConfigState is NOT called (RAM owns current_state/ERROR, RAP must not write)
//   - Reconcile returns Ack
func TestSpec_RAPReconcile_when_expiration_in_past_and_current_state_error_then_ack_only(t *testing.T) {
	const (
		tenantID = "11111111-1111-1111-1111-111111111111"
		resID    = "rmtacconf-expirederror"
	)

	rac := expiredRAC(tenantID, resID)
	rac.CurrentState = remoteaccessv1.RemoteAccessState_REMOTE_ACCESS_STATE_ERROR

	stub := &stubInventoryClient{getResult: rac}
	rt := &recordingRAPRuntime{inner: NewInMemoryRAPRuntime()}
	rec := newReconcilerWithStub(stub, rt, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req := rec_v2.Request[ReconcilerID]{ID: NewReconcilerID(tenantID, resID)}

	requireAck(t, rec.Reconcile(ctx, req))

	// Replica teardown: DisableSession must be called.
	require.NotEmpty(t, rt.disableReasons,
		"reconciler must call DisableSession for expired RAC with current_state=ERROR")

	// RAM owns current_state=ERROR: RAP must NOT call UpdateRemoteAccessConfigState.
	require.Empty(t, stub.stateUpdates,
		"reconciler must NOT write configuration_status when RAM already set current_state=ERROR (RAM owns state)")
}

func TestSpec_RAPReconcile_when_inventory_get_transient_error_then_retry_directive(t *testing.T) {
	const (
		tenantID = "11111111-1111-1111-1111-111111111111"
		resID    = "rmtacconf-transientget"
	)
	stub := &stubInventoryClient{getErr: status.Error(codes.Unavailable, "inventory transient")}
	rec := newReconcilerWithStub(stub, NewInMemoryRAPRuntime(), nil)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req := rec_v2.Request[ReconcilerID]{ID: NewReconcilerID(tenantID, resID)}
	requireRetry(t, rec.Reconcile(ctx, req))
}

func TestSpec_RAPReconcile_when_spec_invalid_identity_then_mark_error(t *testing.T) {
	const (
		tenantID = "11111111-1111-1111-1111-111111111111"
		resID    = "rmtacconf-badidentity"
	)
	stub := &stubInventoryClient{getResult: racSpecInvalidMissingInstance(tenantID, resID)}
	rt := &recordingRAPRuntime{inner: NewInMemoryRAPRuntime()}
	rec := newReconcilerWithStub(stub, rt, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req := rec_v2.Request[ReconcilerID]{ID: NewReconcilerID(tenantID, resID)}
	requireAck(t, rec.Reconcile(ctx, req))

	require.NotEmpty(t, rt.disableReasons, "SpecInvalid must tear down replica session")
	require.Contains(t, rt.disableReasons[len(rt.disableReasons)-1], "spec invalid:")
	require.Contains(t, rt.disableReasons[len(rt.disableReasons)-1], "missing instance reference")
	require.Empty(t, stub.stateUpdates, "non-expiry SpecInvalid: RAM owns current_state; RAP must not write configuration_status")
}

func TestSpec_RAPReconcile_when_persist_binding_fails_then_inventory_directive(t *testing.T) {
	const (
		tenantID = "11111111-1111-1111-1111-111111111111"
		resID    = "rmtacconf-bindfail"
	)
	stub := &stubInventoryClient{
		getResult:        racSpecReadyEnabledMismatchCurrent(tenantID, resID),
		bindingUpdateErr: status.Error(codes.Unavailable, "binding write transient"),
	}
	rec := newReconcilerWithStub(stub, NewInMemoryRAPRuntime(), nil)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req := rec_v2.Request[ReconcilerID]{ID: NewReconcilerID(tenantID, resID)}
	requireRetry(t, rec.Reconcile(ctx, req))
}

func TestSpec_RAPReconcile_when_set_connection_status_fails_then_inventory_directive(t *testing.T) {
	const (
		tenantID = "11111111-1111-1111-1111-111111111111"
		resID    = "rmtacconf-statefail"
	)
	stub := &stubInventoryClient{
		getResult:      racSpecReadyEnabledMismatchCurrent(tenantID, resID),
		stateUpdateErr: status.Error(codes.Unavailable, "state write transient"),
	}
	rec := newReconcilerWithStub(stub, NewInMemoryRAPRuntime(), nil)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req := rec_v2.Request[ReconcilerID]{ID: NewReconcilerID(tenantID, resID)}
	requireRetry(t, rec.Reconcile(ctx, req))
	require.NotEmpty(t, stub.bindingUpdates, "binding persist should run before operational status write")
}
