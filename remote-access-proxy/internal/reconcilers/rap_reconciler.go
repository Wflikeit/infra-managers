// SPDX-FileCopyrightText: (C) 2025 Intel Corporation
// SPDX-License-Identifier: Apache-2.0

package reconcilers

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/open-edge-platform/cluster-api-provider-intel/pkg/tracing"
	remoteaccessv1 "github.com/open-edge-platform/infra-core/inventory/v2/pkg/api/remoteaccess/v1"
	inv_errors "github.com/open-edge-platform/infra-core/inventory/v2/pkg/errors"
	"github.com/open-edge-platform/infra-core/inventory/v2/pkg/logging"
	"github.com/open-edge-platform/infra-managers/remote-access-proxy/internal/clients"
	rec_v2 "github.com/open-edge-platform/orch-library/go/pkg/controller/v2"
)

// rapInventoryClient is the inventory subset used by RAPReconciler.
// Implemented by *clients.RmtAccessInventoryClient; tests may provide stubs.
type rapInventoryClient interface {
	GetRemoteAccessConf(ctx context.Context, tenantID, resourceID string, timeout time.Duration) (*remoteaccessv1.RemoteAccessConfiguration, error)
	UpdateRemoteAccessConfigState(ctx context.Context, tenantID, resourceID string, remAccessConf *remoteaccessv1.RemoteAccessConfiguration, timeout time.Duration) error
	UpdateRemoteAccessConfigBinding(ctx context.Context, tenantID, resourceID string, remAccessConf *remoteaccessv1.RemoteAccessConfiguration, timeout time.Duration) error
}

var (
	rapLoggerName = "RAPReconciler"
	zlog          = logging.GetLogger(rapLoggerName)
)

type RAPReconciler struct {
	netClient        rapInventoryClient
	runtime          RAPRuntime
	tracingEnabled   bool
	inventoryTimeout time.Duration
	chisel           ChiselUserRegistrar
	ports            *localPortAllocator
}

func NewRAPReconciler(
	cl *clients.RmtAccessInventoryClient,
	runtime RAPRuntime,
	tracingEnabled bool,
	inventoryTimeout time.Duration,
	chisel ChiselUserRegistrar,
) (*RAPReconciler, error) {
	if chisel == nil {
		chisel = noopChiselRegistrar{}
	}
	return &RAPReconciler{
		netClient:        cl,
		runtime:          runtime,
		tracingEnabled:   tracingEnabled,
		inventoryTimeout: inventoryTimeout,
		chisel:           chisel,
		ports:            newLocalPortAllocator(),
	}, nil
}

func (r *RAPReconciler) Reconcile(
	ctx context.Context,
	req rec_v2.Request[ReconcilerID],
) rec_v2.Directive[ReconcilerID] {

	if r.tracingEnabled {
		ctx = tracing.StartTrace(ctx, "RemoteAccessProxy", "RAPReconciler")
		defer tracing.StopTrace(ctx)
	}

	tenantID := req.ID.GetTenantID()
	resourceID := req.ID.GetResourceID()
	now := time.Now().UTC()

	if r.runtime == nil {
		return r.publishRAPOperationalError(
			ctx,
			req,
			tenantID,
			resourceID,
			"rap runtime not configured",
			now,
		)
	}

	ra, d := r.fetchRemoteAccess(ctx, tenantID, resourceID, req)
	if d != nil {
		return d
	}

	specStatus := evaluateSpec(ra, now)

	if r.shouldSkip(ra, specStatus) {
		// Inventory is stable (desired == current, spec ready) but the RAP process may have
		// restarted: Chisel keeps users only in memory, so we must re-apply session_token
		// and runtime session or agents cannot authenticate until something bumps the RAC.
		zlog.Info().
			Str("tenant_id", tenantID).
			Str("resource_id", resourceID).
			Interface("desired_state", ra.GetDesiredState()).
			Interface("current_state", ra.GetCurrentState()).
			Msg("RAP reconcile: skip full spec path — inventory stable (spec ready, desired==current); will refresh Chisel + runtime from RAC if needed (session_token from inventory, password never logged)")
		return r.ensureChiselRuntimeIfSkipped(ctx, req, tenantID, resourceID, ra, specStatus, now)
	}

	return r.reconcileWithSpec(ctx, req, tenantID, resourceID, ra, specStatus, now)
}

