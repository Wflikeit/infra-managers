// SPDX-FileCopyrightText: (C) 2025 Intel Corporation
// SPDX-License-Identifier: Apache-2.0

package reconcilers

import (
	"context"
	"strings"
	"time"

	"github.com/open-edge-platform/cluster-api-provider-intel/pkg/tracing"
	remoteaccessv1 "github.com/open-edge-platform/infra-core/inventory/v2/pkg/api/remoteaccess/v1"
	statusv1 "github.com/open-edge-platform/infra-core/inventory/v2/pkg/api/status/v1"
	"github.com/open-edge-platform/infra-core/inventory/v2/pkg/logging"
	"github.com/open-edge-platform/infra-managers/remote-access/pkg/clients"
	rec_v2 "github.com/open-edge-platform/orch-library/go/pkg/controller/v2"
)

// ramTunnelUpStatusToken must match the substring present in RAP operational status when the
// edge reverse tunnel is observed (see remote-access-proxy rap_reconciler / rap_bootstrap).
const ramTunnelUpStatusToken = "reverse tunnel up"

// Misc variables.
var (
	loggerName = "RAReconciler"
	zlog       = logging.GetLogger(loggerName)
)

type RAReconciler struct {
	netClient        *clients.RmtAccessInventoryClient
	tracingEnabled   bool
	inventoryTimeout time.Duration
}

func NewRAReconciler(cl *clients.RmtAccessInventoryClient, tracingEnabled bool, inventoryTimeout time.Duration) (*RAReconciler, error) {
	return &RAReconciler{netClient: cl, tracingEnabled: tracingEnabled, inventoryTimeout: inventoryTimeout}, nil
}

type SpecStatus struct {
	Readiness SpecReadiness
	Reason    string
}

// Reconcile implements the main reconcile logic.
func (rar *RAReconciler) Reconcile(ctx context.Context, req rec_v2.Request[ReconcilerID]) rec_v2.Directive[ReconcilerID] {
	if rar.tracingEnabled {
		ctx = tracing.StartTrace(ctx, "RemoteAccessManager", "RAReconciler")
		defer tracing.StopTrace(ctx)
	}

	tenantID := req.ID.GetTenantID()
	resourceID := req.ID.GetResourceID()

	ra, d := rar.fetchRA(ctx, tenantID, resourceID, req)
	if d != nil {
		return d
	}

	now := time.Now().UTC()
	spec := evaluateSpecEval(ra, now)

	if rar.shouldSkip(ra, spec) {
		// refresh cache snapshot even on skip
		return req.Ack()
	}

	return rar.reconcileWithSpec(ctx, req, ra, spec, now)
}

func (rar *RAReconciler) fetchRA(
	ctx context.Context,
	tenantID, resourceID string,
	req rec_v2.Request[ReconcilerID],
) (*remoteaccessv1.RemoteAccessConfiguration, rec_v2.Directive[ReconcilerID]) {

	ra, err := rar.netClient.GetRemoteAccessConf(ctx, tenantID, resourceID, rar.inventoryTimeout)
	if d := HandleInventoryError(err, req); d != nil {
		return nil, d
	}

	if ra == nil {
		zlog.Warn().Msgf("RemoteAccessConfiguration %s not found, nothing to reconcile", resourceID)
		return nil, req.Ack()
	}

	return ra, nil
}

func evaluateSpecEval(ra *remoteaccessv1.RemoteAccessConfiguration, now time.Time) SpecStatus {
	r, reason := evaluateSpec(ra, now)
	return SpecStatus{Readiness: r, Reason: reason}
}

// evaluateSpec classifies RemoteAccessConfiguration as READY, PENDING or INVALID
// and returns a human-readable reason used for status text.
func evaluateSpec(ra *remoteaccessv1.RemoteAccessConfiguration, now time.Time) (SpecReadiness, string) {
	if ra == nil {
		return SpecReadinessInvalid, "configuration is nil"
	}

	var fatalIssues []string
	var pendingIssues []string

	checkIdentity(ra, &fatalIssues)
	checkExpiration(ra, now, &fatalIssues, &pendingIssues)
	checkRAPBinding(ra, &pendingIssues)
	checkAuth(ra, &pendingIssues)
	checkDesiredState(ra, &fatalIssues)

	switch {
	case len(fatalIssues) > 0:
		return SpecReadinessInvalid, strings.Join(fatalIssues, "; ")
	case len(pendingIssues) > 0:
		return SpecReadinessPending, strings.Join(pendingIssues, "; ")
	default:
		return SpecReadinessReady, ""
	}
}

