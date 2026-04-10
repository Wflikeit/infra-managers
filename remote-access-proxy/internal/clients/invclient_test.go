// SPDX-FileCopyrightText: (C) 2025 Intel Corporation
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package clients_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	inv_v1 "github.com/open-edge-platform/infra-core/inventory/v2/pkg/api/inventory/v1"
	remoteaccessv1 "github.com/open-edge-platform/infra-core/inventory/v2/pkg/api/remoteaccess/v1"
	statusv1 "github.com/open-edge-platform/infra-core/inventory/v2/pkg/api/status/v1"
	"github.com/open-edge-platform/infra-core/inventory/v2/pkg/util"
	"github.com/open-edge-platform/infra-managers/remote-access-proxy/internal/clients"
	rmtAccess_testing "github.com/open-edge-platform/infra-managers/remote-access-proxy/internal/testing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	inv_testing "github.com/open-edge-platform/infra-core/inventory/v2/pkg/testing"
)

func TestMain(m *testing.M) {
	wd, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	projectRoot := filepath.Dir(filepath.Dir(wd))
	policyPath := projectRoot + "/out"
	migrationsDir := projectRoot + "/out"

	inv_testing.StartTestingEnvironment(policyPath, "", migrationsDir)
	code := m.Run()
	inv_testing.StopTestingEnvironment()
	os.Exit(code)
}

// arrangeInventoryWithRAPClient prepares bufconn inventory + RAP inventory client (test cleanup via t).
func arrangeInventoryWithRAPClient(t testing.TB) *inv_testing.InvResourceDAO {
	t.Helper()
	dao := inv_testing.NewInvResourceDAOOrFail(t)
	rmtAccess_testing.CreateRemoteAccessMgrClient(t)
	return dao
}

func TestSpec_EventsOnRemoteAccessConfigurationCreate(t *testing.T) {
	t.Run("when_RAC_is_created_then_watcher_receives_CREATE_with_matching_ids", func(t *testing.T) {
		// arrange
		dao := arrangeInventoryWithRAPClient(t)
		cli := rmtAccess_testing.RmtAccessCfgClient
		rac := dao.CreateRemoteAccessConfiguration(t, rmtAccess_testing.Tenant1)

		// act + assert
		select {
		case ev, ok := <-cli.Watcher:
			require.True(t, ok, "watcher should deliver an event")
			assert.Equal(t, inv_v1.SubscribeEventsResponse_EVENT_KIND_CREATED, ev.Event.EventKind)

			kind, err := util.GetResourceKindFromResourceID(ev.Event.ResourceId)
			require.NoError(t, err)
			assert.Equal(t, inv_v1.ResourceKind_RESOURCE_KIND_RMT_ACCESS_CONF, kind)

			tenantID, resID, err := util.GetResourceKeyFromResource(ev.Event.Resource)
			require.NoError(t, err)
			assert.Equal(t, rmtAccess_testing.Tenant1, tenantID)
			assert.Equal(t, rac.GetResourceId(), resID)
		case <-time.After(2 * time.Second):
			t.Fatal("expected an event on watcher within timeout")
		}
	})
}

func TestSpec_NewRAInventoryClientWithOptions_UnreachableAddress(t *testing.T) {
	t.Run("when_address_is_unreachable_then_New_returns_Unavailable", func(t *testing.T) {
		// Valid host:port; port 1 is almost never bound in test/CI environments, so dial gets
		// connection refused without the listen→close TOCTOU of an ephemeral port.
		const unreachableInventory = "127.0.0.1:1"

		// act — no inventory server at this address
		_, err := clients.NewRAInventoryClientWithOptions(
			clients.WithInventoryAddress(unreachableInventory),
			clients.WithEnableTracing(true),
			clients.WithInsecureGRPC(true),
		)
		// assert
		require.Error(t, err)
		st, ok := status.FromError(err)
		require.True(t, ok, "error should be gRPC status")
		assert.Equal(t, codes.Unavailable, st.Code())
	})
}

func TestSpec_RmtAccessInventoryClient_GetRemoteAccessConf(t *testing.T) {
	dao := arrangeInventoryWithRAPClient(t)
	cli := rmtAccess_testing.RmtAccessCfgClient
	tenantID := rmtAccess_testing.Tenant1

	t.Run("when_resource_id_is_unknown_then_Get_fails_and_returns_nil", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		// arrange — bogus id (wrong prefix for RAC)
		// act
		got, err := cli.GetRemoteAccessConf(ctx, tenantID, "remoteaccess-deadbeef", time.Second)
		// assert
		require.Error(t, err)
		assert.Nil(t, got)
	})

	t.Run("when_RAC_exists_then_Get_returns_same_identity_and_instance", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		// arrange
		rac := dao.CreateRemoteAccessConfiguration(t, tenantID)
		// act
		got, err := cli.GetRemoteAccessConf(ctx, tenantID, rac.GetResourceId(), time.Second)
		// assert
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, rac.GetResourceId(), got.GetResourceId())
		assert.Equal(t, tenantID, got.GetTenantId())
		require.NotNil(t, got.GetInstance())
		assert.Equal(t, rac.GetInstance().GetResourceId(), got.GetInstance().GetResourceId())
		assert.Equal(t, rac.GetDesiredState(), got.GetDesiredState())
	})
}

