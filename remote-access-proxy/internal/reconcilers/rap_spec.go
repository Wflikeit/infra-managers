// SPDX-FileCopyrightText: (C) 2025 Intel Corporation
// SPDX-License-Identifier: Apache-2.0

package reconcilers

import (
	"context"
	"strings"
	"time"

	remoteaccessv1 "github.com/open-edge-platform/infra-core/inventory/v2/pkg/api/remoteaccess/v1"
)

// SessionConnectivity describes how far the edge path is on this RAP instance.
// The reconciler must sync the Chisel user before EnsureSession. AgentReverseTunnelUp is a
// best-effort probe: TCP accept on probeHost:localPort (same shape as /term reverse dial) indicates
// the Chisel reverse remote from the edge agent is listening.
type SessionConnectivity struct {
	AgentReverseTunnelUp bool
}

// RAPRuntime represents in-memory / runtime state of the Remote Access Proxy.
// It MUST NOT perform inventory reconciliation logic.
// It MUST NOT decide desired/current state transitions.
// Its only responsibility is to ensure or tear down runtime sessions.
type RAPRuntime interface {

	// EnsureSession ensures that a runtime session exists for the given spec.
	// It may create, update or refresh an existing session.
	// It MUST be idempotent.
	EnsureSession(
		ctx context.Context,
		tenantID string,
		resourceID string,
		spec *RAPSpec,
	) (SessionConnectivity, error)

	// DisableSession removes in-memory runtime artifacts for the resource.
	// The reconciler also releases replica-local port bindings in the same teardown step
	// (see RAPReconciler.teardownLocalReplicaSession).
	// It MUST be safe to call even if no session exists.
	DisableSession(
		ctx context.Context,
		tenantID string,
		resourceID string,
		reason string,
	) error
}

// RAPSpec is a pure runtime view of RemoteAccessConfiguration.
// It contains only fields required by the proxy runtime.
type RAPSpec struct {
	ResourceID string
	TenantID   string

	ProxyHost string
	LocalPort uint32

	TargetHost string
	TargetPort uint32

	User         string
	SessionToken string

	DesiredState remoteaccessv1.RemoteAccessState
	ExpirationTs uint64 // unix seconds
}

func buildRAPSpec(
	ra *remoteaccessv1.RemoteAccessConfiguration,
) *RAPSpec {
	return &RAPSpec{
		ResourceID:   ra.GetResourceId(),
		TenantID:     ra.GetTenantId(),
		ProxyHost:    ra.GetProxyHost(),
		LocalPort:    ra.GetLocalPort(),
		TargetHost:   ra.GetTargetHost(),
		TargetPort:   ra.GetTargetPort(),
		User:         ra.GetUser(),
		SessionToken: ra.GetSessionToken(),
		DesiredState: ra.GetDesiredState(),
		ExpirationTs: ra.GetExpirationTimestamp(),
	}
}

// SpecReadiness describes whether the configuration can be applied by RAP.
type SpecReadiness int

const (
	SpecReady SpecReadiness = iota
	SpecPending
	SpecInvalid
)

type SpecStatus struct {
	Readiness SpecReadiness
	Reason    string
}

// evaluateSpec classifies the configuration without performing side effects.
func evaluateSpec(
	ra *remoteaccessv1.RemoteAccessConfiguration,
	now time.Time,
) SpecStatus {

	if ra == nil {
		return SpecStatus{SpecInvalid, "configuration is nil"}
	}

	var fatal []string
	var pending []string

	checkIdentity(ra, &fatal)
	checkDesiredState(ra, &fatal)
	checkExpirationForRAP(ra, now, &fatal, &pending)
	checkRAPBinding(ra, &pending)
	checkAgentTarget(ra, &pending)
	checkAuth(ra, &pending)

	switch {
	case len(fatal) > 0:
		return SpecStatus{SpecInvalid, strings.Join(fatal, "; ")}
	case len(pending) > 0:
		return SpecStatus{SpecPending, strings.Join(pending, "; ")}
	default:
		return SpecStatus{SpecReady, ""}
	}
}

// rapDesiredStateNeedsRuntimeChisel reports whether this RAC should have a Chisel user
// and proxy runtime session while desired/current are aligned (skip path).
func rapDesiredStateNeedsRuntimeChisel(ds remoteaccessv1.RemoteAccessState) bool {
	switch ds {
	case remoteaccessv1.RemoteAccessState_REMOTE_ACCESS_STATE_ENABLED,
		remoteaccessv1.RemoteAccessState_REMOTE_ACCESS_STATE_CONFIGURED:
		return true
	default:
		return false
	}
}

func specReadinessString(r SpecReadiness) string {
	switch r {
	case SpecReady:
		return "ready"
	case SpecPending:
		return "pending"
	case SpecInvalid:
		return "invalid"
	default:
		return "unknown"
	}
}

func checkIdentity(
	ra *remoteaccessv1.RemoteAccessConfiguration,
	fatal *[]string,
) {
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

func checkDesiredState(
	ra *remoteaccessv1.RemoteAccessConfiguration,
	fatal *[]string,
) {
	if ra.GetDesiredState() ==
		remoteaccessv1.RemoteAccessState_REMOTE_ACCESS_STATE_UNSPECIFIED {
		*fatal = append(*fatal, "desired_state is UNSPECIFIED")
	}
}

func isExpiredInvalid(spec SpecStatus) bool {
	return spec.Readiness == SpecInvalid &&
		strings.Contains(spec.Reason, "expiration_timestamp is in the past")
}

// Expiration is fatal only when enabling.
func checkExpirationForRAP(
	ra *remoteaccessv1.RemoteAccessConfiguration,
	now time.Time,
	fatal *[]string,
	pending *[]string,
) {
	if ra.GetDesiredState() ==
		remoteaccessv1.RemoteAccessState_REMOTE_ACCESS_STATE_DISABLED {
		return
	}

	ts := ra.GetExpirationTimestamp()
	switch {
	case ts == 0:
		*pending = append(*pending, "expiration_timestamp not set")
	case int64(ts) <= now.Unix():
		*fatal = append(*fatal, "expiration_timestamp is in the past")
	}
}

// Fields typically allocated by manager/proxy.
func checkRAPBinding(
	ra *remoteaccessv1.RemoteAccessConfiguration,
	pending *[]string,
) {
	if ra.GetLocalPort() == 0 {
		*pending = append(*pending, "local_port not allocated")
	}
	if strings.TrimSpace(ra.GetProxyHost()) == "" {
		*pending = append(*pending, "proxy_host not set")
	}
}

// Without target, agent cannot expose SSH endpoint.
func checkAgentTarget(
	ra *remoteaccessv1.RemoteAccessConfiguration,
	pending *[]string,
) {
	if strings.TrimSpace(ra.GetTargetHost()) == "" {
		*pending = append(*pending, "target_host not set")
	}
	if ra.GetTargetPort() == 0 {
		*pending = append(*pending, "target_port not set")
	}
}

func checkAuth(
	ra *remoteaccessv1.RemoteAccessConfiguration,
	pending *[]string,
) {
	if strings.TrimSpace(ra.GetUser()) == "" {
		*pending = append(*pending, "user not set")
	}
	if strings.TrimSpace(ra.GetSessionToken()) == "" {
		*pending = append(*pending, "session_token not set")
	}
}
