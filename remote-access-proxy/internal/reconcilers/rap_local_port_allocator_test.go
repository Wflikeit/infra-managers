// SPDX-FileCopyrightText: (C) 2025 Intel Corporation
// SPDX-License-Identifier: Apache-2.0

package reconcilers

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSpec_localPortAllocator_allocateOrGet_returnsSmallestFirstAndIsStableForSameKey(t *testing.T) {
	a := newLocalPortAllocator()
	p1, err := a.allocateOrGet("t", "rac-a")
	require.NoError(t, err)
	require.Equal(t, localPortRangeStart, p1)

	p2, err := a.allocateOrGet("t", "rac-b")
	require.NoError(t, err)
	require.Equal(t, localPortRangeStart+1, p2)

	// Same (tenant,resource) must return cached port.
	p1b, err := a.allocateOrGet("t", "rac-a")
	require.NoError(t, err)
	require.Equal(t, p1, p1b)
}

func TestSpec_localPortAllocator_reserveKnown_marksUsedWithoutHeapPop(t *testing.T) {
	a := newLocalPortAllocator()
	// Reserve a port in the middle of the range; subsequent allocations should skip it via lazy
	// removal from the heap top.
	require.NoError(t, a.reserveKnown("t", "rac-mid", localPortRangeStart+5))

	p, err := a.allocateOrGet("t", "rac-a")
	require.NoError(t, err)
	require.Equal(t, localPortRangeStart, p)

	// Reserve the very next port so the reserved mid-port is indeed skipped.
	p2, err := a.allocateOrGet("t", "rac-b")
	require.NoError(t, err)
	require.Equal(t, localPortRangeStart+1, p2)
}

func TestSpec_localPortAllocator_reserveKnown_conflict(t *testing.T) {
	a := newLocalPortAllocator()
	require.NoError(t, a.reserveKnown("t", "rac-a", localPortRangeStart+10))
	err := a.reserveKnown("t", "rac-b", localPortRangeStart+10)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrLocalPortConflict,
		"reserveKnown on an already-held port must expose ErrLocalPortConflict so callers can self-heal")
	// The reservation for rac-a must remain intact on conflict.
	got, ok := a.resourceToPort[sessionKey("t", "rac-a")]
	require.True(t, ok)
	require.Equal(t, localPortRangeStart+10, got)
	_, ok = a.resourceToPort[sessionKey("t", "rac-b")]
	require.False(t, ok)
}

func TestSpec_localPortAllocator_reserveKnown_rebindReleasesOldPort(t *testing.T) {
	a := newLocalPortAllocator()
	require.NoError(t, a.reserveKnown("t", "rac-a", localPortRangeStart+10))
	require.NoError(t, a.reserveKnown("t", "rac-a", localPortRangeStart+20))

	// Old port (10) must be free again — a fresh allocateOrGet under a different key must be able
	// to get it when the range top is popped past it.
	// We directly check used maps here since heap order is deterministic but iteration is not
	// required.
	_, stillHeld := a.usedPorts[localPortRangeStart+10]
	require.False(t, stillHeld, "old port must be released after rebind")
	require.Equal(t, localPortRangeStart+20, a.resourceToPort[sessionKey("t", "rac-a")])
}

func TestSpec_localPortAllocator_release_makesPortReusable(t *testing.T) {
	a := newLocalPortAllocator()
	p, err := a.allocateOrGet("t", "rac-a")
	require.NoError(t, err)
	a.release("t", "rac-a")

	// After release, the same key starts fresh and gets the next port in min-heap order (which,
	// for an otherwise untouched pool, is the just-released port — smallest in pool).
	p2, err := a.allocateOrGet("t", "rac-a")
	require.NoError(t, err)
	require.Equal(t, p, p2, "released port should be reused by min-heap order")
}

func TestSpec_localPortAllocator_outOfRange(t *testing.T) {
	a := newLocalPortAllocator()
	errLow := a.reserveKnown("t", "rac-a", localPortRangeStart-1)
	require.Error(t, errLow)
	require.ErrorIs(t, errLow, ErrLocalPortOutOfRange)
	// Out-of-range must NOT masquerade as a conflict — callers use this distinction to decide
	// whether a self-heal reallocation is safe (conflict only).
	require.False(t, errors.Is(errLow, ErrLocalPortConflict),
		"out-of-range port must not be classified as conflict")

	errHigh := a.reserveKnown("t", "rac-a", localPortRangeEnd+1)
	require.Error(t, errHigh)
	require.ErrorIs(t, errHigh, ErrLocalPortOutOfRange)
}

func TestSpec_localPortAllocator_exhaustion(t *testing.T) {
	a := newLocalPortAllocator()
	poolSize := int(localPortRangeEnd - localPortRangeStart + 1)
	for i := 0; i < poolSize; i++ {
		_, err := a.allocateOrGet("t", fmt.Sprintf("rac-%d", i))
		require.NoError(t, err)
	}
	_, err := a.allocateOrGet("t", "rac-overflow")
	require.Error(t, err)
	require.ErrorIs(t, err, ErrLocalPortPoolExhausted)
}

func TestSpec_localPortAllocator_Stats(t *testing.T) {
	a := newLocalPortAllocator()
	u, f, total := a.Stats()
	require.Equal(t, 0, u)
	require.Equal(t, int(localPortRangeEnd-localPortRangeStart+1), f)
	require.Equal(t, int(localPortRangeEnd-localPortRangeStart+1), total)

	_, err := a.allocateOrGet("t", "rac-a")
	require.NoError(t, err)
	u, f, _ = a.Stats()
	require.Equal(t, 1, u)
	require.Equal(t, int(localPortRangeEnd-localPortRangeStart), f)
}
