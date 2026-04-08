// SPDX-FileCopyrightText: (C) 2025 Intel Corporation
// SPDX-License-Identifier: Apache-2.0

package reconcilers

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/open-edge-platform/cluster-api-provider-intel/pkg/tracing"
	remoteaccessv1 "github.com/open-edge-platform/infra-core/inventory/v2/pkg/api/remoteaccess/v1"
	"github.com/open-edge-platform/infra-core/inventory/v2/pkg/logging"
	"github.com/open-edge-platform/infra-managers/remote-access-proxy/internal/chiselauth"
	"github.com/open-edge-platform/infra-managers/remote-access-proxy/internal/clients"
	rec_v2 "github.com/open-edge-platform/orch-library/go/pkg/controller/v2"
)

var (
	rapLoggerName = "RAPReconciler"
	zlog          = logging.GetLogger(rapLoggerName)
)

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
	) (connected bool, err error)

	// DisableSession removes any runtime artifacts associated with the resource.
	// It MUST be safe to call even if no session exists.
	DisableSession(
		ctx context.Context,
		tenantID string,
		resourceID string,
		reason string,
	) error
}

type RAPReconciler struct {
	netClient        *clients.RmtAccessInventoryClient
	runtime          RAPRuntime
	tracingEnabled   bool
	inventoryTimeout time.Duration
	chisel           ChiselUserRegistrar
	portAllocMu      sync.Mutex
	resourceToPort   map[string]uint32
	usedPorts        map[uint32]string
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
		resourceToPort:   make(map[string]uint32),
		usedPorts:        make(map[uint32]string),
	}, nil
}

const (
	localPortRangeStart uint32 = 21000
	localPortRangeEnd   uint32 = 21999
)

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
		return r.markError(
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
		return req.Ack()
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
	if d := HandleInventoryError(err, req); d != nil {
		return nil, d
	}

	// Inventory object disappeared -> ensure runtime cleanup
	if ra == nil {
		zlog.Warn().Msgf(
			"RemoteAccessConfiguration %s not found, cleaning up runtime",
			resourceID,
		)
		r.releaseAllocatedPort(tenantID, resourceID)
		_ = r.runtime.DisableSession(ctx, tenantID, resourceID, "inventory record missing")
		return nil, req.Ack()
	}

	return ra, nil
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

// Skip reconciliation only when:
// - spec is READY
// - desired state already equals current state
func (r *RAPReconciler) shouldSkip(
	ra *remoteaccessv1.RemoteAccessConfiguration,
	spec SpecStatus,
) bool {
	return spec.Readiness == SpecReady &&
		ra.GetDesiredState() == ra.GetCurrentState()
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

func (r *RAPReconciler) reconcileWithSpec(
	ctx context.Context,
	req rec_v2.Request[ReconcilerID],
	tenantID string,
	resourceID string,
	ra *remoteaccessv1.RemoteAccessConfiguration,
	spec SpecStatus,
	now time.Time,
) rec_v2.Directive[ReconcilerID] {
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
		r.removeChiselUserFromToken(ra.GetSessionToken())
		r.releaseAllocatedPort(tenantID, resourceID)
		_ = r.runtime.DisableSession(ctx, tenantID, resourceID, "spec invalid: "+spec.Reason)
		return r.markError(ctx, req, tenantID, resourceID, spec.Reason, now)

	case SpecPending:
		if ra.GetDesiredState() == remoteaccessv1.RemoteAccessState_REMOTE_ACCESS_STATE_DISABLED {
			r.removeChiselUserFromToken(ra.GetSessionToken())
			r.releaseAllocatedPort(tenantID, resourceID)
			_ = r.runtime.DisableSession(ctx, tenantID, resourceID, "desired disabled (pending)")
			return req.Ack()
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
			r.releaseAllocatedPort(tenantID, resourceID)
			_ = r.runtime.DisableSession(ctx, tenantID, resourceID, "desired disabled")
			return r.convergeState(ctx, req, tenantID, resourceID, ra, now)
		}

		spec := buildRAPSpec(ra)
		if err := r.syncChiselFromToken(spec.SessionToken); err != nil {
			return r.markError(
				ctx,
				req,
				tenantID,
				resourceID,
				"chisel user sync: "+err.Error(),
				now,
			)
		}
		connected, err := r.runtime.EnsureSession(ctx, tenantID, resourceID, spec)
		if err != nil {
			return r.markError(
				ctx,
				req,
				tenantID,
				resourceID,
				"runtime ensure failed: "+err.Error(),
				now,
			)
		}

		statusText := "remote access configured; waiting for agent connection"
		if connected {
			statusText = "remote access connection active"
		}
		if d := r.persistBinding(ctx, req, tenantID, resourceID, spec); d != nil {
			return d
		}
		if d := r.setConnectionStatus(ctx, req, tenantID, resourceID, statusText, now); d != nil {
			return d
		}

		return r.convergeState(ctx, req, tenantID, resourceID, ra, now)

	default:
		return req.Ack()
	}
}

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
	connected, err := r.runtime.EnsureSession(ctx, tenantID, resourceID, spec)
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
	statusText := "remote access configured; waiting for agent connection"
	if connected {
		statusText = "remote access connection active"
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
		port, err := r.allocateOrGetLocalPort(tenantID, resourceID)
		if err != nil {
			return err
		}
		spec.LocalPort = port
	} else {
		r.reserveKnownPort(tenantID, resourceID, spec.LocalPort)
	}
	if strings.TrimSpace(spec.ProxyHost) == "" {
		spec.ProxyHost = "remote-access-proxy-ws.kind.internal:443"
	}
	if strings.TrimSpace(spec.TargetHost) == "" {
		spec.TargetHost = "127.0.0.1"
	}
	if spec.TargetPort == 0 {
		spec.TargetPort = 22
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

func (r *RAPReconciler) syncChiselFromToken(sessionToken string) error {
	sessionToken = strings.TrimSpace(sessionToken)
	if sessionToken == "" {
		return nil
	}
	user, pass, ok := strings.Cut(sessionToken, ":")
	if !ok || strings.TrimSpace(user) == "" || pass == "" {
		return fmt.Errorf("session_token must be user:pass")
	}
	return r.chisel.EnsureUser(user, pass)
}

func (r *RAPReconciler) removeChiselUserFromToken(sessionToken string) {
	user, _, ok := strings.Cut(strings.TrimSpace(sessionToken), ":")
	if !ok || user == "" {
		return
	}
	r.chisel.RemoveUser(user)
}

func (r *RAPReconciler) allocateOrGetLocalPort(tenantID, resourceID string) (uint32, error) {
	key := sessionKey(tenantID, resourceID)
	r.portAllocMu.Lock()
	defer r.portAllocMu.Unlock()

	if p, ok := r.resourceToPort[key]; ok {
		return p, nil
	}

	for p := localPortRangeStart; p <= localPortRangeEnd; p++ {
		if _, used := r.usedPorts[p]; used {
			continue
		}
		r.resourceToPort[key] = p
		r.usedPorts[p] = key
		return p, nil
	}

	return 0, fmt.Errorf("no free local_port in range %d-%d", localPortRangeStart, localPortRangeEnd)
}

func (r *RAPReconciler) reserveKnownPort(tenantID, resourceID string, port uint32) {
	key := sessionKey(tenantID, resourceID)
	r.portAllocMu.Lock()
	defer r.portAllocMu.Unlock()

	if current, ok := r.resourceToPort[key]; ok {
		if current == port {
			return
		}
		delete(r.usedPorts, current)
	}
	r.resourceToPort[key] = port
	r.usedPorts[port] = key
}

func (r *RAPReconciler) releaseAllocatedPort(tenantID, resourceID string) {
	key := sessionKey(tenantID, resourceID)
	r.portAllocMu.Lock()
	defer r.portAllocMu.Unlock()

	if p, ok := r.resourceToPort[key]; ok {
		delete(r.resourceToPort, key)
		delete(r.usedPorts, p)
	}
}

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
		TargetHost:   spec.TargetHost,
		TargetPort:   spec.TargetPort,
		User:         spec.User,
		SessionToken: spec.SessionToken,
	}
	err := r.netClient.UpdateRemoteAccessConfigBinding(ctx, tenantID, resourceID, patch, r.inventoryTimeout)
	if d := HandleInventoryError(err, req); d != nil {
		return d
	}
	return nil
}

