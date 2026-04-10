// SPDX-FileCopyrightText: (C) 2025 Intel Corporation
// SPDX-License-Identifier: Apache-2.0

package reconcilers

import (
	"fmt"
	"sync"
)

const (
	localPortRangeStart uint32 = 21000
	localPortRangeEnd   uint32 = 21999
)

// localPortAllocator is an in-memory, replica-local view of which local_port each (tenant, resource)
// uses on this process. It does not replace inventory as source of truth: allocateOrGet picks a free
// port when none is known yet; reserveKnown registers a binding already known from outside (e.g. after
// load/reconcile) and refuses conflicts instead of overwriting another resource.
type localPortAllocator struct {
	mu             sync.Mutex
	resourceToPort map[string]uint32
	usedPorts      map[uint32]string
}

func newLocalPortAllocator() *localPortAllocator {
	return &localPortAllocator{
		resourceToPort: make(map[string]uint32),
		usedPorts:      make(map[uint32]string),
	}
}

func (a *localPortAllocator) allocateOrGet(tenantID, resourceID string) (uint32, error) {
	key := sessionKey(tenantID, resourceID)
	a.mu.Lock()
	defer a.mu.Unlock()

	if p, ok := a.resourceToPort[key]; ok {
		return p, nil
	}

	for p := localPortRangeStart; p <= localPortRangeEnd; p++ {
		if _, used := a.usedPorts[p]; used {
			continue
		}
		a.resourceToPort[key] = p
		a.usedPorts[p] = key
		return p, nil
	}

	return 0, fmt.Errorf("no free local_port in range %d-%d", localPortRangeStart, localPortRangeEnd)
}

func (a *localPortAllocator) reserveKnown(tenantID, resourceID string, port uint32) error {
	key := sessionKey(tenantID, resourceID)
	a.mu.Lock()
	defer a.mu.Unlock()

	if port < localPortRangeStart || port > localPortRangeEnd {
		return fmt.Errorf("local_port %d out of allocator range %d-%d", port, localPortRangeStart, localPortRangeEnd)
	}

	if current, ok := a.resourceToPort[key]; ok && current == port {
		return nil
	}

	// Check target port before mutating: on conflict we must leave maps unchanged (invariant:
	// resourceToPort[key]=p iff usedPorts[p]=key).
	if otherKey, ok := a.usedPorts[port]; ok && otherKey != key {
		return fmt.Errorf("local_port %d already reserved for %q", port, otherKey)
	}

	if current, ok := a.resourceToPort[key]; ok && current != port {
		delete(a.usedPorts, current)
	}

	a.resourceToPort[key] = port
	a.usedPorts[port] = key
	return nil
}

func (a *localPortAllocator) release(tenantID, resourceID string) {
	key := sessionKey(tenantID, resourceID)
	a.mu.Lock()
	defer a.mu.Unlock()

	if p, ok := a.resourceToPort[key]; ok {
		delete(a.resourceToPort, key)
		delete(a.usedPorts, p)
	}
}
