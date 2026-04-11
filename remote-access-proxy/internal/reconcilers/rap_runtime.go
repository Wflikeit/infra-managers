// SPDX-FileCopyrightText: (C) 2025 Intel Corporation
// SPDX-License-Identifier: Apache-2.0

package reconcilers

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

type runtimeSession struct {
	spec RAPSpec
}

// DefaultRAPRuntime records the last reconciled session spec and probes the reverse SSH listen
// address (same host/port shape as wsterm.RouteFromRA) to infer whether the edge agent has opened
// its Chisel reverse remote on local_port.
type DefaultRAPRuntime struct {
	mu          sync.Mutex
	sessions    map[string]runtimeSession
	probeHost   string
	dialTimeout time.Duration
}

// NewDefaultRAPRuntime returns a RAPRuntime implementation. probeHost is the host used for the
// reverse-path TCP check (e.g. 127.0.0.1); if empty, 127.0.0.1 is used.
func NewDefaultRAPRuntime(probeHost string) RAPRuntime {
	h := strings.TrimSpace(probeHost)
	if h == "" {
		h = "127.0.0.1"
	}
	return &DefaultRAPRuntime{
		sessions:    make(map[string]runtimeSession),
		probeHost:   h,
		dialTimeout: 80 * time.Millisecond,
	}
}

// NewInMemoryRAPRuntime is kept for tests and legacy call sites; prefer NewDefaultRAPRuntime.
func NewInMemoryRAPRuntime() RAPRuntime {
	return NewDefaultRAPRuntime("127.0.0.1")
}

func sessionKey(tenantID, resourceID string) string {
	return tenantID + "/" + resourceID
}

func (r *DefaultRAPRuntime) EnsureSession(
	ctx context.Context,
	tenantID string,
	resourceID string,
	spec *RAPSpec,
) (SessionConnectivity, error) {
	_ = ctx
	if spec == nil {
		return SessionConnectivity{}, fmt.Errorf("nil RAPSpec")
	}

	k := sessionKey(tenantID, resourceID)
	r.mu.Lock()
	r.sessions[k] = runtimeSession{spec: *spec}
	r.mu.Unlock()

	return SessionConnectivity{
		AgentReverseTunnelUp: reverseTunnelAcceptsTCP(r.probeHost, spec.LocalPort, r.dialTimeout),
	}, nil
}

func (r *DefaultRAPRuntime) DisableSession(
	ctx context.Context,
	tenantID string,
	resourceID string,
	reason string,
) error {
	_ = ctx
	_ = reason
	k := sessionKey(tenantID, resourceID)
	r.mu.Lock()
	delete(r.sessions, k)
	r.mu.Unlock()
	return nil
}

func reverseTunnelAcceptsTCP(host string, port uint32, dialTimeout time.Duration) bool {
	if port == 0 {
		return false
	}
	addr := net.JoinHostPort(host, strconv.FormatUint(uint64(port), 10))
	d := net.Dialer{Timeout: dialTimeout}
	conn, err := d.Dial("tcp", addr)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}
