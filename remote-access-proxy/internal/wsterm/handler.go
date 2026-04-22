// SPDX-FileCopyrightText: (C) 2025 Intel Corporation
// SPDX-License-Identifier: Apache-2.0

package wsterm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	remoteaccessv1 "github.com/open-edge-platform/infra-core/inventory/v2/pkg/api/remoteaccess/v1"
	"github.com/open-edge-platform/infra-core/inventory/v2/pkg/logging"
	"golang.org/x/crypto/ssh"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// InventoryRACGetter loads a RemoteAccessConfiguration for /term when tenant_id and resource_id are set.
// Implemented by *clients.RmtAccessInventoryClient.
//
//go:generate sh -c "cd ../.. && env GOTOOLCHAIN=go1.25.5 go run github.com/vektra/mockery/v2@v2.53.5"
type InventoryRACGetter interface {
	GetRemoteAccessConf(ctx context.Context, tenantID, resourceID string, timeout time.Duration) (*remoteaccessv1.RemoteAccessConfiguration, error)
}

var zlog = logging.GetLogger("RAPWebTerm")

var upgrader = websocket.Upgrader{
	ReadBufferSize:  32 * 1024,
	WriteBufferSize: 32 * 1024,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

// wsMsg matches the JSON terminal framing used by /term.
type wsMsg struct {
	Type string `json:"type"`
	Data string `json:"data,omitempty"`
	Cols int    `json:"cols,omitempty"`
	Rows int    `json:"rows,omitempty"`
}

// HandlerConfig configures the base WebSocket terminal (fixed reverse SSH target).
type HandlerConfig struct {
	ReverseSSHAddr        string
	ReverseSSHWaitTimeout time.Duration
	SSHUser               string
	PrivateKeyPath        string
	Password              string
}

// NewHandler returns /term with a fixed reverse-SSH target and on-disk SSH key/password (no Inventory, no Vault).
// Production RAP uses NewInventoryHandler; this remains for narrow dev/embed cases.
func NewHandler(cfg HandlerConfig) http.HandlerFunc {
	return newTermWS(cfg.ReverseSSHAddr, cfg.ReverseSSHWaitTimeout, cfg.SSHUser, cfg.PrivateKeyPath, cfg.Password, nil)
}

// InventoryHandlerConfig adds Inventory-backed routing when tenant_id and resource_id are set.
type InventoryHandlerConfig struct {
	HandlerConfig
	NetClient        InventoryRACGetter
	InventoryTimeout time.Duration
	// Now supplies the instant for TermGateDenied expiry checks. If nil, time.Now().UTC() is used.
	Now func() time.Time
	// SessionAuth supplies SSH auth for inventory-backed /term (e.g. Vault user certificates).
	// When non-nil, PrivateKeyPath and Password are ignored for that path.
	SessionAuth func(ctx context.Context, ra *remoteaccessv1.RemoteAccessConfiguration, tenantID, resourceID string) ([]ssh.AuthMethod, error)
}

// NewInventoryHandler returns /term with optional RAC lookup and access checks.
func NewInventoryHandler(cfg InventoryHandlerConfig) http.HandlerFunc {
	now := cfg.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	base := newTermWS(cfg.ReverseSSHAddr, cfg.ReverseSSHWaitTimeout, cfg.SSHUser, cfg.PrivateKeyPath, cfg.Password, nil)
	return func(w http.ResponseWriter, r *http.Request) {
		tenantID := strings.TrimSpace(r.URL.Query().Get("tenant_id"))
		resourceID := strings.TrimSpace(r.URL.Query().Get("resource_id"))
		// Per-request Inventory lookup (not startup config): RAC keys identify which reverse-SSH
		// local port (which edge tunnel) to dial. Multiple ENs ⇒ multiple /term sessions with
		// different resource_id values, not a single RAP-wide preload.
		if cfg.NetClient == nil || tenantID == "" || resourceID == "" {
			if cfg.SessionAuth != nil {
				WriteJSONError(w, http.StatusBadRequest, "term_params_required",
					"tenant_id and resource_id are required: they select the RemoteAccessConfiguration in Inventory for this /term session (reverse SSH port per edge node). This is not RAP startup configuration.")
				return
			}
			base(w, r)
			return
		}

		ra, err := cfg.NetClient.GetRemoteAccessConf(r.Context(), tenantID, resourceID, cfg.InventoryTimeout)
		if err != nil {
			if st, ok := status.FromError(err); ok && st.Code() == codes.NotFound {
				WriteJSONError(w, http.StatusNotFound, "rac_not_found",
					"Remote access configuration not found for this tenant and resource.")
			} else {
				zlog.Warn().Err(err).Str("tenant_id", tenantID).Str("resource_id", resourceID).Msg("/term: inventory get failed")
				WriteJSONError(w, http.StatusBadGateway, "inventory_unavailable",
					"Could not load remote access configuration from inventory.")
			}
			return
		}

		if httpSt, code, msg := TermGateDenied(ra, now(), r); httpSt != 0 {
			zlog.Info().
				Str("tenant_id", tenantID).
				Str("resource_id", resourceID).
				Str("deny_code", code).
				Int("http_status", httpSt).
				Msg("/term: access denied")
			WriteJSONError(w, httpSt, code, msg)
			return
		}

		route := RouteFromRA(ra, r, cfg.ReverseSSHAddr, cfg.SSHUser)
		principalForLog, _ := RAPSSHPrincipal(tenantID)
		invAudit := &termSSHSessionAudit{TenantID: tenantID, ResourceID: resourceID, Principal: principalForLog}
		if cfg.SessionAuth != nil {
			authFn := func(ctx context.Context) ([]ssh.AuthMethod, error) {
				return cfg.SessionAuth(ctx, ra, tenantID, resourceID)
			}
			newTermWSWithAuth(route.ReverseSSHAddr, cfg.ReverseSSHWaitTimeout, route.SSHUser, authFn, invAudit)(w, r)
			return
		}
		if route.ReverseSSHAddr == cfg.ReverseSSHAddr && route.SSHUser == cfg.SSHUser {
			base(w, r)
			return
		}
		newTermWS(route.ReverseSSHAddr, cfg.ReverseSSHWaitTimeout, route.SSHUser, cfg.PrivateKeyPath, cfg.Password, invAudit)(w, r)
	}
}

// termSSHSessionAudit enriches /term logs after a successful SSH dial (Inventory-backed paths).
type termSSHSessionAudit struct {
	TenantID   string
	ResourceID string
	Principal  string // e.g. rap:<tenant> for Vault SSH; may be empty if RAPSSHPrincipal failed
}

func newTermWS(
	reverseSSHAddr string,
	reverseSSHWaitTimeout time.Duration,
	sshUser string,
	privateKeyPath string,
	password string,
	audit *termSSHSessionAudit,
) http.HandlerFunc {
	return newTermWSWithAuth(reverseSSHAddr, reverseSSHWaitTimeout, sshUser, func(context.Context) ([]ssh.AuthMethod, error) {
		methods := BuildSSHAuthMethods(privateKeyPath, password)
		if len(methods) == 0 {
			return nil, fmt.Errorf("no ssh auth methods configured")
		}
		return methods, nil
	}, audit)
}

func newTermWSWithAuth(
	reverseSSHAddr string,
	reverseSSHWaitTimeout time.Duration,
	sshUser string,
	authFn func(context.Context) ([]ssh.AuthMethod, error),
	audit *termSSHSessionAudit,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			zlog.Warn().Err(err).Msg("/term: WebSocket upgrade failed")
			return
		}
		defer conn.Close()

		if err := WaitPort(reverseSSHAddr, reverseSSHWaitTimeout); err != nil {
			b, _ := json.Marshal(map[string]string{
				"type":    "error",
				"code":    "reverse_path_unavailable",
				"message": "SSH reverse path is not reachable on the proxy (tunnel closed or agent disconnected).",
			})
			_ = conn.WriteMessage(websocket.TextMessage, b)
			return
		}

		termName := r.URL.Query().Get("term")

		_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		var initRows, initCols int
		var deferred []wsMsg
		mt, payload, err := conn.ReadMessage()
		if err == nil && mt == websocket.TextMessage {
			var m wsMsg
			if json.Unmarshal(payload, &m) == nil {
				if m.Type == "resize" && m.Cols > 0 && m.Rows > 0 {
					initRows, initCols = m.Rows, m.Cols
				} else {
					deferred = append(deferred, m)
				}
			}
		}
		_ = conn.SetReadDeadline(time.Time{})

		sshClient, sess, stdin, stdout, err := DialSSHWithAuth(
			r.Context(),
			initRows,
			initCols,
			termName,
			reverseSSHAddr,
			sshUser,
			authFn,
		)
		if err != nil {
			zlog.Warn().Err(err).
				Str("reverse_ssh", reverseSSHAddr).
				Str("ssh_user", sshUser).
				Msg("/term: ssh dial failed (reverse path or Vault SSH auth)")
			msg := fmt.Sprintf("SSH connection over reverse path failed: %v", err)
			b, mErr := json.Marshal(map[string]string{
				"type":    "error",
				"code":    "ssh_dial_failed",
				"message": msg,
			})
			if mErr != nil {
				b = []byte(`{"type":"error","code":"ssh_dial_failed","message":"SSH connection failed"}`)
			}
			_ = conn.WriteMessage(websocket.TextMessage, b)
			return
		}
		defer func() { _ = sess.Close(); _ = sshClient.Close() }()

		established := zlog.Info().
			Str("reverse_ssh", reverseSSHAddr).
			Str("ssh_user", sshUser)
		if audit != nil {
			if audit.TenantID != "" {
				established = established.Str("tenant_id", audit.TenantID)
			}
			if audit.ResourceID != "" {
				established = established.Str("resource_id", audit.ResourceID)
			}
			if audit.Principal != "" {
				established = established.Str("ssh_principal", audit.Principal)
			}
		}
		established.Msg("/term: SSH session established (authenticated to edge via reverse path; interactive shell starting)")

		processMsg := func(m wsMsg) bool {
			switch m.Type {
			case "stdio":
				if _, err := io.WriteString(stdin, m.Data); err != nil {
					return false
				}
			case "resize":
				if m.Cols > 0 && m.Rows > 0 {
					_ = sess.WindowChange(m.Rows, m.Cols)
				}
			}
			return true
		}
		for _, m := range deferred {
			if !processMsg(m) {
				return
			}
		}

		conn.SetPongHandler(func(string) error { return nil })
		go func() {
			t := time.NewTicker(30 * time.Second)
			defer t.Stop()
			for range t.C {
				_ = conn.WriteControl(websocket.PingMessage, []byte("ping"), time.Now().Add(5*time.Second))
			}
		}()

		var writeMu sync.Mutex
		go func() {
			buf := make([]byte, 32*1024)
			for {
				n, err := stdout.Read(buf)
				if n > 0 {
					out := wsMsg{Type: "stdio", Data: string(buf[:n])}
					b, _ := json.Marshal(out)
					writeMu.Lock()
					_ = conn.WriteMessage(websocket.TextMessage, b)
					writeMu.Unlock()
				}
				if err != nil {
					writeMu.Lock()
					_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"stdio","data":"[RAP] stream closed\n"}`))
					writeMu.Unlock()
					return
				}
			}
		}()

		for {
			_, payload, err := conn.ReadMessage()
			if err != nil {
				zlog.Debug().Err(err).Msg("/term: WebSocket read ended")
				return
			}
			var m wsMsg
			if err := json.Unmarshal(payload, &m); err != nil {
				continue
			}
			if !processMsg(m) {
				return
			}
		}
	}
}
