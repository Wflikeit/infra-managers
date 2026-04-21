// SPDX-FileCopyrightText: (C) 2025 Intel Corporation
// SPDX-License-Identifier: Apache-2.0

package reconcilers

import (
	"context"
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
	if spec.LocalPort == 0 {
		port, err := r.ports.allocateOrGet(tenantID, resourceID)
		if err != nil {
			return err
		}
		spec.LocalPort = port
	} else {
		if err := r.ports.reserveKnown(tenantID, resourceID, spec.LocalPort); err != nil {
			return err
		}
	}
	if strings.TrimSpace(spec.ProxyHost) == "" {
		spec.ProxyHost = "remote-access-proxy-ws.kind.internal:443"
	}
	if strings.TrimSpace(spec.User) == "" {
		spec.User = "root"
	}
	if strings.TrimSpace(spec.SessionToken) == "" {
		user := chiselauth.UsernameForRAC(resourceID)
		pass, err := chiselauth.GeneratePasswordHex()
		if err != nil {
			return err
		}
		if err := r.chisel.EnsureUser(user, pass); err != nil {
			return fmt.Errorf("chisel EnsureUser: %w", err)
		}
		spec.SessionToken = user + ":" + pass
	} else if err := r.syncChiselFromToken(spec.SessionToken); err != nil {
		return err
	}
	return nil
}
