// SPDX-FileCopyrightText: (C) 2025 Intel Corporation
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	inv_v1 "github.com/open-edge-platform/infra-core/inventory/v2/pkg/api/inventory/v1"
	"github.com/open-edge-platform/infra-managers/remote-access-proxy/internal/clients"
	"github.com/open-edge-platform/infra-managers/remote-access-proxy/internal/reconcilers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	inv_testing "github.com/open-edge-platform/infra-core/inventory/v2/pkg/testing"
)

const (
	nbHandlerTestClientName = "NBHandlerTestRAPClient"
	nbHandlerTestTenant     = "11111111-1111-1111-1111-111111111111"
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

func arrangeRAPInventoryClient(t *testing.T) *clients.RmtAccessInventoryClient {
	t.Helper()
	resourceKinds := []inv_v1.ResourceKind{inv_v1.ResourceKind_RESOURCE_KIND_RMT_ACCESS_CONF}
	err := inv_testing.CreateClient(inv_testing.ClientType(nbHandlerTestClientName), inv_v1.ClientKind_CLIENT_KIND_RESOURCE_MANAGER, resourceKinds, "")
	require.NoError(t, err)
	cl, err := clients.NewRAInventoryClient(
		inv_testing.TestClients[inv_testing.ClientType(nbHandlerTestClientName)].GetTenantAwareInventoryClient(),
		inv_testing.TestClientsEvents[inv_testing.ClientType(nbHandlerTestClientName)],
	)
	require.NoError(t, err)
	t.Cleanup(func() {
		cl.Stop()
		delete(inv_testing.TestClients, inv_testing.ClientType(nbHandlerTestClientName))
		delete(inv_testing.TestClientsEvents, inv_testing.ClientType(nbHandlerTestClientName))
	})
	return cl
}

func newNBHandler(t *testing.T, cl *clients.RmtAccessInventoryClient) *NBHandler {
	t.Helper()
	h, err := NewNBHandler(cl, false, time.Hour, 1, clients.DefaultInventoryTimeout, clients.ListAllDefaultTimeout, reconcilers.NewChiselServerRegistrar(nil))
	require.NoError(t, err)
	return h
}

func nbHandlerWithRACFilterOnly() *NBHandler {
	return &NBHandler{
		Filters: map[inv_v1.ResourceKind]Filter{
			inv_v1.ResourceKind_RESOURCE_KIND_RMT_ACCESS_CONF: filterEvents,
		},
	}
}

func racSubscribeEvent(
	t *testing.T,
	dao *inv_testing.InvResourceDAO,
	tenant string,
	kind inv_v1.SubscribeEventsResponse_EventKind,
) *inv_v1.SubscribeEventsResponse {
	t.Helper()
	rac := dao.CreateRemoteAccessConfiguration(t, tenant)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	api := inv_testing.TestClients[inv_testing.APIClient].GetTenantAwareInventoryClient()
	gresp, err := api.Get(ctx, tenant, rac.GetResourceId())
	require.NoError(t, err)
	return &inv_v1.SubscribeEventsResponse{
		ClientUuid: "11111111-1111-1111-1111-111111111111",
		ResourceId: rac.GetResourceId(),
		EventKind:  kind,
		Resource:   gresp.GetResource(),
	}
}

func TestSpec_NewNBHandler(t *testing.T) {
	t.Run("when_inventory_client_is_valid_then_handler_is_created", func(t *testing.T) {
		cl := arrangeRAPInventoryClient(t)
		h, err := NewNBHandler(cl, false, time.Minute, 2, clients.DefaultInventoryTimeout, clients.ListAllDefaultTimeout, reconcilers.NewChiselServerRegistrar(nil))
		require.NoError(t, err)
		require.NotNil(t, h)
		require.Contains(t, h.Controllers, inv_v1.ResourceKind_RESOURCE_KIND_RMT_ACCESS_CONF)
		require.Contains(t, h.Filters, inv_v1.ResourceKind_RESOURCE_KIND_RMT_ACCESS_CONF)
	})
}

func TestSpec_NBHandler_reconcileAll(t *testing.T) {
	t.Run("when_inventory_has_no_RAC_then_reconcileAll_succeeds", func(t *testing.T) {
		cl := arrangeRAPInventoryClient(t)
		h := newNBHandler(t, cl)
		err := h.reconcileAll()
		require.NoError(t, err)
	})

	t.Run("when_RAC_exists_then_reconcileAll_succeeds", func(t *testing.T) {
		cl := arrangeRAPInventoryClient(t)
		dao := inv_testing.NewInvResourceDAOOrFail(t)
		dao.CreateRemoteAccessConfiguration(t, nbHandlerTestTenant)
		h := newNBHandler(t, cl)
		err := h.reconcileAll()
		require.NoError(t, err)
	})
}

func TestSpec_NBHandler_reconcileResource(t *testing.T) {
	t.Run("when_resource_id_does_not_parse_then_reconcile_is_noop", func(t *testing.T) {
		cl := arrangeRAPInventoryClient(t)
		h := newNBHandler(t, cl)
		h.reconcileResource(nbHandlerTestTenant, "not-a-resource-id")
	})

	t.Run("when_kind_has_no_registered_controller_then_reconcile_is_noop", func(t *testing.T) {
		cl := arrangeRAPInventoryClient(t)
		h := newNBHandler(t, cl)
		// Valid host ID parses to RESOURCE_KIND_HOST; NBHandler only registers RMT_ACCESS_CONF.
		h.reconcileResource(nbHandlerTestTenant, "host-1234567")
	})
}

func TestSpec_NBHandler_filterEvent(t *testing.T) {
	dao := inv_testing.NewInvResourceDAOOrFail(t)
	nbh := nbHandlerWithRACFilterOnly()

	t.Run("when_RAC_is_deleted_then_filter_drops_event", func(t *testing.T) {
		ev := racSubscribeEvent(t, dao, nbHandlerTestTenant, inv_v1.SubscribeEventsResponse_EVENT_KIND_DELETED)
		assert.False(t, nbh.filterEvent(ev))
	})

	t.Run("when_RAC_is_updated_then_filter_accepts_event", func(t *testing.T) {
		ev := racSubscribeEvent(t, dao, nbHandlerTestTenant, inv_v1.SubscribeEventsResponse_EVENT_KIND_UPDATED)
		assert.True(t, nbh.filterEvent(ev))
	})

	t.Run("when_resource_id_is_unknown_kind_then_filter_accepts_before_kind_gate", func(t *testing.T) {
		ev := &inv_v1.SubscribeEventsResponse{
			ClientUuid: "22222222-2222-2222-2222-222222222222",
			ResourceId: "zzz-ffffffff",
			EventKind:  inv_v1.SubscribeEventsResponse_EVENT_KIND_UPDATED,
		}
		assert.True(t, nbh.filterEvent(ev))
	})

	t.Run("when_kind_is_not_RAC_then_filter_drops_event", func(t *testing.T) {
		ev := &inv_v1.SubscribeEventsResponse{
			ClientUuid: "33333333-3333-3333-3333-333333333333",
			ResourceId: "host-abcdefg",
			EventKind:  inv_v1.SubscribeEventsResponse_EVENT_KIND_UPDATED,
		}
		assert.False(t, nbh.filterEvent(ev))
	})
}

func TestSpec_NBHandler_Start_Stop(t *testing.T) {
	t.Run("when_started_then_stop_drains_control_loop", func(t *testing.T) {
		cl := arrangeRAPInventoryClient(t)
		h := newNBHandler(t, cl)
		h.tickerPeriod = 50 * time.Millisecond
		require.NoError(t, h.Start())

		done := make(chan struct{})
		go func() {
			h.Stop()
			close(done)
		}()

		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("Stop did not complete")
		}
	})
}
