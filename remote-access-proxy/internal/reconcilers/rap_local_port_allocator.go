// SPDX-FileCopyrightText: (C) 2025 Intel Corporation
// SPDX-License-Identifier: Apache-2.0

package reconcilers

import (
	"container/heap"
	"errors"
	"fmt"
	"sync"
)

const (
	localPortRangeStart uint32 = 21000
	localPortRangeEnd   uint32 = 21999
)

// Sentinel errors for allocator callers that want to react to specific failure modes
// (e.g. self-heal on conflict without masking genuine misconfiguration).
var (
	// ErrLocalPortConflict is returned by reserveKnown when the requested port is already held
	// by a different (tenant, resource). Callers may fall back to allocateOrGet to self-heal.
	ErrLocalPortConflict = errors.New("local_port already held by another (tenant, resource)")
	// ErrLocalPortOutOfRange is returned by reserveKnown when the port is outside the configured
	// allocator range — this indicates external data corruption and is NOT self-healable.
	ErrLocalPortOutOfRange = errors.New("local_port out of allocator range")
	// ErrLocalPortPoolExhausted is returned by allocateOrGet when no free port remains in the
	// configured range.
	ErrLocalPortPoolExhausted = errors.New("local_port pool exhausted")
)

// localPortAllocator is an in-memory, replica-local view of which local_port each (tenant, resource)
// uses on this process. It does not replace inventory as source of truth: allocateOrGet picks a free
// port when none is known yet; reserveKnown registers a binding already known from outside (e.g. after
// load/reconcile) and refuses conflicts instead of overwriting another resource.
//
// The pool is a min-heap of free ports in [localPortRangeStart, localPortRangeEnd]. Pop always returns
// the smallest free port, which is deterministic (helps tests and log reading) and avoids fragmentation.
// Lazy removal is used: reserveKnown marks a port as used without touching the heap; allocateOrGet
// skips heap entries that are already marked used. This keeps reserve/release O(log n) without
// maintaining a reverse index into the heap.
type localPortAllocator struct {
	mu             sync.Mutex
	resourceToPort map[string]uint32
	usedPorts      map[uint32]string
	freePorts      *portMinHeap
}

// portMinHeap implements heap.Interface for a min-heap of port numbers.
type portMinHeap []uint32

func (h portMinHeap) Len() int            { return len(h) }
func (h portMinHeap) Less(i, j int) bool  { return h[i] < h[j] }
func (h portMinHeap) Swap(i, j int)       { h[i], h[j] = h[j], h[i] }
func (h *portMinHeap) Push(x interface{}) { *h = append(*h, x.(uint32)) }
func (h *portMinHeap) Pop() interface{} {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[:n-1]
	return x
}

func newLocalPortAllocator() *localPortAllocator {
	h := &portMinHeap{}
	heap.Init(h)
	for p := localPortRangeStart; p <= localPortRangeEnd; p++ {
		heap.Push(h, p)
	}
	return &localPortAllocator{
		resourceToPort: make(map[string]uint32),
		usedPorts:      make(map[uint32]string),
		freePorts:      h,
	}
}

// stats returns the current number of used and free ports. Callers own the lock (if needed); this
// helper is meant for diagnostics inside this package, where the caller has already taken a.mu.
// It does not re-acquire a.mu to avoid double locking.
func (a *localPortAllocator) statsLocked() (used int, free int) {
	return len(a.usedPorts), int(localPortRangeEnd-localPortRangeStart+1) - len(a.usedPorts)
}

// Stats returns a snapshot of allocator pressure (takes the lock).
func (a *localPortAllocator) Stats() (used int, free int, total int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	u, f := a.statsLocked()
	return u, f, int(localPortRangeEnd - localPortRangeStart + 1)
}