func (r *RAPReconciler) setConnectionStatus(
	ctx context.Context,
	req rec_v2.Request[ReconcilerID],
	tenantID string,
	resourceID string,
	statusText string,
	now time.Time,
) rec_v2.Directive[ReconcilerID] {
	patch := &remoteaccessv1.RemoteAccessConfiguration{
		ResourceId:                   resourceID,
		ConfigurationStatus:          statusText,
		ConfigurationStatusTimestamp: uint64(now.Unix()),
	}
	err := r.netClient.UpdateRemoteAccessConfigState(ctx, tenantID, resourceID, patch, r.inventoryTimeout)
	if d := HandleInventoryError(err, req); d != nil {
		return d
	}
	return nil
}

func (r *RAPReconciler) markError(
	ctx context.Context,
	req rec_v2.Request[ReconcilerID],
	tenantID string,
	resourceID string,
	reason string,
	now time.Time,
) rec_v2.Directive[ReconcilerID] {

	patch := &remoteaccessv1.RemoteAccessConfiguration{
		ResourceId:                   resourceID,
		CurrentState:                 remoteaccessv1.RemoteAccessState_REMOTE_ACCESS_STATE_ERROR,
		ConfigurationStatus:          reason,
		ConfigurationStatusTimestamp: uint64(now.Unix()),
	}

	err := r.netClient.UpdateRemoteAccessConfigState(ctx, tenantID, resourceID, patch, r.inventoryTimeout)
	if d := HandleInventoryError(err, req); d != nil {
		return d
	}
	return req.Ack()
}

func (r *RAPReconciler) convergeState(
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
		CurrentState:                 target,
		ConfigurationStatus:          "remote access proxy reconciled",
		ConfigurationStatusTimestamp: uint64(now.Unix()),
	}

	err := r.netClient.UpdateRemoteAccessConfigState(ctx, tenantID, resourceID, patch, r.inventoryTimeout)
	if d := HandleInventoryError(err, req); d != nil {
		return d
	}
	return req.Ack()
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
