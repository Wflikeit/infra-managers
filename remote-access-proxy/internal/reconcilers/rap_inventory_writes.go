// SPDX-FileCopyrightText: (C) 2025 Intel Corporation
// SPDX-License-Identifier: Apache-2.0

package reconcilers

import (
	"context"
	"fmt"
	"strings"
	"time"

	remoteaccessv1 "github.com/open-edge-platform/infra-core/inventory/v2/pkg/api/remoteaccess/v1"
	rec_v2 "github.com/open-edge-platform/orch-library/go/pkg/controller/v2"
)

func (r *RAPReconciler) syncChiselFromToken(sessionToken string) error {
	sessionToken = strings.TrimSpace(sessionToken)
	if sessionToken == "" {
		return nil
	}
	user, pass, ok := strings.Cut(sessionToken, ":")
	user = strings.TrimSpace(user)
	if !ok || user == "" || pass == "" {
		return fmt.Errorf("session_token must be user:pass")
	}
	return r.chisel.EnsureUser(user, pass)
}

func (r *RAPReconciler) removeChiselUserFromToken(sessionToken string) {
	user, _, ok := strings.Cut(strings.TrimSpace(sessionToken), ":")
	user = strings.TrimSpace(user)
	if !ok || user == "" {
		return
	}
	r.chisel.RemoveUser(user)
}

// persistBinding writes every field in the RAP binding mask (see clients.UpdateRemoteAccessConfigBinding).
//
// SessionToken is intentionally included in the same PATCH as host/port binding: RAP is the sole writer
// of this mask; RAM treats session_token as a separate readiness gate (checkAuth) but it must appear in
// inventory for the agent/session story, and bootstrap generates token together with allocated local_port
// in one logical step (rap_bootstrap.applyBootstrapDefaults). So “binding” here means “RAP-owned RAC
// fields that are not operational status / not RM state”, not strictly L3/L4 topology.
//
// A narrower alternative would be a separate Update mask for credentials only (rotation, auditing); that
// would be a deliberate API/client split, not required for correctness today.
func (r *RAPReconciler) persistBinding(
	ctx context.Context,
	req rec_v2.Request[ReconcilerID],
	tenantID string,
	resourceID string,
	spec *RAPSpec,
) rec_v2.Directive[ReconcilerID] {
	if spec == nil {
		return nil
	}
	patch := &remoteaccessv1.RemoteAccessConfiguration{
		ResourceId:   resourceID,
		LocalPort:    spec.LocalPort,
		ProxyHost:    spec.ProxyHost,
		User:         spec.User,
		SessionToken: spec.SessionToken,
	}
	err := r.netClient.UpdateRemoteAccessConfigBinding(ctx, tenantID, resourceID, patch, r.inventoryTimeout)
	if d := HandleInventoryError(err, req); d != nil {
		return d
	}
	return nil
}

// setConnectionStatusCode persists RAP operational status (configuration_status_code + timestamp).
// configuration_status_indicator is owned by RAM (§12.12 B).
func (r *RAPReconciler) setConnectionStatusCode(
	ctx context.Context,
	req rec_v2.Request[ReconcilerID],
	tenantID string,
	resourceID string,
	code remoteaccessv1.RemoteAccessConfigurationStatus,
	now time.Time,
) rec_v2.Directive[ReconcilerID] {
	patch := &remoteaccessv1.RemoteAccessConfiguration{
		ResourceId:                   resourceID,
		ConfigurationStatusCode:      code,
		ConfigurationStatusTimestamp: uint64(now.Unix()),
	}
	err := r.netClient.UpdateRemoteAccessConfigState(ctx, tenantID, resourceID, patch, r.inventoryTimeout)
	if d := HandleInventoryError(err, req); d != nil {
		return d
	}
	return nil
}

// publishRAPOperationalError persists proxy-side failure text via configuration_status (+ timestamp) only.
// RAP inventory client masks do not include current_state or configuration_status_indicator.
func (r *RAPReconciler) publishRAPOperationalError(
	ctx context.Context,
	req rec_v2.Request[ReconcilerID],
	tenantID string,
	resourceID string,
	reason string,
	now time.Time,
) rec_v2.Directive[ReconcilerID] {
	if d := r.setConnectionStatusCode(ctx, req, tenantID, resourceID,
		remoteaccessv1.RemoteAccessConfigurationStatus_REMOTE_ACCESS_CONFIGURATION_STATUS_OPERATIONAL_ERROR, now); d != nil {
		return d
	}
	return req.Ack()
}

// patchRAPReconciledIdleOperationalStatus writes IDLE operational text after RAP-side work.
// It does not write current_state (not in RAP's UpdateRemoteAccessConfigState mask); RAM
// advances current_state when inventory readiness allows.
func (r *RAPReconciler) patchRAPReconciledIdleOperationalStatus(
	ctx context.Context,
	req rec_v2.Request[ReconcilerID],
	tenantID string,
	resourceID string,
	ra *remoteaccessv1.RemoteAccessConfiguration,
	now time.Time,
) rec_v2.Directive[ReconcilerID] {

	target := ra.GetDesiredState()
	if target == remoteaccessv1.RemoteAccessState_REMOTE_ACCESS_STATE_UNSPECIFIED {
		target = remoteaccessv1.RemoteAccessState_REMOTE_ACCESS_STATE_ERROR
	}

	if ra.GetCurrentState() == target {
		return req.Ack()
	}

	patch := &remoteaccessv1.RemoteAccessConfiguration{
		ResourceId:                   resourceID,
		ConfigurationStatusCode:      remoteaccessv1.RemoteAccessConfigurationStatus_REMOTE_ACCESS_CONFIGURATION_STATUS_TUNNEL_ACTIVE,
		ConfigurationStatusTimestamp: uint64(now.Unix()),
	}

	err := r.netClient.UpdateRemoteAccessConfigState(ctx, tenantID, resourceID, patch, r.inventoryTimeout)
	if d := HandleInventoryError(err, req); d != nil {
		return d
	}
	return req.Ack()
}

// rapTeardownAndSignalInactive tears down replica runtime and persists ConnectionInactiveStatus
// so RAM can finalize hard delete after expiry or soft-delete (desired=DELETED).
func (r *RAPReconciler) rapTeardownAndSignalInactive(
	ctx context.Context,
	req rec_v2.Request[ReconcilerID],
	tenantID, resourceID string,
	ra *remoteaccessv1.RemoteAccessConfiguration,
	reason string,
) rec_v2.Directive[ReconcilerID] {
	r.removeChiselUserFromToken(ra.GetSessionToken())
	r.teardownLocalReplicaSession(ctx, tenantID, resourceID, reason)
	if d := r.setConnectionStatusCode(ctx, req, tenantID, resourceID, ConnectionInactiveCode, time.Now().UTC()); d != nil {
		return d
	}
	return req.Ack()
}
