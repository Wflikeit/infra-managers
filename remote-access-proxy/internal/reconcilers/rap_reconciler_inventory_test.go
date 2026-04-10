// SPDX-FileCopyrightText: (C) 2025 Intel Corporation
// SPDX-License-Identifier: Apache-2.0

package reconcilers

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	inv_v1 "github.com/open-edge-platform/infra-core/inventory/v2/pkg/api/inventory/v1"
	inv_testing "github.com/open-edge-platform/infra-core/inventory/v2/pkg/testing"
	"github.com/open-edge-platform/infra-managers/remote-access-proxy/internal/clients"
	"github.com/stretchr/testify/require"

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
	rec, err := NewRAPReconciler(
		integrationRmtAccessCfgClient,
		NewInMemoryRAPRuntime(),
		false,
		clients.DefaultInventoryTimeout,
		nil,
	)
	require.NoError(t, err)
	return rec
}

func requireAck(t *testing.T, d rec_v2.Directive[ReconcilerID]) {
	t.Helper()
	_, ok := d.(*rec_v2.Ack[ReconcilerID])
	require.True(t, ok, "expected Ack, got %T", d)
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
	t.Skip("TODO: RAPReconciler.Reconcile early exit when runtime is nil (markError). Requires internal test or exported test constructor; NewRAPReconciler always sets runtime today.")
}

func TestSpec_RAPReconcile_when_inventory_record_missing_then_runtime_cleanup(t *testing.T) {
	t.Skip("TODO: fetchRemoteAccess nil RAC — HardDeleteRemoteAccessConfiguration (or equivalent), Reconcile; assert Ack, DisableSession, port release. See rap_reconciler.go fetchRemoteAccess.")
}

func TestSpec_RAPReconcile_when_inventory_get_transient_error_then_retry_directive(t *testing.T) {
	t.Skip("TODO: assert HandleInventoryError maps gRPC transient errors to request.Retry (rec_v2). May need fault injection or harness hook.")
}

func TestSpec_RAPReconcile_when_spec_ready_and_desired_equals_current_then_ack_without_side_effects(t *testing.T) {
	t.Skip("TODO: shouldSkip — seed RAC SpecReady with desired==current; assert Ack; no UpdateRemoteAccessConfigState/Binding calls (spy client or call counts).")
}

func TestSpec_RAPReconcile_when_spec_invalid_identity_then_mark_error(t *testing.T) {
	t.Skip("TODO: SpecInvalid (missing instance / tenant / resource_id) — assert markError path: ERROR current_state or connection status per policy, DisableSession, chisel cleanup.")
}

func TestSpec_RAPReconcile_when_expiration_in_past_and_current_state_error_then_ack_only(t *testing.T) {
	t.Skip("TODO: isExpiredInvalid + current_state ERROR — RAP acks without noisy writes (see rap_reconciler reconcileWithSpec). Seed RAC via DAO + RM patch if needed.")
}

func TestSpec_RAPReconcile_when_expiration_in_past_then_teardown_and_operational_status(t *testing.T) {
	t.Skip("TODO: today's SpecInvalid expiry path (setConnectionStatus vs markError). After ADR 0001, extend to desired EXPIRED + connection_status signalling.")
}

func TestSpec_RAPReconcile_when_pending_desired_disabled_then_ack_cleanup(t *testing.T) {
	t.Skip("TODO: SpecPending + desired DISABLED — chisel remove, ports.release, DisableSession, Ack (no bootstrap).")
}

func TestSpec_RAPReconcile_when_ready_desired_disabled_then_disable_and_converge_state(t *testing.T) {
	t.Skip("TODO: SpecReady + desired DISABLED — runtime cleanup then convergeState (RAM-owned current_state updates via inventory).")
}

func TestSpec_RAPReconcile_when_chisel_token_sync_fails_then_mark_error(t *testing.T) {
	t.Skip("TODO: SpecReady + invalid session_token — syncChiselFromToken error → markError.")
}

func TestSpec_RAPReconcile_when_runtime_ensure_session_fails_then_mark_error(t *testing.T) {
	t.Skip("TODO: SpecReady + runtime.EnsureSession error (inject failing RAPRuntime) → markError.")
}

func TestSpec_RAPReconcile_when_persist_binding_fails_then_inventory_directive(t *testing.T) {
	t.Skip("TODO: UpdateRemoteAccessConfigBinding failure → HandleInventoryError directive (Ack vs Retry). Fault injection on client or stub.")
}

func TestSpec_RAPReconcile_when_set_connection_status_fails_then_inventory_directive(t *testing.T) {
	t.Skip("TODO: UpdateRemoteAccessConfigState failure on status path — assert directive from HandleInventoryError.")
}

func TestSpec_RAPReconcile_tracing_enabled_smoke(t *testing.T) {
	t.Skip("TODO: NewRAPReconciler(..., tracingEnabled true); single Reconcile with ctx (no panic). Low priority.")
}

func TestSpec_RAP_RAM_when_desired_expired_rap_tears_down_and_sets_connection_inactive(t *testing.T) {
	t.Skip("TODO (ADR 0001): RAM sets desired to agreed EXPIRED/revoked enum; RAP disables session, does not write desired/current_state; RAP sets connection_status INACTIVE/CLOSED (field TBD in proto). Implement after ADR acceptance + inventory API.")
}

func TestSpec_RAP_RAM_when_connection_inactive_ram_deletes_rac(t *testing.T) {
	t.Skip("TODO (ADR 0001): E2E — after RAP signals connection inactive, RAM reconcile deletes RAC (or marks deleted). Cross-component test; may live under RAM repo with RAP fake or full env.")
}

func TestSpec_RAPReconcile_inventory_watcher_controller_path(t *testing.T) {
	t.Skip("TODO: CreateRAController + AssertReconcile pattern (see internal/testing/testing_utils.go) — event → Reconcile ID from watcher.")
}