func (a *localPortAllocator) allocateOrGet(tenantID, resourceID string) (uint32, error) {
	key := sessionKey(tenantID, resourceID)
	a.mu.Lock()
	defer a.mu.Unlock()

	if p, ok := a.resourceToPort[key]; ok {
		used, free := a.statsLocked()
		zlog.Debug().
			Str("tenant_id", tenantID).
			Str("resource_id", resourceID).
			Uint32("port", p).
			Int("pool_used", used).
			Int("pool_free", free).
			Msg("localPortAllocator: cache hit for (tenant,resource); returning previously assigned port")
		return p, nil
	}

	// Lazy removal: skip heap entries that were reserved by reserveKnown without heap touch.
	for a.freePorts.Len() > 0 {
		p := heap.Pop(a.freePorts).(uint32)
		if _, used := a.usedPorts[p]; used {
			continue // stale entry; marker
		}
		a.resourceToPort[key] = p
		a.usedPorts[p] = key
		u, f := a.statsLocked()
		zlog.Info().
			Str("tenant_id", tenantID).
			Str("resource_id", resourceID).
			Uint32("port", p).
			Int("pool_used", u).
			Int("pool_free", f).
			Msg("localPortAllocator: allocated new port from free pool")
		return p, nil
	}

	used, free := a.statsLocked()
	zlog.Warn().
		Str("tenant_id", tenantID).
		Str("resource_id", resourceID).
		Int("pool_used", used).
		Int("pool_free", free).
		Uint32("range_start", localPortRangeStart).
		Uint32("range_end", localPortRangeEnd).
		Msg("localPortAllocator: exhausted — no free local_port in configured range")
	return 0, fmt.Errorf("%w: range %d-%d", ErrLocalPortPoolExhausted, localPortRangeStart, localPortRangeEnd)
}

// reserveKnown registers a binding already known from outside (e.g. hydrated from Inventory).
// It does not pop the port from the free-pool heap; allocateOrGet performs lazy filtering.
// On conflict (target port already used by a different key) the maps are left unchanged.
func (a *localPortAllocator) reserveKnown(tenantID, resourceID string, port uint32) error {
	key := sessionKey(tenantID, resourceID)
	a.mu.Lock()
	defer a.mu.Unlock()

	if port < localPortRangeStart || port > localPortRangeEnd {
		zlog.Warn().
			Str("tenant_id", tenantID).
			Str("resource_id", resourceID).
			Uint32("port", port).
			Uint32("range_start", localPortRangeStart).
			Uint32("range_end", localPortRangeEnd).
			Msg("localPortAllocator: reserveKnown rejected; port out of configured range")
		return fmt.Errorf("%w: local_port %d (range %d-%d)", ErrLocalPortOutOfRange, port, localPortRangeStart, localPortRangeEnd)
	}

	if current, ok := a.resourceToPort[key]; ok && current == port {
		return nil
	}

	if otherKey, ok := a.usedPorts[port]; ok && otherKey != key {
		used, free := a.statsLocked()
		zlog.Warn().
			Str("tenant_id", tenantID).
			Str("resource_id", resourceID).
			Str("holding_key", otherKey).
			Uint32("port", port).
			Int("pool_used", used).
			Int("pool_free", free).
			Msg("localPortAllocator: reserveKnown conflict; port already held by another (tenant,resource)")
		return fmt.Errorf("%w: local_port %d held by %q", ErrLocalPortConflict, port, otherKey)
	}

	// If this key previously held a different port, release it back to the heap so the freed
	// port becomes allocatable again.
	if current, ok := a.resourceToPort[key]; ok && current != port {
		delete(a.usedPorts, current)
		heap.Push(a.freePorts, current)
		zlog.Debug().
			Str("tenant_id", tenantID).
			Str("resource_id", resourceID).
			Uint32("released_port", current).
			Uint32("new_port", port).
			Msg("localPortAllocator: reserveKnown rebinds key to a new port; released previous port")
	}

	a.resourceToPort[key] = port
	a.usedPorts[port] = key
	used, free := a.statsLocked()
	zlog.Info().
		Str("tenant_id", tenantID).
		Str("resource_id", resourceID).
		Uint32("port", port).
		Int("pool_used", used).
		Int("pool_free", free).
		Msg("localPortAllocator: reserved known port (from inventory or rehydration)")
	return nil
}

// release returns the port held by (tenant,resource) — if any — back to the free pool.
// Safe to call when the key is unknown (no-op).
func (a *localPortAllocator) release(tenantID, resourceID string) {
	key := sessionKey(tenantID, resourceID)
	a.mu.Lock()
	defer a.mu.Unlock()

	p, ok := a.resourceToPort[key]
	if !ok {
		return
	}
	delete(a.resourceToPort, key)
	delete(a.usedPorts, p)
	heap.Push(a.freePorts, p)
	used, free := a.statsLocked()
	zlog.Info().
		Str("tenant_id", tenantID).
		Str("resource_id", resourceID).
		Uint32("port", p).
		Int("pool_used", used).
		Int("pool_free", free).
		Msg("localPortAllocator: released port back to free pool")
}
