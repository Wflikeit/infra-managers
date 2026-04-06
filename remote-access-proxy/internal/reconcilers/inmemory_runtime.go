// SPDX-FileCopyrightText: (C) 2025 Intel Corporation
// SPDX-License-Identifier: Apache-2.0

package reconcilers

import (
	"context"
	"fmt"
	"sync"
)

type runtimeSession struct {
	spec      RAPSpec
	connected bool
}

// InMemoryRAPRuntime is a minimal, non-placeholder runtime that stores desired session state.
// It does not fabricate missing RAC fields; connected stays false until a real transport layer reports it.
type InMemoryRAPRuntime struct {
	mu       sync.Mutex
	sessions map[string]runtimeSession
}

func NewInMemoryRAPRuntime() RAPRuntime {
	return &InMemoryRAPRuntime{
		sessions: make(map[string]runtimeSession),
	}
}

func sessionKey(tenantID, resourceID string) string {
	return tenantID + "/" + resourceID
}

func (r *InMemoryRAPRuntime) EnsureSession(
	ctx context.Context,
	tenantID string,
	resourceID string,
	spec *RAPSpec,
) (bool, error) {
	_ = ctx
	if spec == nil {
		return false, fmt.Errorf("nil RAPSpec")
	}

	k := sessionKey(tenantID, resourceID)
	r.mu.Lock()
	prev, ok := r.sessions[k]
	r.sessions[k] = runtimeSession{
		spec:      *spec,
		connected: ok && prev.connected,
	}
	connected := r.sessions[k].connected
	r.mu.Unlock()

	return connected, nil
}

func (r *InMemoryRAPRuntime) DisableSession(
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