func (r *RAPReconciler) fetchRemoteAccess(
	ctx context.Context,
	tenantID string,
	resourceID string,
	req rec_v2.Request[ReconcilerID],
) (*remoteaccessv1.RemoteAccessConfiguration, rec_v2.Directive[ReconcilerID]) {

	ra, err := r.netClient.GetRemoteAccessConf(ctx, tenantID, resourceID, r.inventoryTimeout)
	if err != nil {
		if inv_errors.IsNotFound(err) {
			zlog.Warn().
				Str("tenant_id", tenantID).
				Str("resource_id", resourceID).
				Msg("RemoteAccessConfiguration not found in inventory, cleaning up replica runtime")
			r.teardownLocalReplicaSession(ctx, tenantID, resourceID, "inventory record not found")
			return nil, req.Ack()
		}
		// Inventory rejects ids that do not map to a known prefix before lookup; there is no
		// authoritative row RAP can converge — same replica cleanup semantics as NotFound.
		if inv_errors.IsInvalidArgument(err) &&
			strings.Contains(err.Error(), "does not match any known ResourcePrefix") {
			zlog.Warn().
				Str("tenant_id", tenantID).
				Str("resource_id", resourceID).
				Msg("RemoteAccessConfiguration id not recognized by inventory, cleaning up replica runtime")
			r.teardownLocalReplicaSession(ctx, tenantID, resourceID, "inventory record not found")
			return nil, req.Ack()
		}
		if d := HandleInventoryError(err, req); d != nil {
			return nil, d
		}
	}

	// Defensive: Get succeeded but payload missing (should not happen with a validating client).
	if ra == nil {
		zlog.Warn().Msgf(
			"RemoteAccessConfiguration %s not found, cleaning up runtime",
			resourceID,
		)
		r.teardownLocalReplicaSession(ctx, tenantID, resourceID, "inventory record missing")
		return nil, req.Ack()
	}

	return ra, nil
}

// teardownLocalReplicaSession releases replica-local port bindings and runtime session state.
// Only RAP establishes these for a RAC on a replica; callers should use this instead of
// pairing ports.release with runtime.DisableSession by hand.
func (r *RAPReconciler) teardownLocalReplicaSession(ctx context.Context, tenantID, resourceID, reason string) {
	if r.ports != nil {
		r.ports.release(tenantID, resourceID)
	}
	if r.runtime != nil {
		_ = r.runtime.DisableSession(ctx, tenantID, resourceID, reason)
	}
}

// Skip full reconciliation only when:
// - spec is READY
// - desired state already equals current state
//
// Even then, ensureChiselRuntimeIfSkipped may still run so Chisel users and runtime
// sessions are re-applied after a RAP restart (in-memory loss).
func (r *RAPReconciler) shouldSkip(
	ra *remoteaccessv1.RemoteAccessConfiguration,
	spec SpecStatus,
) bool {
	return spec.Readiness == SpecReady &&
		ra.GetDesiredState() == ra.GetCurrentState()
}

