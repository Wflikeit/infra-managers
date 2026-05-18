// SPDX-FileCopyrightText: (C) 2025 Intel Corporation
// SPDX-License-Identifier: Apache-2.0

package reconcilers

import (
	"context"
	"testing"
	"time"

	computev1 "github.com/open-edge-platform/infra-core/inventory/v2/pkg/api/compute/v1"
	remoteaccessv1 "github.com/open-edge-platform/infra-core/inventory/v2/pkg/api/remoteaccess/v1"
	rec_v2 "github.com/open-edge-platform/orch-library/go/pkg/controller/v2"
	"github.com/stretchr/testify/require"
)

type stubRAMInventory struct {
	getResult *remoteaccessv1.RemoteAccessConfiguration
	getErr    error

	softDeleteCalls int
	finalizeCalls   int
	stateUpdates    int
	lastFinalizeRA  *remoteaccessv1.RemoteAccessConfiguration
}

func (s *stubRAMInventory) GetRemoteAccessConf(
	_ context.Context, _, _ string, _ time.Duration,
) (*remoteaccessv1.RemoteAccessConfiguration, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	return s.getResult, nil
}

func (s *stubRAMInventory) UpdateRemoteAccessConfigState(
	_ context.Context, _, _ string, _ *remoteaccessv1.RemoteAccessConfiguration, _ time.Duration,
) error {
	s.stateUpdates++
	return nil
}

func (s *stubRAMInventory) SoftDeleteRemoteAccessConf(
	_ context.Context, _, _ string, _ time.Duration,
) error {
	s.softDeleteCalls++
	return nil
}

func (s *stubRAMInventory) FinalizeRemoteAccessConfDeletion(
	_ context.Context, _, _ string, ra *remoteaccessv1.RemoteAccessConfiguration, _ time.Duration,
) error {
	s.finalizeCalls++
	s.lastFinalizeRA = ra
	return nil
}

func TestSpec_RAReconcile_when_expired_then_soft_delete_without_error_state(t *testing.T) {
	const (
		tenantID = "11111111-1111-1111-1111-111111111111"
		resID    = "rmtacconf-expired-ram"
	)
	stub := &stubRAMInventory{
		getResult: &remoteaccessv1.RemoteAccessConfiguration{
			ResourceId:          resID,
			TenantId:            tenantID,
			DesiredState:        remoteaccessv1.RemoteAccessState_REMOTE_ACCESS_STATE_ENABLED,
			CurrentState:        remoteaccessv1.RemoteAccessState_REMOTE_ACCESS_STATE_ENABLED,
			ExpirationTimestamp: uint64(time.Now().Add(-time.Hour).Unix()),
			Instance:            &computev1.InstanceResource{ResourceId: "instance-1"},
		},
	}
	rec := &RAReconciler{netClient: stub, inventoryTimeout: time.Second}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req := rec_v2.Request[ReconcilerID]{ID: NewReconcilerID(tenantID, resID)}

	d := rec.Reconcile(ctx, req)
	_, ok := d.(*rec_v2.Ack[ReconcilerID])
	require.True(t, ok)
	require.Equal(t, 1, stub.softDeleteCalls)
	require.Equal(t, 0, stub.finalizeCalls)
	require.Equal(t, 0, stub.stateUpdates, "must not patch ERROR current_state on expiry")
}

func TestSpec_RAReconcile_when_expired_and_rap_inactive_then_hard_delete(t *testing.T) {
	const (
		tenantID = "11111111-1111-1111-1111-111111111111"
		resID    = "rmtacconf-expired-finalize"
	)
	stub := &stubRAMInventory{
		getResult: &remoteaccessv1.RemoteAccessConfiguration{
			ResourceId:          resID,
			TenantId:            tenantID,
			DesiredState:        remoteaccessv1.RemoteAccessState_REMOTE_ACCESS_STATE_DELETED,
			CurrentState:        remoteaccessv1.RemoteAccessState_REMOTE_ACCESS_STATE_ENABLED,
			ExpirationTimestamp: uint64(time.Now().Add(-time.Hour).Unix()),
			ConfigurationStatusCode: remoteaccessv1.RemoteAccessConfigurationStatus_REMOTE_ACCESS_CONFIGURATION_STATUS_CONNECTION_INACTIVE,
			Instance:            &computev1.InstanceResource{ResourceId: "instance-1"},
		},
	}
	rec := &RAReconciler{netClient: stub, inventoryTimeout: time.Second}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req := rec_v2.Request[ReconcilerID]{ID: NewReconcilerID(tenantID, resID)}

	d := rec.Reconcile(ctx, req)
	_, ok := d.(*rec_v2.Ack[ReconcilerID])
	require.True(t, ok)
	require.Equal(t, 0, stub.softDeleteCalls)
	require.Equal(t, 1, stub.finalizeCalls)
	require.NotNil(t, stub.lastFinalizeRA)
	require.Equal(t, "instance-1", stub.lastFinalizeRA.GetInstance().GetResourceId())
}
