// SPDX-FileCopyrightText: (C) 2025 Intel Corporation
// SPDX-License-Identifier: Apache-2.0

package reconcilers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	remoteaccessv1 "github.com/open-edge-platform/infra-core/inventory/v2/pkg/api/remoteaccess/v1"
	"github.com/open-edge-platform/infra-managers/remote-access-proxy/internal/chiselauth"
	rec_v2 "github.com/open-edge-platform/orch-library/go/pkg/controller/v2"
)

func (r *RAPReconciler) tryBootstrapFromPending(
	ctx context.Context,
	req rec_v2.Request[ReconcilerID],
	tenantID string,
	resourceID string,
	ra *remoteaccessv1.RemoteAccessConfiguration,
	now time.Time,
) rec_v2.Directive[ReconcilerID] {
	// Do not bootstrap until expiration is known and valid.
	if ra.GetExpirationTimestamp() == 0 || int64(ra.GetExpirationTimestamp()) <= now.Unix() {
		return nil
	}
	spec := buildRAPSpec(ra)
	if err := r.applyBootstrapDefaults(tenantID, resourceID, spec); err != nil {
		if d := r.setConnectionStatus(
			ctx,
			req,
			tenantID,
			resourceID,
			"bootstrap pending: "+err.Error(),
			now,
		); d != nil {
			return d
		}
		return nil
	}
	conn, err := r.runtime.EnsureSession(ctx, tenantID, resourceID, spec)
	if err != nil {
		// Keep reconciliation non-fatal during bootstrap; expose reason in status.
		if d := r.setConnectionStatus(
			ctx,
			req,
			tenantID,
			resourceID,
			"bootstrap pending: "+err.Error(),
			now,
		); d != nil {
			return d
		}
		return nil
	}
	if d := r.persistBinding(ctx, req, tenantID, resourceID, spec); d != nil {
		return d
	}
	statusText := "remote access proxy ready; waiting for edge agent reverse tunnel"
	if conn.AgentReverseTunnelUp {
		statusText = "remote access connection active (edge agent reverse tunnel up)"
	}
	if d := r.setConnectionStatus(ctx, req, tenantID, resourceID, statusText, now); d != nil {
		return d
	}
	return nil
}

func (r *RAPReconciler) applyBootstrapDefaults(tenantID, resourceID string, spec *RAPSpec) error {
	if spec == nil {
		return nil
	}
	// Bootstrap defaults for single-session bring-up.
	// Local port is allocated from a fixed range without collisions in a single RAP replica.
	if err := r.ensureLocalPort(tenantID, resourceID, spec); err != nil {
		return err
	}
	if strings.TrimSpace(spec.ProxyHost) == "" {
		spec.ProxyHost = "remote-access-proxy-ws.kind.internal:443"
	}
	if strings.TrimSpace(spec.User) == "" {
		spec.User = "root"
	}
	return r.ensureChiselCredential(resourceID, spec)
}

// ensureChiselCredential guarantees spec.SessionToken is present and its chisel user is registered
// on this replica.
//
// Two paths:
//   - spec.SessionToken is empty (fresh bootstrap, or just cleared by self-heal after a port
//     reallocation): generate a new user:pass, EnsureUser on chisel, write back to spec.
//   - spec.SessionToken is populated: parse user:pass and EnsureUser (idempotent on chisel).
//
// This helper is shared by the bootstrap path (pending → first bring-up) and the ready-steady
// reconcile path; both must end with a chisel user that matches spec.SessionToken, otherwise
// agent authentication fails after a RAP restart or a port self-heal.
func (r *RAPReconciler) ensureChiselCredential(resourceID string, spec *RAPSpec) error {
	if strings.TrimSpace(spec.SessionToken) != "" {
		return r.syncChiselFromToken(spec.SessionToken)
	}
	user := chiselauth.UsernameForRAC(resourceID)
	pass, err := chiselauth.GeneratePasswordHex()
	if err != nil {
		return err
	}
	if err := r.chisel.EnsureUser(user, pass); err != nil {
		return fmt.Errorf("chisel EnsureUser: %w", err)
	}
	spec.SessionToken = user + ":" + pass
	return nil
}

// ensureLocalPort populates spec.LocalPort, taking three paths:
//
//  1. LocalPort == 0          → allocate a fresh port from the replica-local pool.
//  2. LocalPort set & free    → reserveKnown succeeds; inventory stays authoritative.
//  3. LocalPort set & in use  → self-heal: the port is held by a different (tenant,resource)
//     on this replica, so we cannot bind to it. We reallocate via allocateOrGet and drop the
//     existing SessionToken so a fresh chisel credential is minted later in this function.
//     persistBinding then rewrites local_port (+user+session_token) in Inventory.
//
// Conflicts are the signature of a prior race on a different replica / ordering issue in
// reconcileAll after a RAP restart (one RAC with local_port=0 grabbed a port another RAC still
// claims in Inventory). Self-heal keeps reconciliation progressing instead of failing forever.
// Out-of-range or pool-exhausted errors are NOT self-healable and bubble up unchanged.
func (r *RAPReconciler) ensureLocalPort(tenantID, resourceID string, spec *RAPSpec) error {
	if spec.LocalPort == 0 {
		port, err := r.ports.allocateOrGet(tenantID, resourceID)
		if err != nil {
			return err
		}
		spec.LocalPort = port
		return nil
	}
	err := r.ports.reserveKnown(tenantID, resourceID, spec.LocalPort)
	if err == nil {
		return nil
	}
	if !errors.Is(err, ErrLocalPortConflict) {
		return err
	}
	oldPort := spec.LocalPort
	newPort, allocErr := r.ports.allocateOrGet(tenantID, resourceID)
	if allocErr != nil {
		zlog.InfraSec().Warn().
			Str("tenant_id", tenantID).
			Str("resource_id", resourceID).
			Uint32("inventory_port", oldPort).
			Err(allocErr).
			Msg("RAP self-heal: reserveKnown conflict and allocator exhausted; cannot reassign port, propagating error")
		return allocErr
	}
	// Force a new chisel credential — the previous SessionToken was paired with oldPort, and a
	// different (tenant,resource) on this replica already owns oldPort, so keeping the old
	// token could let an unrelated session authenticate against the wrong binding.
	spec.LocalPort = newPort
	spec.SessionToken = ""
	zlog.InfraSec().Warn().
		Str("tenant_id", tenantID).
		Str("resource_id", resourceID).
		Uint32("inventory_port", oldPort).
		Uint32("assigned_port", newPort).
		Err(err).
		Msg("RAP self-heal: reserveKnown conflict on inventory-declared local_port; reallocated to a free port and cleared session_token so a fresh chisel credential is minted and persistBinding rewrites the binding in Inventory")
	return nil
}