func (r *RAPReconciler) ensureChiselRuntimeIfSkipped(
	ctx context.Context,
	req rec_v2.Request[ReconcilerID],
	tenantID string,
	resourceID string,
	ra *remoteaccessv1.RemoteAccessConfiguration,
	spec SpecStatus,
	now time.Time,
) rec_v2.Directive[ReconcilerID] {
	if spec.Readiness != SpecReady {
		zlog.Debug().
			Str("tenant_id", tenantID).
			Str("resource_id", resourceID).
			Msg("RAP skip path: spec not ready (unexpected here); Ack without Chisel refresh")
		return req.Ack()
	}
	if !rapDesiredStateNeedsRuntimeChisel(ra.GetDesiredState()) {
		zlog.Info().
			Str("tenant_id", tenantID).
			Str("resource_id", resourceID).
			Interface("desired_state", ra.GetDesiredState()).
			Msg("RAP skip path: desired state does not require Chisel/runtime on replica; Ack")
		return req.Ack()
	}

	specLocal := buildRAPSpec(ra)
	// Best-effort allocator seed on the skip path: inventory is stable, so we just need the
	// allocator to learn which port this RAC already owns. On conflict we only log — the skip
	// path does not own Inventory writes, so self-heal belongs in the full reconcile below.
	// Out-of-range / exhaustion errors are surfaced as operational status so the operator sees
	// the inconsistency even when nothing else will run for this RAC until desired changes.
	if specLocal.LocalPort != 0 {
		if err := r.ports.reserveKnown(tenantID, resourceID, specLocal.LocalPort); err != nil {
			if errors.Is(err, ErrLocalPortConflict) {
				zlog.InfraSec().Warn().
					Str("tenant_id", tenantID).
					Str("resource_id", resourceID).
					Uint32("inventory_port", specLocal.LocalPort).
					Err(err).
					Msg("RAP skip path: allocator seed detected a conflict with another (tenant,resource) on this replica; deferring heal to the next full reconcile (skip path does not write to Inventory)")
			} else {
				return r.publishRAPOperationalError(
					ctx,
					req,
					tenantID,
					resourceID,
					"port allocator (skip path): "+err.Error(),
					now,
				)
			}
		}
	}
	token := strings.TrimSpace(specLocal.SessionToken)
	chiselUser := chiselUsernameForLog(specLocal.SessionToken)
	if token == "" {
		zlog.Warn().
			Str("tenant_id", tenantID).
			Str("resource_id", resourceID).
			Msg("RAP skip path: session_token empty in RAC — cannot re-register Chisel user after restart; agent auth will fail until token is set")
	} else {
		zlog.Info().
			Str("tenant_id", tenantID).
			Str("resource_id", resourceID).
			Str("chisel_user", chiselUser).
			Bool("session_token_valid_shape", chiselUser != "").
			Msg("RAP skip path: syncChiselFromToken from RAC inventory (user logged if parseable; password never logged)")
	}
	if err := r.syncChiselFromToken(specLocal.SessionToken); err != nil {
		return r.publishRAPOperationalError(
			ctx,
			req,
			tenantID,
			resourceID,
			"chisel user sync: "+err.Error(),
			now,
		)
	}
	// Connectivity snapshot is irrelevant on the skip path; Chisel user + session refresh is enough.
	if _, err := r.runtime.EnsureSession(ctx, tenantID, resourceID, specLocal); err != nil {
		return r.publishRAPOperationalError(
			ctx,
			req,
			tenantID,
			resourceID,
			"runtime ensure failed: "+err.Error(),
			now,
		)
	}
	zlog.Info().
		Str("tenant_id", tenantID).
		Str("resource_id", resourceID).
		Str("chisel_user", chiselUser).
		Msg("RAP skip path: Chisel EnsureUser + runtime EnsureSession completed from RAC; Ack")
	return req.Ack()
}

