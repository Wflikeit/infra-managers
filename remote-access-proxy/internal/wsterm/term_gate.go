// SPDX-FileCopyrightText: (C) 2025 Intel Corporation
// SPDX-License-Identifier: Apache-2.0

package wsterm

import (
	"encoding/json"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	remoteaccessv1 "github.com/open-edge-platform/infra-core/inventory/v2/pkg/api/remoteaccess/v1"
)

// HTTPErrorBody is the JSON envelope returned when /term rejects the handshake.
type HTTPErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// WriteJSONError writes a JSON error response before WebSocket upgrade.
func WriteJSONError(w http.ResponseWriter, httpStatus int, code, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(httpStatus)
	_ = json.NewEncoder(w).Encode(HTTPErrorBody{Code: code, Message: message})
}

// effectiveSSHUserForBinding resolves the SSH user for binding-readiness checks.
// Prefers ra.User when set; falls back to the ?ssh_user= query parameter when ra.User is empty.
// r may be nil (e.g. from unit tests that do not carry a request context).
func effectiveSSHUserForBinding(ra *remoteaccessv1.RemoteAccessConfiguration, r *http.Request) string {
	if u := strings.TrimSpace(ra.GetUser()); u != "" {
		return u
	}
	if r != nil {
		if u := strings.TrimSpace(r.URL.Query().Get("ssh_user")); u != "" {
			return u
		}
	}
	return ""
}

// RACBindingIncomplete is true when RAC exists but control-plane fields needed for /term are not ready yet.
// r may be nil (tests); when non-nil, a non-empty ssh_user query counts toward the user field requirement.
func RACBindingIncomplete(ra *remoteaccessv1.RemoteAccessConfiguration, r *http.Request) bool {
	if ra.GetExpirationTimestamp() == 0 {
		return true
	}
	if strings.TrimSpace(ra.GetProxyHost()) == "" {
		return true
	}
	if effectiveSSHUserForBinding(ra, r) == "" {
		return true
	}
	if strings.TrimSpace(ra.GetSessionToken()) == "" {
		return true
	}
	return false
}

// TermGateDenied returns non-zero HTTP status if /term must not proceed (before WebSocket upgrade).
// r may be nil; when non-nil it is used to let a ?ssh_user= query satisfy the user binding requirement.
func TermGateDenied(ra *remoteaccessv1.RemoteAccessConfiguration, now time.Time, r *http.Request) (httpStatus int, code, message string) {
	if ra == nil {
		return http.StatusNotFound, "rac_not_found", "Remote access configuration not found."
	}
	if ts := ra.GetExpirationTimestamp(); ts != 0 && int64(ts) <= now.Unix() {
		return http.StatusForbidden, "rac_expired", "Remote access session has expired."
	}
	switch ra.GetCurrentState() {
	case remoteaccessv1.RemoteAccessState_REMOTE_ACCESS_STATE_ERROR:
		msg := "Remote access is in an error state."
		if s := strings.TrimSpace(ra.GetConfigurationStatus()); s != "" {
			msg = s
		}
		return http.StatusForbidden, "rac_error", msg
	case remoteaccessv1.RemoteAccessState_REMOTE_ACCESS_STATE_DISABLED,
		remoteaccessv1.RemoteAccessState_REMOTE_ACCESS_STATE_DELETED:
		return http.StatusForbidden, "rac_inactive", "Remote access is not active for this resource."
	}
	switch ra.GetDesiredState() {
	case remoteaccessv1.RemoteAccessState_REMOTE_ACCESS_STATE_DISABLED,
		remoteaccessv1.RemoteAccessState_REMOTE_ACCESS_STATE_DELETED:
		return http.StatusForbidden, "rac_disabled", "Remote access is disabled for this resource."
	case remoteaccessv1.RemoteAccessState_REMOTE_ACCESS_STATE_ERROR,
		remoteaccessv1.RemoteAccessState_REMOTE_ACCESS_STATE_UNSPECIFIED:
		return http.StatusForbidden, "rac_not_available", "Remote access is not available (invalid desired state)."
	}
	if RACBindingIncomplete(ra, r) {
		return http.StatusServiceUnavailable, "rac_initializing",
			"Remote access is still being provisioned; try again shortly."
	}
	if ra.GetLocalPort() == 0 {
		return http.StatusServiceUnavailable, "tunnel_unavailable",
			"Binding is ready but the reverse port is not available on the proxy yet (edge agent may still be connecting)."
	}
	return 0, "", ""
}

// RouteConfig selects reverse SSH dial target and user for /term.
type RouteConfig struct {
	ReverseSSHAddr string
	SSHUser        string
}

// RouteFromRA builds dial config from RAC query params and defaults.
func RouteFromRA(
	ra *remoteaccessv1.RemoteAccessConfiguration,
	r *http.Request,
	defaultAddr, defaultUser string,
) RouteConfig {
	cfg := RouteConfig{
		ReverseSSHAddr: defaultAddr,
		SSHUser:        defaultUser,
	}
	if qUser := strings.TrimSpace(r.URL.Query().Get("ssh_user")); qUser != "" {
		cfg.SSHUser = qUser
	}
	if ra == nil {
		return cfg
	}
	if ra.GetLocalPort() != 0 {
		cfg.ReverseSSHAddr = net.JoinHostPort("127.0.0.1", strconv.FormatUint(uint64(ra.GetLocalPort()), 10))
	}
	if strings.TrimSpace(r.URL.Query().Get("ssh_user")) == "" {
		if raUser := strings.TrimSpace(ra.GetUser()); raUser != "" {
			cfg.SSHUser = raUser
		}
	}
	return cfg
}