// Reconciliation helper to verify if reconciliation is needed.
func (rar *RAReconciler) shouldSkip(ra *remoteaccessv1.RemoteAccessConfiguration, spec SpecStatus) bool {
	return spec.Readiness == SpecReadinessReady && ra.GetDesiredState() == ra.GetCurrentState()
}

func ramSpecReadinessString(r SpecReadiness) string {
	switch r {
	case SpecReadinessReady:
		return "ready"
	case SpecReadinessPending:
		return "pending"
	case SpecReadinessInvalid:
		return "invalid"
	default:
		return "unknown"
	}
}

func (rar *RAReconciler) reconcileWithSpec(
	ctx context.Context,
	req rec_v2.Request[ReconcilerID],
	ra *remoteaccessv1.RemoteAccessConfiguration,
	spec SpecStatus,
	now time.Time,
) rec_v2.Directive[ReconcilerID] {

	tenantID := req.ID.GetTenantID()
	resourceID := ra.GetResourceId()

	zlog.Info().
		Str("tenant_id", tenantID).
		Str("resource_id", resourceID).
		Str("readiness", ramSpecReadinessString(spec.Readiness)).
		Str("reason", spec.Reason).
		Interface("desired", ra.GetDesiredState()).
		Interface("current", ra.GetCurrentState()).
		Msg("RAM reconcile")

	switch spec.Readiness {
	case SpecReadinessInvalid:
		return rar.markError(ctx, req, tenantID, resourceID, spec.Reason, now)

	case SpecReadinessPending:
		return req.Ack()

	case SpecReadinessReady:
		return rar.convergeState(ctx, req, tenantID, resourceID, ra, now)

	default:
		return req.Ack()
	}
}

func (rar *RAReconciler) markError(
	ctx context.Context,
	req rec_v2.Request[ReconcilerID],
	tenantID, resourceID string,
	reason string,
	now time.Time,
) rec_v2.Directive[ReconcilerID] {
	zlog.Warn().
		Str("tenant_id", tenantID).
		Str("resource_id", resourceID).
		Str("reason", reason).
		Time("now", now).
		Msg("RAM markError: RM invalid; configuration_status (operational text) is owned by RAP — not patched here")
	return rar.patchRMInventoryState(
		ctx,
		req,
		tenantID,
		resourceID,
		remoteaccessv1.RemoteAccessState_REMOTE_ACCESS_STATE_ERROR,
		statusv1.StatusIndication_STATUS_INDICATION_ERROR,
	)
}

func (rar *RAReconciler) patchRMInventoryState(
	ctx context.Context,
	req rec_v2.Request[ReconcilerID],
	tenantID, resourceID string,
	current remoteaccessv1.RemoteAccessState,
	indicator statusv1.StatusIndication,
) rec_v2.Directive[ReconcilerID] {
	patch := &remoteaccessv1.RemoteAccessConfiguration{
		ResourceId:                   resourceID,
		CurrentState:                 current,
		ConfigurationStatusIndicator: indicator,
	}
	err := rar.netClient.UpdateRemoteAccessConfigState(ctx, tenantID, resourceID, patch, rar.inventoryTimeout)
	if d := HandleInventoryError(err, req); d != nil {
		return d
	}
	return req.Ack()
}

func ramInventoryShowsOperationalTunnelUp(ra *remoteaccessv1.RemoteAccessConfiguration) bool {
	if ra == nil {
		return false
	}
	if ra.GetConfigurationStatusIndicator() != statusv1.StatusIndication_STATUS_INDICATION_IDLE {
		return false
	}
	return strings.Contains(strings.ToLower(ra.GetConfigurationStatus()), ramTunnelUpStatusToken)
}