func (r *RAPReconciler) reconcileWithSpec(
	ctx context.Context,
	req rec_v2.Request[ReconcilerID],
	tenantID string,
	resourceID string,
	ra *remoteaccessv1.RemoteAccessConfiguration,
	spec SpecStatus,
	now time.Time,
) rec_v2.Directive[ReconcilerID] {
	// Expired and RAM already set ERROR: tear down replica session (ports + runtime) then ack
	// without inventory writes — RAM owns current_state (§12.14 (10)).
	if isExpiredInvalid(spec) &&
		ra.GetCurrentState() == remoteaccessv1.RemoteAccessState_REMOTE_ACCESS_STATE_ERROR {
		r.removeChiselUserFromToken(ra.GetSessionToken())
		r.teardownLocalReplicaSession(ctx, tenantID, resourceID, "expired RAC with RAM ERROR: replica cleanup")
		zlog.Debug().
			Str("tenant_id", tenantID).
			Str("resource_id", resourceID).
			Msg("RAP: teardown then ack (expired RAC, current_state=ERROR set by RAM)")
		return req.Ack()
	}

	zlog.Debug().Msgf(
		"Reconciling RAP for %s: current=%v desired=%v readiness=%v",
		resourceID,
		ra.GetCurrentState(),
		ra.GetDesiredState(),
		spec.Readiness,
	)

	zlog.Info().
		Str("tenant_id", tenantID).
		Str("resource_id", resourceID).
		Str("readiness", specReadinessString(spec.Readiness)).
		Str("reason", spec.Reason).
		Interface("desired", ra.GetDesiredState()).
		Interface("current", ra.GetCurrentState()).
		Msg("RAP reconcile")

	switch spec.Readiness {

	case SpecInvalid:
		if isExpiredInvalid(spec) || ra.GetDesiredState() == remoteaccessv1.RemoteAccessState_REMOTE_ACCESS_STATE_DELETED {
			// RAP may refresh configuration_status (operational text). RAM owns desired_state and
			// performs soft/hard delete on expiry; RAP signals connection inactive after teardown.
			return r.rapTeardownAndSignalInactive(ctx, req, tenantID, resourceID, ra, "expired or deleted")
		}
		r.removeChiselUserFromToken(ra.GetSessionToken())
		r.teardownLocalReplicaSession(ctx, tenantID, resourceID, "spec invalid: "+spec.Reason)
		// Invalid spec for reasons other than expiry (identity, binding, etc.): RAM owns current_state / ERROR.
		return req.Ack()

	case SpecPending:
		if ra.GetDesiredState() == remoteaccessv1.RemoteAccessState_REMOTE_ACCESS_STATE_DISABLED {
			r.removeChiselUserFromToken(ra.GetSessionToken())
			r.teardownLocalReplicaSession(ctx, tenantID, resourceID, "desired disabled (pending)")
			return req.Ack()
		}
		if ra.GetDesiredState() == remoteaccessv1.RemoteAccessState_REMOTE_ACCESS_STATE_DELETED {
			return r.rapTeardownAndSignalInactive(ctx, req, tenantID, resourceID, ra, "desired deleted (pending)")
		}

		// Break the bootstrap deadlock: even when spec is still pending, attempt runtime bootstrap
		// so RAP can populate binding fields in Inventory and unblock RAM readiness.
		if d := r.tryBootstrapFromPending(ctx, req, tenantID, resourceID, ra, now); d != nil {
			return d
		}
		return req.Ack()

	case SpecReady:
		if ra.GetDesiredState() == remoteaccessv1.RemoteAccessState_REMOTE_ACCESS_STATE_DISABLED {
			r.removeChiselUserFromToken(ra.GetSessionToken())
			r.teardownLocalReplicaSession(ctx, tenantID, resourceID, "desired disabled")
			return r.patchRAPReconciledIdleOperationalStatus(ctx, req, tenantID, resourceID, ra, now)
		}
		if ra.GetDesiredState() == remoteaccessv1.RemoteAccessState_REMOTE_ACCESS_STATE_DELETED {
			return r.rapTeardownAndSignalInactive(ctx, req, tenantID, resourceID, ra, "desired deleted")
		}

		spec := buildRAPSpec(ra)
		// Seed / reconcile the replica-local allocator for this RAC. Without this call the
		// allocator is untouched for RACs whose spec is already ready-steady at startup, which
		// means a later bootstrap path could hand out a port that is already claimed in
		// Inventory. ensureLocalPort also self-heals on conflict by reallocating + clearing
		// spec.SessionToken; ensureChiselCredential then mints a fresh credential so the
		// binding PATCH below (persistBinding) is internally consistent.
		if err := r.ensureLocalPort(tenantID, resourceID, spec); err != nil {
			return r.publishRAPOperationalError(
				ctx,
				req,
				tenantID,
				resourceID,
				"port allocator: "+err.Error(),
				now,
			)
		}
		if err := r.ensureChiselCredential(resourceID, spec); err != nil {
			return r.publishRAPOperationalError(
				ctx,
				req,
				tenantID,
				resourceID,
				"chisel user sync: "+err.Error(),
				now,
			)
		}
		conn, err := r.runtime.EnsureSession(ctx, tenantID, resourceID, spec)
		if err != nil {
			return r.publishRAPOperationalError(
				ctx,
				req,
				tenantID,
				resourceID,
				"runtime ensure failed: "+err.Error(),
				now,
			)
		}

		if d := r.persistBinding(ctx, req, tenantID, resourceID, spec); d != nil {
			return d
		}
		if d := r.setConnectionStatusCode(ctx, req, tenantID, resourceID, rapTunnelStatusCode(conn.AgentReverseTunnelUp), now); d != nil {
			return d
		}

		// Once the reverse path is up, publish IDLE operational text. RAM advances current_state when readiness allows.
		// Until then RAM may still observe current != desired; operational detail is in configuration_status above.
		if conn.AgentReverseTunnelUp {
			return r.patchRAPReconciledIdleOperationalStatus(ctx, req, tenantID, resourceID, ra, now)
		}
		zlog.Debug().
			Str("tenant_id", tenantID).
			Str("resource_id", resourceID).
			Msg("RAP: skip idle operational patch until edge agent reverse tunnel is listening on local_port")
		return req.Ack()

	default:
		return req.Ack()
	}
}

// chiselUsernameForLog returns the user part of session_token for logs only (never the password).
func chiselUsernameForLog(sessionToken string) string {
	t := strings.TrimSpace(sessionToken)
	if t == "" {
		return ""
	}
	u, _, ok := strings.Cut(t, ":")
	if !ok {
		return ""
	}
	return strings.TrimSpace(u)
}
