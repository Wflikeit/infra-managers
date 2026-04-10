// SPDX-FileCopyrightText: (C) 2025 Intel Corporation
// SPDX-License-Identifier: Apache-2.0

package reconcilers

import (
	"fmt"
	"strings"
	"testing"
)

func newPortAllocatorState() *localPortAllocator {
	return newLocalPortAllocator()
}

func TestAllocateOrGetLocalPort_IsStablePerResource(t *testing.T) {
	r := newPortAllocatorState()

	p1, err := r.allocateOrGet("t1", "r1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	p2, err := r.allocateOrGet("t1", "r1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p1 != p2 {
		t.Fatalf("expected stable allocation, got %d then %d", p1, p2)
	}
}

func TestAllocateOrGetLocalPort_SameResourceIDDifferentTenantsDoNotSharePort(t *testing.T) {
	r := newPortAllocatorState()

	pT1, err := r.allocateOrGet("t1", "r1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	pT2, err := r.allocateOrGet("t2", "r1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pT1 == pT2 {
		t.Fatalf("t1/r1 and t2/r1 must not share port %d; allocation key must include tenant", pT1)
	}

	pT1b, err := r.allocateOrGet("t1", "r1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pT1b != pT1 {
		t.Fatalf("expected stable port for t1/r1, got %d then %d", pT1, pT1b)
	}
	pT2b, err := r.allocateOrGet("t2", "r1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pT2b != pT2 {
		t.Fatalf("expected stable port for t2/r1, got %d then %d", pT2, pT2b)
	}
}

func TestAllocateOrGetLocalPort_NoCollisionsWithinReplica(t *testing.T) {
	r := newPortAllocatorState()

	seen := map[uint32]struct{}{}
	for i := 0; i < 10; i++ {
		port, err := r.allocateOrGet("t1", fmt.Sprintf("r%d", i))
		if err != nil {
			t.Fatalf("unexpected error for resource %d: %v", i, err)
		}
		if _, exists := seen[port]; exists {
			t.Fatalf("duplicate port allocation detected: %d", port)
		}
		seen[port] = struct{}{}
	}
}

func TestReleaseAllocatedPort_ClearsBindingAndDoesNotRequireReuseOrder(t *testing.T) {
	r := newPortAllocatorState()

	p1, err := r.allocateOrGet("t1", "r1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	p2, err := r.allocateOrGet("t1", "r2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p1 == p2 {
		t.Fatalf("expected distinct ports for r1 and r2, got %d", p1)
	}

	r.release("t1", "r1")

	// Another resource must still get a port distinct from r2's (r2 still bound).
	p3, err := r.allocateOrGet("t1", "r3")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p3 == p2 {
		t.Fatalf("r3 must not collide with r2's port (still bound), got %d", p3)
	}
	// Do not assert p3 == p1: allocator may pick any free port in range, not necessarily the one r1 released first.

	// Release cleared (t1,r1); a new allocation for that pair is stable.
	q1, err := r.allocateOrGet("t1", "r1")
	if err != nil {
		t.Fatalf("unexpected error after release: %v", err)
	}
	q2, err := r.allocateOrGet("t1", "r1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if q1 != q2 {
		t.Fatalf("expected stable allocation for re-bound t1/r1, got %d then %d", q1, q2)
	}
}

func TestReserveKnown_RejectOutOfRange(t *testing.T) {
	r := newPortAllocatorState()
	cases := []uint32{20999, 22000, 22, 65000}
	for _, p := range cases {
		err := r.reserveKnown("t1", "r1", p)
		if err == nil {
			t.Fatalf("expected error for port %d", p)
		}
		if !strings.Contains(err.Error(), "out of allocator range") {
			t.Fatalf("port %d: expected range error, got %v", p, err)
		}
	}
}

func TestReserveKnown_AcceptsRangeBoundaries(t *testing.T) {
	r := newPortAllocatorState()
	if err := r.reserveKnown("t1", "r1", localPortRangeStart); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if err := r.reserveKnown("t2", "r2", localPortRangeEnd); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
}

func TestReserveKnown_PortInUseByAnotherResourceReturnsError(t *testing.T) {
	r := newPortAllocatorState()

	_, err := r.allocateOrGet("t1", "r1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	pT2, err := r.allocateOrGet("t2", "r2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	err = r.reserveKnown("t3", "r3", pT2)
	if err == nil {
		t.Fatal("expected error when reserving a port still owned by another resource")
	}
	if !strings.Contains(err.Error(), "already reserved") {
		t.Fatalf("expected conflict error, got %v", err)
	}

	// Prior owner unchanged; conflicting reservation did not apply.
	if got := r.resourceToPort[sessionKey("t2", "r2")]; got != pT2 {
		t.Fatalf("t2/r2 port: got %d want %d", got, pT2)
	}
	if _, ok := r.resourceToPort[sessionKey("t3", "r3")]; ok {
		t.Fatal("t3/r3 must not be bound after failed reserveKnown")
	}
	if owner := r.usedPorts[pT2]; owner != sessionKey("t2", "r2") {
		t.Fatalf("usedPorts[%d]: got %q want t2/r2", pT2, owner)
	}
}

func TestReserveKnown_ConflictWhenRebindingLeavesMapsConsistent(t *testing.T) {
	r := newPortAllocatorState()

	p1, err := r.allocateOrGet("t1", "r1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	p2, err := r.allocateOrGet("t2", "r2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p1 == p2 {
		t.Fatalf("expected distinct ports, got %d", p1)
	}

	err = r.reserveKnown("t1", "r1", p2)
	if err == nil {
		t.Fatal("expected error when rebinding to another resource's port")
	}
	if !strings.Contains(err.Error(), "already reserved") {
		t.Fatalf("expected conflict error, got %v", err)
	}

	// Failed reserve must not drop usedPorts for t1/r1's current port (regression: conflict after partial delete).
	if r.resourceToPort[sessionKey("t1", "r1")] != p1 {
		t.Fatalf("t1/r1 resourceToPort: got %v want %d", r.resourceToPort[sessionKey("t1", "r1")], p1)
	}
	if r.usedPorts[p1] != sessionKey("t1", "r1") {
		t.Fatalf("usedPorts[%d]: got %q want t1/r1", p1, r.usedPorts[p1])
	}
	if r.usedPorts[p2] != sessionKey("t2", "r2") {
		t.Fatalf("usedPorts[%d]: got %q want t2/r2", p2, r.usedPorts[p2])
	}
}

func TestAllocateOrGet_ExhaustedPoolReturnsError(t *testing.T) {
	r := newPortAllocatorState()
	n := int(localPortRangeEnd - localPortRangeStart + 1)
	for i := 0; i < n; i++ {
		_, err := r.allocateOrGet("t1", fmt.Sprintf("r%d", i))
		if err != nil {
			t.Fatalf("allocation %d/%d: unexpected error: %v", i+1, n, err)
		}
	}

	_, err := r.allocateOrGet("t1", "one-too-many")
	if err == nil {
		t.Fatal("expected error when pool is exhausted")
	}
	wantSub := fmt.Sprintf("no free local_port in range %d-%d", localPortRangeStart, localPortRangeEnd)
	if !strings.Contains(err.Error(), wantSub) {
		t.Fatalf("expected error containing %q, got %v", wantSub, err)
	}
}