func (rar *RAReconciler) convergeState(
	ctx context.Context,
	req rec_v2.Request[ReconcilerID],
	tenantID, resourceID string,
	ra *remoteaccessv1.RemoteAccessConfiguration,
	now time.Time,
) rec_v2.Directive[ReconcilerID] {
	targetState := ra.GetDesiredState()
	if targetState == remoteaccessv1.RemoteAccessState_REMOTE_ACCESS_STATE_UNSPECIFIED {
		targetState = remoteaccessv1.RemoteAccessState_REMOTE_ACCESS_STATE_ERROR
	}

	// Two-step path toward ENABLED (§12.4 B): CONFIGURED = inventory Ready; ENABLED after RAP tunnel text + IDLE indicator.
	if targetState == remoteaccessv1.RemoteAccessState_REMOTE_ACCESS_STATE_ENABLED {
		switch ra.GetCurrentState() {
		case remoteaccessv1.RemoteAccessState_REMOTE_ACCESS_STATE_ENABLED:
			return req.Ack()
		case remoteaccessv1.RemoteAccessState_REMOTE_ACCESS_STATE_CONFIGURED:
			if ramInventoryShowsOperationalTunnelUp(ra) {
				return rar.patchRMInventoryState(
					ctx,
					req,
					tenantID,
					resourceID,
					remoteaccessv1.RemoteAccessState_REMOTE_ACCESS_STATE_ENABLED,
					statusv1.StatusIndication_STATUS_INDICATION_IDLE,
				)
			}
			return req.Ack()
		default:
			return rar.patchRMInventoryState(
				ctx,
				req,
				tenantID,
				resourceID,
				remoteaccessv1.RemoteAccessState_REMOTE_ACCESS_STATE_CONFIGURED,
				statusv1.StatusIndication_STATUS_INDICATION_IDLE,
			)
		}
	}

	if ra.GetCurrentState() == targetState {
		return req.Ack()
	}

	return rar.patchRMInventoryState(
		ctx,
		req,
		tenantID,
		resourceID,
		targetState,
		statusv1.StatusIndication_STATUS_INDICATION_IDLE,
	)
}

type SpecReadiness int

const (
	SpecReadinessReady SpecReadiness = iota
	SpecReadinessPending
	SpecReadinessInvalid
)

func checkIdentity(ra *remoteaccessv1.RemoteAccessConfiguration, fatal *[]string) {
	if strings.TrimSpace(ra.GetResourceId()) == "" {
		*fatal = append(*fatal, "missing resource_id")
	}
	if ra.GetInstance() == nil {
		*fatal = append(*fatal, "missing instance reference")
	}
	if strings.TrimSpace(ra.GetTenantId()) == "" {
		*fatal = append(*fatal, "missing tenant_id")
	}
}

func checkExpiration(
	ra *remoteaccessv1.RemoteAccessConfiguration,
	now time.Time,
	fatal, pending *[]string,
) {
	ts := ra.GetExpirationTimestamp()
	switch {
	case ts == 0:
		*pending = append(*pending, "expiration_timestamp not set yet")
	case int64(ts) <= now.Unix():
		*fatal = append(*fatal, "expiration_timestamp is in the past")
	}
}

func checkRAPBinding(ra *remoteaccessv1.RemoteAccessConfiguration, pending *[]string) {
	if ra.GetLocalPort() == 0 {
		*pending = append(*pending, "local_port not allocated yet by RAP")
	}
	if strings.TrimSpace(ra.GetProxyHost()) == "" {
		*pending = append(*pending, "proxy_host not set yet")
	}
}

func checkAuth(ra *remoteaccessv1.RemoteAccessConfiguration, pending *[]string) {
	if strings.TrimSpace(ra.GetUser()) == "" {
		*pending = append(*pending, "user (SSH user) not set yet")
	}
	if strings.TrimSpace(ra.GetSessionToken()) == "" {
		*pending = append(*pending, "session_token not set yet")
	}
}

func checkDesiredState(ra *remoteaccessv1.RemoteAccessConfiguration, fatal *[]string) {
	if ra.GetDesiredState() == remoteaccessv1.RemoteAccessState_REMOTE_ACCESS_STATE_UNSPECIFIED {
		*fatal = append(*fatal, "desired_state is UNSPECIFIED")
	}
}
