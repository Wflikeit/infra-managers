// SPDX-FileCopyrightText: (C) 2025 Intel Corporation
// SPDX-License-Identifier: Apache-2.0

package reconcilers

import (
	"fmt"
	"testing"
)

func TestAllocateOrGetLocalPort_IsStablePerResource(t *testing.T) {
	r := &RAPReconciler{
		resourceToPort: make(map[string]uint32),
		usedPorts:      make(map[uint32]string),
	}

	p1, err := r.allocateOrGetLocalPort("t1", "r1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	p2, err := r.allocateOrGetLocalPort("t1", "r1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p1 != p2 {
		t.Fatalf("expected stable allocation, got %d then %d", p1, p2)
	}
}

func TestAllocateOrGetLocalPort_NoCollisionsWithinReplica(t *testing.T) {
	r := &RAPReconciler{
		resourceToPort: make(map[string]uint32),
		usedPorts:      make(map[uint32]string),
	}

	seen := map[uint32]struct{}{}
	for i := 0; i < 10; i++ {
		port, err := r.allocateOrGetLocalPort("t1", fmt.Sprintf("r%d", i))
		if err != nil {
			t.Fatalf("unexpected error for resource %d: %v", i, err)
		}
		if _, exists := seen[port]; exists {
			t.Fatalf("duplicate port allocation detected: %d", port)
		}
		seen[port] = struct{}{}
	}
}

func TestReleaseAllocatedPort_FreesPortForReuse(t *testing.T) {
	r := &RAPReconciler{
		resourceToPort: make(map[string]uint32),
		usedPorts:      make(map[uint32]string),
	}

	p1, err := r.allocateOrGetLocalPort("t1", "r1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_, err = r.allocateOrGetLocalPort("t1", "r2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	r.releaseAllocatedPort("t1", "r1")

	p3, err := r.allocateOrGetLocalPort("t1", "r3")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p3 != p1 {
		t.Fatalf("expected released port %d to be reused first, got %d", p1, p3)
	}
}
