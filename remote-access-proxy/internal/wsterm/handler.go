// SPDX-FileCopyrightText: (C) 2025 Intel Corporation
// SPDX-License-Identifier: Apache-2.0

package wsterm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	remoteaccessv1 "github.com/open-edge-platform/infra-core/inventory/v2/pkg/api/remoteaccess/v1"
	"github.com/open-edge-platform/infra-core/inventory/v2/pkg/logging"
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

// NewHandler returns a /term handler that dials ReverseSSHAddr without Inventory.
func NewHandler(cfg HandlerConfig) http.HandlerFunc {
	return newTermWS(cfg.ReverseSSHAddr, cfg.ReverseSSHWaitTimeout, cfg.SSHUser, cfg.PrivateKeyPath, cfg.Password)
}

// InventoryHandlerConfig adds Inventory-backed routing when tenant_id and resource_id are set.
type InventoryHandlerConfig struct {
	HandlerConfig
	NetClient        InventoryRACGetter
	InventoryTimeout time.Duration
	// Now supplies the instant for TermGateDenied expiry checks. If nil, time.Now().UTC() is used.
	Now func() time.Time
}

// NewInventoryHandler returns /term with optional RAC lookup and access checks.
func NewInventoryHandler(cfg InventoryHandlerConfig) http.HandlerFunc {
	now := cfg.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	base := newTermWS(cfg.ReverseSSHAddr, cfg.ReverseSSHWaitTimeout, cfg.SSHUser, cfg.PrivateKeyPath, cfg.Password)
	return func(w http.ResponseWriter, r *http.Request) {
		tenantID := strings.TrimSpace(r.URL.Query().Get("tenant_id"))
		resourceID := strings.TrimSpace(r.URL.Query().Get("resource_id"))
		if cfg.NetClient == nil || tenantID == "" || resourceID == "" {
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

		if httpSt, code, msg := TermGateDenied(ra, now()); httpSt != 0 {
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
		if route.ReverseSSHAddr == cfg.ReverseSSHAddr && route.SSHUser == cfg.SSHUser {
			base(w, r)
			return
		}
		newTermWS(route.ReverseSSHAddr, cfg.ReverseSSHWaitTimeout, route.SSHUser, cfg.PrivateKeyPath, cfg.Password)(w, r)
	}
}

func newTermWS(
	reverseSSHAddr string,
	reverseSSHWaitTimeout time.Duration,
	sshUser string,
	privateKeyPath string,
	password string,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Printf("WS upgrade: %v", err)
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

		sshClient, sess, stdin, stdout, err := DialSSH(
			initRows,
			initCols,
			termName,
			reverseSSHAddr,
			sshUser,
			privateKeyPath,
			password,
		)
		if err != nil {
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
				log.Printf("WS read: %v", err)
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
