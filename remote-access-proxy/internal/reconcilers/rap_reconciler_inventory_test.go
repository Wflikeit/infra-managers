// SPDX-FileCopyrightText: (C) 2025 Intel Corporation
// SPDX-License-Identifier: Apache-2.0

package reconcilers

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	inv_v1 "github.com/open-edge-platform/infra-core/inventory/v2/pkg/api/inventory/v1"
	remoteaccessv1 "github.com/open-edge-platform/infra-core/inventory/v2/pkg/api/remoteaccess/v1"
	inv_testing "github.com/open-edge-platform/infra-core/inventory/v2/pkg/testing"
	"github.com/open-edge-platform/infra-core/inventory/v2/pkg/util"
	"github.com/open-edge-platform/infra-managers/remote-access-proxy/internal/clients"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/fieldmaskpb"

	rec_v2 "github.com/open-edge-platform/orch-library/go/pkg/controller/v2"
)

// Same tenant UUID as internal/testing (cannot import that package here: it imports reconcilers → import cycle).
const integrationTestTenant1 = "11111111-1111-1111-1111-111111111111"

// testRAPInventoryClientName must match internal/testing client registration pattern for RM + RMT_ACCESS_CONF.
const testRAPInventoryClientName = "TestNetInventoryClient"

var integrationRmtAccessCfgClient *clients.RmtAccessInventoryClient

func createRAPInventoryClientForTest(tb testing.TB) {
	tb.Helper()
	resourceKinds := []inv_v1.ResourceKind{inv_v1.ResourceKind_RESOURCE_KIND_RMT_ACCESS_CONF}
	err := inv_testing.CreateClient(testRAPInventoryClientName, inv_v1.ClientKind_CLIENT_KIND_RESOURCE_MANAGER, resourceKinds, "")
	require.NoError(tb, err)

	cli, err := clients.NewRAInventoryClient(
		inv_testing.TestClients[testRAPInventoryClientName].GetTenantAwareInventoryClient(),
		inv_testing.TestClientsEvents[testRAPInventoryClientName],
	)
	require.NoError(tb, err)
	integrationRmtAccessCfgClient = cli
	tb.Cleanup(func() {
		integrationRmtAccessCfgClient.Stop()
		integrationRmtAccessCfgClient = nil
		delete(inv_testing.TestClients, testRAPInventoryClientName)
		delete(inv_testing.TestClientsEvents, testRAPInventoryClientName)
	})
}

func TestMain(m *testing.M) {
	wd, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	projectRoot := filepath.Dir(filepath.Dir(wd))
	policyPath := filepath.Join(projectRoot, "out")
	migrationsDir := filepath.Join(projectRoot, "out")

	inv_testing.StartTestingEnvironment(policyPath, "", migrationsDir)
	code := m.Run()
	inv_testing.StopTestingEnvironment()
	os.Exit(code)
}

func arrangeRAPInventoryAndDAO(t *testing.T) *inv_testing.InvResourceDAO {
	t.Helper()
	dao := inv_testing.NewInvResourceDAOOrFail(t)
	createRAPInventoryClientForTest(t)
	return dao
}

func newTestRAPReconciler(t *testing.T) *RAPReconciler {
	t.Helper()
	return newTestRAPReconcilerWithOptions(t, newTestRAPReconcilerOptions{
		runtime: NewInMemoryRAPRuntime(),
	})
}

type newTestRAPReconcilerOptions struct {
	runtime        RAPRuntime
	tracingEnabled bool
}

func newTestRAPReconcilerWithOptions(t *testing.T, o newTestRAPReconcilerOptions) *RAPReconciler {
	t.Helper()
	rec, err := NewRAPReconciler(
		integrationRmtAccessCfgClient,
		o.runtime,
		o.tracingEnabled,
		clients.DefaultInventoryTimeout,
		nil,
	)
	require.NoError(t, err)
	return rec
}