func TestSpec_RmtAccessInventoryClient_FindRemoteAccessConfigs(t *testing.T) {
	t.Run("when_RAC_exists_then_Find_lists_its_tenant_and_resource_id", func(t *testing.T) {
		// arrange
		dao := arrangeInventoryWithRAPClient(t)
		cli := rmtAccess_testing.RmtAccessCfgClient
		tenantID := rmtAccess_testing.Tenant1
		rac := dao.CreateRemoteAccessConfiguration(t, tenantID)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		// act
		found, err := cli.FindRemoteAccessConfigs(ctx, 5*time.Second)
		// assert
		require.NoError(t, err)
		require.NotEmpty(t, found)
		var match bool
		for _, c := range found {
			if c.GetTenantId() == tenantID && c.GetResourceId() == rac.GetResourceId() {
				match = true
				break
			}
		}
		assert.True(t, match, "expected RAC %s for tenant %s in Find result", rac.GetResourceId(), tenantID)
	})
}

func TestSpec_RmtAccessInventoryClient_UpdateRemoteAccessConfigState(t *testing.T) {
	t.Run("when_RAC_exists_then_patch_configuration_status_fields_persist", func(t *testing.T) {
		// arrange
		dao := arrangeInventoryWithRAPClient(t)
		cli := rmtAccess_testing.RmtAccessCfgClient
		tenantID := rmtAccess_testing.Tenant1
		rac := dao.CreateRemoteAccessConfiguration(t, tenantID)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		base, err := cli.GetRemoteAccessConf(ctx, tenantID, rac.GetResourceId(), time.Second)
		require.NoError(t, err)
		ts := uint64(time.Now().Unix())
		patch := base
		patch.ConfigurationStatus = "rap-test-status"
		patch.ConfigurationStatusIndicator = statusv1.StatusIndication_STATUS_INDICATION_IDLE
		patch.ConfigurationStatusTimestamp = ts

		// act
		err = cli.UpdateRemoteAccessConfigState(ctx, tenantID, rac.GetResourceId(), patch, 5*time.Second)
		require.NoError(t, err)

		// assert
		after, err := cli.GetRemoteAccessConf(ctx, tenantID, rac.GetResourceId(), time.Second)
		require.NoError(t, err)
		assert.Equal(t, "rap-test-status", after.GetConfigurationStatus())
		assert.Equal(t, statusv1.StatusIndication_STATUS_INDICATION_IDLE, after.GetConfigurationStatusIndicator())
		assert.Equal(t, ts, after.GetConfigurationStatusTimestamp())
	})

	t.Run("when_resource_id_does_not_exist_then_Update_returns_error", func(t *testing.T) {
		// arrange
		_ = arrangeInventoryWithRAPClient(t)
		cli := rmtAccess_testing.RmtAccessCfgClient
		tenantID := rmtAccess_testing.Tenant1
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		patch := &remoteaccessv1.RemoteAccessConfiguration{
			ConfigurationStatus:          "x",
			ConfigurationStatusIndicator: statusv1.StatusIndication_STATUS_INDICATION_ERROR,
			ConfigurationStatusTimestamp: 1,
		}
		// act
		err := cli.UpdateRemoteAccessConfigState(ctx, tenantID, "rmtacconf-deadbeef", patch, time.Second)
		// assert
		assert.Error(t, err)
	})
}

func TestSpec_RmtAccessInventoryClient_UpdateRemoteAccessConfigBinding(t *testing.T) {
	t.Run("when_binding_fields_are_valid_then_they_persist_on_read_back", func(t *testing.T) {
		// arrange
		dao := arrangeInventoryWithRAPClient(t)
		cli := rmtAccess_testing.RmtAccessCfgClient
		tenantID := rmtAccess_testing.Tenant1
		rac := dao.CreateRemoteAccessConfiguration(t, tenantID)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		base, err := cli.GetRemoteAccessConf(ctx, tenantID, rac.GetResourceId(), time.Second)
		require.NoError(t, err)
		base.LocalPort = 2222
		base.ProxyHost = "rap.test.local"
		base.TargetHost = "127.0.0.1"
		base.TargetPort = 22
		base.User = "testuser"
		base.SessionToken = "tok"

		// act
		err = cli.UpdateRemoteAccessConfigBinding(ctx, tenantID, rac.GetResourceId(), base, 5*time.Second)
		require.NoError(t, err)

		// assert
		after, err := cli.GetRemoteAccessConf(ctx, tenantID, rac.GetResourceId(), time.Second)
		require.NoError(t, err)
		assert.Equal(t, uint32(2222), after.GetLocalPort())
		assert.Equal(t, "rap.test.local", after.GetProxyHost())
		assert.Equal(t, "127.0.0.1", after.GetTargetHost())
		assert.Equal(t, uint32(22), after.GetTargetPort())
		assert.Equal(t, "testuser", after.GetUser())
		assert.Equal(t, "tok", after.GetSessionToken())
	})

	t.Run("when_local_port_violates_proto_bounds_then_Update_returns_error", func(t *testing.T) {
		// arrange
		dao := arrangeInventoryWithRAPClient(t)
		cli := rmtAccess_testing.RmtAccessCfgClient
		tenantID := rmtAccess_testing.Tenant1
		rac := dao.CreateRemoteAccessConfiguration(t, tenantID)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		base, err := cli.GetRemoteAccessConf(ctx, tenantID, rac.GetResourceId(), time.Second)
		require.NoError(t, err)
		base.LocalPort = 80 // proto requires local_port >= 1024

		// act
		err = cli.UpdateRemoteAccessConfigBinding(ctx, tenantID, rac.GetResourceId(), base, 5*time.Second)
		// assert
		assert.Error(t, err)
	})
}