// recordingRAPRuntime delegates to inner and records DisableSession reasons (for teardown assertions).
type recordingRAPRuntime struct {
	inner            RAPRuntime
	disableReasons   []string
	disableTenantRes [][2]string
}

func (r *recordingRAPRuntime) EnsureSession(
	ctx context.Context,
	tenantID, resourceID string,
	spec *RAPSpec,
) (SessionConnectivity, error) {
	return r.inner.EnsureSession(ctx, tenantID, resourceID, spec)
}

func (r *recordingRAPRuntime) DisableSession(
	ctx context.Context,
	tenantID, resourceID, reason string,
) error {
	r.disableReasons = append(r.disableReasons, reason)
	r.disableTenantRes = append(r.disableTenantRes, [2]string{tenantID, resourceID})
	return r.inner.DisableSession(ctx, tenantID, resourceID, reason)
}

// runRAPRevokeViaDesiredDisabled models RAM revoking access by patching desired_state to DISABLED after RAP
// has materialized binding (SpecReady path). Asserts replica teardown and idle operational text; RAP never
// mutates desired_state on inventory (RAM-owned intent).
func runRAPRevokeViaDesiredDisabled(t *testing.T, dao *inv_testing.InvResourceDAO, tenantID, resID string) {
	t.Helper()
	rt := &recordingRAPRuntime{inner: NewInMemoryRAPRuntime()}
	rec := newTestRAPReconcilerWithOptions(t, newTestRAPReconcilerOptions{runtime: rt})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req := rec_v2.Request[ReconcilerID]{ID: NewReconcilerID(tenantID, resID)}

	requireAck(t, rec.Reconcile(ctx, req))
	require.Empty(t, rt.disableReasons, "first reconcile must not disable session")

	patchCtx, patchCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer patchCancel()
	_, err := dao.GetAPIClient().Update(
		patchCtx,
		tenantID,
		resID,
		&fieldmaskpb.FieldMask{Paths: []string{remoteaccessv1.RemoteAccessConfigurationFieldDesiredState}},
		&inv_v1.Resource{
			Resource: &inv_v1.Resource_RemoteAccess{
				RemoteAccess: &remoteaccessv1.RemoteAccessConfiguration{
					DesiredState: remoteaccessv1.RemoteAccessState_REMOTE_ACCESS_STATE_DISABLED,
				},
			},
		},
	)
	require.NoError(t, err)

	requireAck(t, rec.Reconcile(ctx, req))
	require.NotEmpty(t, rt.disableReasons, "second reconcile must disable session when desired is disabled")
	require.Contains(t, rt.disableReasons[len(rt.disableReasons)-1], "desired disabled")

	got, err := integrationRmtAccessCfgClient.GetRemoteAccessConf(ctx, tenantID, resID, clients.DefaultInventoryTimeout)
	require.NoError(t, err)
	require.Contains(t, got.GetConfigurationStatus(), "remote access proxy reconciled",
		"reconciler must persist idle operational status to inventory after disabling")
	require.Equal(t, remoteaccessv1.RemoteAccessState_REMOTE_ACCESS_STATE_DISABLED, got.GetDesiredState(),
		"RAP must not change desired_state on inventory (RAM owns desired policy)")
}

func requireAck(t *testing.T, d rec_v2.Directive[ReconcilerID]) {
	t.Helper()
	_, ok := d.(*rec_v2.Ack[ReconcilerID])
	require.True(t, ok, "expected Ack, got %T", d)
}

type errEnsureSessionRuntime struct {
	inner RAPRuntime
}

func (e *errEnsureSessionRuntime) EnsureSession(
	ctx context.Context,
	tenantID, resourceID string,
	spec *RAPSpec,
) (SessionConnectivity, error) {
	return SessionConnectivity{}, fmt.Errorf("ensure session boom")
}

func (e *errEnsureSessionRuntime) DisableSession(
	ctx context.Context,
	tenantID, resourceID, reason string,
) error {
	return e.inner.DisableSession(ctx, tenantID, resourceID, reason)
}

func TestSpec_RAPReconcile_after_RAC_create_persists_binding(t *testing.T) {
	dao := arrangeRAPInventoryAndDAO(t)
	rac := dao.CreateRemoteAccessConfiguration(t, integrationTestTenant1)
	tenantID := rac.GetTenantId()
	resID := rac.GetResourceId()

	rec := newTestRAPReconciler(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	req := rec_v2.Request[ReconcilerID]{ID: NewReconcilerID(tenantID, resID)}
	d := rec.Reconcile(ctx, req)
	requireAck(t, d)

	cli := integrationRmtAccessCfgClient
	got, err := cli.GetRemoteAccessConf(ctx, tenantID, resID, clients.DefaultInventoryTimeout)
	require.NoError(t, err)
	require.NotNil(t, got)

	require.Greater(t, got.GetLocalPort(), uint32(0), "RAP should allocate/persist local_port")
	require.GreaterOrEqual(t, got.GetLocalPort(), uint32(21000))
	require.LessOrEqual(t, got.GetLocalPort(), uint32(21999))
	require.NotEmpty(t, strings.TrimSpace(got.GetProxyHost()))
	require.NotEmpty(t, strings.TrimSpace(got.GetTargetHost()))
	require.Greater(t, got.GetTargetPort(), uint32(0))
	require.NotEmpty(t, strings.TrimSpace(got.GetUser()))
	require.NotEmpty(t, strings.TrimSpace(got.GetSessionToken()))
	require.Contains(t, got.GetSessionToken(), ":", "session_token should be user:pass from bootstrap")
}

func TestSpec_RAPReconcile_second_pass_is_ack_and_stable_binding(t *testing.T) {
	dao := arrangeRAPInventoryAndDAO(t)
	rac := dao.CreateRemoteAccessConfiguration(t, integrationTestTenant1)
	tenantID := rac.GetTenantId()
	resID := rac.GetResourceId()

	rec := newTestRAPReconciler(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req := rec_v2.Request[ReconcilerID]{ID: NewReconcilerID(tenantID, resID)}

	requireAck(t, rec.Reconcile(ctx, req))

	cli := integrationRmtAccessCfgClient
	afterFirst, err := cli.GetRemoteAccessConf(ctx, tenantID, resID, clients.DefaultInventoryTimeout)
	require.NoError(t, err)
	port1 := afterFirst.GetLocalPort()

	requireAck(t, rec.Reconcile(ctx, req))

	afterSecond, err := cli.GetRemoteAccessConf(ctx, tenantID, resID, clients.DefaultInventoryTimeout)
	require.NoError(t, err)
	require.Equal(t, port1, afterSecond.GetLocalPort(), "second reconcile must not rotate local_port for same resource")
}

// --- Placeholders

func TestSpec_RAPReconcile_when_runtime_nil_then_mark_error(t *testing.T) {
	dao := arrangeRAPInventoryAndDAO(t)
	rac := dao.CreateRemoteAccessConfiguration(t, integrationTestTenant1)
	tenantID := rac.GetTenantId()
	resID := rac.GetResourceId()

	rec, err := NewRAPReconciler(
		integrationRmtAccessCfgClient,
		nil,
		false,
		clients.DefaultInventoryTimeout,
		nil,
	)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req := rec_v2.Request[ReconcilerID]{ID: NewReconcilerID(tenantID, resID)}
	requireAck(t, rec.Reconcile(ctx, req))

	got, err := integrationRmtAccessCfgClient.GetRemoteAccessConf(ctx, tenantID, resID, clients.DefaultInventoryTimeout)
	require.NoError(t, err)
	require.Contains(t, got.GetConfigurationStatus(), "rap runtime not configured")
}

func TestSpec_RAPReconcile_when_inventory_record_missing_then_runtime_cleanup(t *testing.T) {
	t.Run("not_found_without_prior_session", func(t *testing.T) {
		dao := arrangeRAPInventoryAndDAO(t)
		anchor := dao.CreateRemoteAccessConfiguration(t, integrationTestTenant1)
		tenantID := anchor.GetTenantId()
		// Must use a valid RMT_ACCESS_CONF resource id prefix so inventory returns NotFound
		// for a missing row; arbitrary UUID-shaped ids are rejected as InvalidArgument before lookup.
		missingRes := "rmtacconf-deadbeef"

		rt := &recordingRAPRuntime{inner: NewInMemoryRAPRuntime()}
		rec := newTestRAPReconcilerWithOptions(t, newTestRAPReconcilerOptions{runtime: rt})

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		req := rec_v2.Request[ReconcilerID]{ID: NewReconcilerID(tenantID, missingRes)}
		requireAck(t, rec.Reconcile(ctx, req))

		require.NotEmpty(t, rt.disableReasons, "expected DisableSession when inventory returns not found")
		require.Contains(t, rt.disableReasons[len(rt.disableReasons)-1], "inventory record not found")
		require.Equal(t, tenantID, rt.disableTenantRes[len(rt.disableTenantRes)-1][0])
		require.Equal(t, missingRes, rt.disableTenantRes[len(rt.disableTenantRes)-1][1])
	})

	t.Run("invalid_uuid_shaped_id_inventory_invalid_argument_then_ack_cleanup", func(t *testing.T) {
		dao := arrangeRAPInventoryAndDAO(t)
		anchor := dao.CreateRemoteAccessConfiguration(t, integrationTestTenant1)
		tenantID := anchor.GetTenantId()
		// UUID-shaped ids are rejected as InvalidArgument (unknown prefix) before DB lookup.
		missingRes := "22222222-2222-2222-2222-222222222222"

		rt := &recordingRAPRuntime{inner: NewInMemoryRAPRuntime()}
		rec := newTestRAPReconcilerWithOptions(t, newTestRAPReconcilerOptions{runtime: rt})

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		req := rec_v2.Request[ReconcilerID]{ID: NewReconcilerID(tenantID, missingRes)}
		requireAck(t, rec.Reconcile(ctx, req))

		require.NotEmpty(t, rt.disableReasons, "expected DisableSession when inventory rejects resource id")
		require.Contains(t, rt.disableReasons[len(rt.disableReasons)-1], "inventory record not found")
		require.Equal(t, tenantID, rt.disableTenantRes[len(rt.disableTenantRes)-1][0])
		require.Equal(t, missingRes, rt.disableTenantRes[len(rt.disableTenantRes)-1][1])
	})

	t.Run("not_found_after_hard_delete", func(t *testing.T) {
		dao := arrangeRAPInventoryAndDAO(t)
		rac := dao.CreateRemoteAccessConfigurationNoCleanup(t, integrationTestTenant1)
		tenantID := rac.GetTenantId()
		resID := rac.GetResourceId()
		deletedFromInventory := false
		t.Cleanup(func() {
			if deletedFromInventory {
				return
			}
			dao.HardDeleteRemoteAccessConfiguration(t, tenantID, resID)
		})

		rt := &recordingRAPRuntime{inner: NewInMemoryRAPRuntime()}
		rec := newTestRAPReconcilerWithOptions(t, newTestRAPReconcilerOptions{runtime: rt})

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		req := rec_v2.Request[ReconcilerID]{ID: NewReconcilerID(tenantID, resID)}

		requireAck(t, rec.Reconcile(ctx, req))
		require.Empty(t, rt.disableReasons, "first reconcile should establish replica state without disable")

		dao.HardDeleteRemoteAccessConfiguration(t, tenantID, resID)
		deletedFromInventory = true

		requireAck(t, rec.Reconcile(ctx, req))
		require.Contains(t, strings.Join(rt.disableReasons, "|"), "inventory record not found")
	})
}

func TestSpec_RAPReconcile_when_spec_ready_and_desired_equals_current_then_ack_without_side_effects(t *testing.T) {
	dao := arrangeRAPInventoryAndDAO(t)
	rac := dao.CreateRemoteAccessConfiguration(t, integrationTestTenant1)
	tenantID, resID := rac.GetTenantId(), rac.GetResourceId()

	rec := newTestRAPReconciler(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req := rec_v2.Request[ReconcilerID]{ID: NewReconcilerID(tenantID, resID)}
	requireAck(t, rec.Reconcile(ctx, req))

	cli := integrationRmtAccessCfgClient
	cur, err := cli.GetRemoteAccessConf(ctx, tenantID, resID, clients.DefaultInventoryTimeout)
	require.NoError(t, err)
	if cur.GetCurrentState() != cur.GetDesiredState() {
		// current_state is RAM-owned; OPA allows RM (not API) clients to align it in tests — same as
		// InvResourceDAO.HardDeleteRemoteAccessConfiguration (rmClient.Update).
		patchCtx, patchCancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, err = dao.GetRMClient().Update(
			patchCtx,
			tenantID,
			resID,
			&fieldmaskpb.FieldMask{Paths: []string{remoteaccessv1.RemoteAccessConfigurationFieldCurrentState}},
			&inv_v1.Resource{
				Resource: &inv_v1.Resource_RemoteAccess{
					RemoteAccess: &remoteaccessv1.RemoteAccessConfiguration{
						CurrentState: cur.GetDesiredState(),
					},
				},
			},
		)
		patchCancel()
		require.NoError(t, err)
	}

	before, err := cli.GetRemoteAccessConf(ctx, tenantID, resID, clients.DefaultInventoryTimeout)
	require.NoError(t, err)
	ts := before.GetConfigurationStatusTimestamp()
	port := before.GetLocalPort()
	statusBefore := before.GetConfigurationStatus()

	requireAck(t, rec.Reconcile(ctx, req))

	after, err := cli.GetRemoteAccessConf(ctx, tenantID, resID, clients.DefaultInventoryTimeout)
	require.NoError(t, err)
	require.Equal(t, ts, after.GetConfigurationStatusTimestamp(),
		"shouldSkip must not bump configuration_status_timestamp (no new inventory state writes)")
	require.Equal(t, port, after.GetLocalPort(), "skip path must not change binding")
	require.Equal(t, statusBefore, after.GetConfigurationStatus(), "operational text unchanged on skip")
}

// Stub-only specs (transient Get, SpecInvalid identity, persist binding / state write failures) live in
// rap_reconciler_unit_test.go. Expiration-in-past cases are there too: inventory rejects short-lived
// expiration at create and treats expiration_timestamp as immutable afterward.

func TestSpec_RAPReconcile_when_ready_desired_disabled_then_disable_and_converge_state(t *testing.T) {
	dao := arrangeRAPInventoryAndDAO(t)
	rac := dao.CreateRemoteAccessConfiguration(t, integrationTestTenant1)
	runRAPRevokeViaDesiredDisabled(t, dao, rac.GetTenantId(), rac.GetResourceId())
}

func TestSpec_RAPReconcile_when_pending_desired_disabled_then_ack_cleanup(t *testing.T) {
	dao := arrangeRAPInventoryAndDAO(t)
	// RAC with desired=DISABLED: expiry check is skipped, binding fields absent → SpecPending.
	rac := dao.CreateRemoteAccessConfiguration(t, integrationTestTenant1, func(r *remoteaccessv1.RemoteAccessConfiguration) {
		r.DesiredState = remoteaccessv1.RemoteAccessState_REMOTE_ACCESS_STATE_DISABLED
	})
	tenantID := rac.GetTenantId()
	resID := rac.GetResourceId()

	rt := &recordingRAPRuntime{inner: NewInMemoryRAPRuntime()}
	rec := newTestRAPReconcilerWithOptions(t, newTestRAPReconcilerOptions{runtime: rt})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req := rec_v2.Request[ReconcilerID]{ID: NewReconcilerID(tenantID, resID)}

	requireAck(t, rec.Reconcile(ctx, req))

	require.NotEmpty(t, rt.disableReasons, "reconciler must call DisableSession when spec is pending and desired is disabled")
	require.Contains(t, rt.disableReasons[len(rt.disableReasons)-1], "desired disabled (pending)")
	require.Equal(t, tenantID, rt.disableTenantRes[len(rt.disableTenantRes)-1][0])
	require.Equal(t, resID, rt.disableTenantRes[len(rt.disableTenantRes)-1][1])
}

func TestSpec_RAPReconcile_when_chisel_token_sync_fails_then_mark_error(t *testing.T) {
	dao := arrangeRAPInventoryAndDAO(t)
	rac := dao.CreateRemoteAccessConfiguration(t, integrationTestTenant1)
	tenantID, resID := rac.GetTenantId(), rac.GetResourceId()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req := rec_v2.Request[ReconcilerID]{ID: NewReconcilerID(tenantID, resID)}
	rec := newTestRAPReconciler(t)
	requireAck(t, rec.Reconcile(ctx, req))

	patchCtx, patchCancel := context.WithTimeout(context.Background(), 5*time.Second)
	_, err := dao.GetAPIClient().Update(
		patchCtx,
		tenantID,
		resID,
		&fieldmaskpb.FieldMask{Paths: []string{remoteaccessv1.RemoteAccessConfigurationFieldSessionToken}},
		&inv_v1.Resource{
			Resource: &inv_v1.Resource_RemoteAccess{
				RemoteAccess: &remoteaccessv1.RemoteAccessConfiguration{
					SessionToken: "not-a-valid-user-pass-pair",
				},
			},
		},
	)
	patchCancel()
	require.NoError(t, err)

	requireAck(t, rec.Reconcile(ctx, req))
	got, err := integrationRmtAccessCfgClient.GetRemoteAccessConf(ctx, tenantID, resID, clients.DefaultInventoryTimeout)
	require.NoError(t, err)
	require.Contains(t, got.GetConfigurationStatus(), "chisel user sync:")
	require.Contains(t, got.GetConfigurationStatus(), "session_token must be user:pass")
}

func TestSpec_RAPReconcile_when_runtime_ensure_session_fails_then_mark_error(t *testing.T) {
	dao := arrangeRAPInventoryAndDAO(t)
	rac := dao.CreateRemoteAccessConfiguration(t, integrationTestTenant1)
	tenantID, resID := rac.GetTenantId(), rac.GetResourceId()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req := rec_v2.Request[ReconcilerID]{ID: NewReconcilerID(tenantID, resID)}
	rec := newTestRAPReconciler(t)
	requireAck(t, rec.Reconcile(ctx, req))

	rec = newTestRAPReconcilerWithOptions(t, newTestRAPReconcilerOptions{
		runtime: &errEnsureSessionRuntime{inner: NewInMemoryRAPRuntime()},
	})
	requireAck(t, rec.Reconcile(ctx, req))

	got, err := integrationRmtAccessCfgClient.GetRemoteAccessConf(ctx, tenantID, resID, clients.DefaultInventoryTimeout)
	require.NoError(t, err)
	require.Contains(t, got.GetConfigurationStatus(), "runtime ensure failed:")
}

func TestSpec_RAPReconcile_tracing_enabled_smoke(t *testing.T) {
	dao := arrangeRAPInventoryAndDAO(t)
	rac := dao.CreateRemoteAccessConfiguration(t, integrationTestTenant1)
	tenantID := rac.GetTenantId()
	resID := rac.GetResourceId()

	rec := newTestRAPReconcilerWithOptions(t, newTestRAPReconcilerOptions{
		runtime:        NewInMemoryRAPRuntime(),
		tracingEnabled: true,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req := rec_v2.Request[ReconcilerID]{ID: NewReconcilerID(tenantID, resID)}
	requireAck(t, rec.Reconcile(ctx, req))
}

func TestSpec_RAP_RAM_when_desired_expired_rap_tears_down_and_sets_connection_inactive(t *testing.T) {
	// ADR 0001 target: REMOTE_ACCESS_STATE_EXPIRED / explicit connection_inactive fields in remoteaccess/v1.
	// Those values are not in the API yet; RAM-side "revoke access" is modeled here as desired_state=DISABLED
	// after RAP has published binding (same integration shape RAM uses today to stop a session).
	// Time-based expiry while desired stays ENABLED stays in rap_reconciler_unit_test.go (stub; inventory
	// rejects past expiration_timestamp at create).
	dao := arrangeRAPInventoryAndDAO(t)
	rac := dao.CreateRemoteAccessConfiguration(t, integrationTestTenant1)
	runRAPRevokeViaDesiredDisabled(t, dao, rac.GetTenantId(), rac.GetResourceId())
}

func TestSpec_RAP_RAM_when_connection_inactive_ram_deletes_rac(t *testing.T) {
	// ADR 0001 target: RAM deletes RAC after RAP signals connection inactive. Until that API exists, we model
	// "RAM removed the row" as inventory hard-delete and assert replica cleanup (DisableSession on not found).
	dao := arrangeRAPInventoryAndDAO(t)
	rac := dao.CreateRemoteAccessConfigurationNoCleanup(t, integrationTestTenant1)
	tenantID, resID := rac.GetTenantId(), rac.GetResourceId()
	deletedFromInventory := false
	t.Cleanup(func() {
		if deletedFromInventory {
			return
		}
		dao.HardDeleteRemoteAccessConfiguration(t, tenantID, resID)
	})

	rt := &recordingRAPRuntime{inner: NewInMemoryRAPRuntime()}
	rec := newTestRAPReconcilerWithOptions(t, newTestRAPReconcilerOptions{runtime: rt})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req := rec_v2.Request[ReconcilerID]{ID: NewReconcilerID(tenantID, resID)}

	requireAck(t, rec.Reconcile(ctx, req))
	require.Empty(t, rt.disableReasons, "first reconcile should establish replica state without disable")

	dao.HardDeleteRemoteAccessConfiguration(t, tenantID, resID)
	deletedFromInventory = true

	requireAck(t, rec.Reconcile(ctx, req))
	require.Contains(t, strings.Join(rt.disableReasons, "|"), "inventory record not found")
}

func TestSpec_RAPReconcile_inventory_watcher_controller_path(t *testing.T) {
	dao := arrangeRAPInventoryAndDAO(t)
	cli := integrationRmtAccessCfgClient

	rec, err := NewRAPReconciler(cli, NewInMemoryRAPRuntime(), false, clients.DefaultInventoryTimeout, nil)
	require.NoError(t, err)
	ctl := rec_v2.NewController[ReconcilerID](rec.Reconcile, rec_v2.WithParallelism(1))
	t.Cleanup(func() { ctl.Stop() })

	rac := dao.CreateRemoteAccessConfiguration(t, integrationTestTenant1)
	wantTenant, wantRes := rac.GetTenantId(), rac.GetResourceId()

	ctxWait, cancelWait := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelWait()
	for {
		select {
		case ev, ok := <-cli.Watcher:
			require.True(t, ok, "inventory watcher closed before event")
			if ev.Event.GetEventKind() == inv_v1.SubscribeEventsResponse_EVENT_KIND_DELETED {
				continue
			}
			tID, rID, err := util.GetResourceKeyFromResource(ev.Event.GetResource())
			require.NoError(t, err)
			if tID != wantTenant || rID != wantRes {
				continue
			}
			require.NoError(t, ctl.Reconcile(NewReconcilerID(tID, rID)))
			return
		case <-ctxWait.Done():
			t.Fatal("timeout waiting for inventory watch event for created RAC")
		}
	}
}
