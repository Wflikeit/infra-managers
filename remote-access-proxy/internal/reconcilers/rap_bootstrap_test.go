// SPDX-FileCopyrightText: (C) 2025 Intel Corporation
// SPDX-License-Identifier: Apache-2.0

package reconcilers

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	remoteaccessv1 "github.com/open-edge-platform/infra-core/inventory/v2/pkg/api/remoteaccess/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type bootstrapCaptureChisel struct {
	ensureCalls []struct{ user, pass string }
}

func (c *bootstrapCaptureChisel) EnsureUser(user, pass string) error {
	c.ensureCalls = append(c.ensureCalls, struct{ user, pass string }{user, pass})
	return nil
}

func (c *bootstrapCaptureChisel) RemoveUser(string) {}

func TestSpec_applyBootstrapDefaults_generates_port_token_and_defaults(t *testing.T) {
	t.Parallel()
	ch := &bootstrapCaptureChisel{}
	r := newReconcilerWithStub(&stubInventoryClient{}, NewInMemoryRAPRuntime(), ch)
	resID := "rmtac-bootstrap-1"
	spec := &RAPSpec{
		ResourceID:   resID,
		TenantID:     "tenant-1",
		LocalPort:    0,
		ProxyHost:    "",
		User:         "",
		SessionToken: "",
		DesiredState: remoteaccessv1.RemoteAccessState_REMOTE_ACCESS_STATE_ENABLED,
	}
	err := r.applyBootstrapDefaults("tenant-1", resID, spec)
	require.NoError(t, err)
	assert.NotZero(t, spec.LocalPort)
	assert.Equal(t, "remote-access-proxy-ws.kind.internal:443", spec.ProxyHost)
	assert.Equal(t, "root", spec.User)
	require.Contains(t, spec.SessionToken, ":")
	require.GreaterOrEqual(t, len(ch.ensureCalls), 1)
	last := ch.ensureCalls[len(ch.ensureCalls)-1]
	assert.Equal(t, strings.TrimSpace(strings.Split(spec.SessionToken, ":")[0]), last.user)
	assert.NotEmpty(t, last.pass)
}

func TestSpec_applyBootstrapDefaults_existing_session_token_calls_EnsureUser(t *testing.T) {
	t.Parallel()
	ch := &bootstrapCaptureChisel{}
	r := newReconcilerWithStub(&stubInventoryClient{}, NewInMemoryRAPRuntime(), ch)
	spec := &RAPSpec{
		ResourceID:   "rmtac-2",
		TenantID:     "t",
		LocalPort:    21050, // must be within localPortAllocator range (see rap_local_port_allocator.go)
		ProxyHost:    "p",
		User:         "root",
		SessionToken: "  alice:secret  ",
		DesiredState: remoteaccessv1.RemoteAccessState_REMOTE_ACCESS_STATE_ENABLED,
	}
	err := r.applyBootstrapDefaults("t", "rmtac-2", spec)
	require.NoError(t, err)
	require.Len(t, ch.ensureCalls, 1)
	assert.Equal(t, "alice", ch.ensureCalls[0].user)
	assert.Equal(t, "secret", ch.ensureCalls[0].pass)
	assert.Equal(t, "  alice:secret  ", spec.SessionToken, "spec token unchanged in place")
}

// TestSpec_applyBootstrapDefaults_self_heals_local_port_conflict verifies the reconcile-time
// self-heal path: if Inventory declares a local_port for this RAC that is already held on this
// replica by a different (tenant,resource), the reconciler must reallocate a fresh port, clear
// the stale session_token, and mint a new chisel credential. persistBinding (not covered here)
// then rewrites the binding in Inventory so the new port wins on the next reconcile.
func TestSpec_applyBootstrapDefaults_self_heals_local_port_conflict(t *testing.T) {
	t.Parallel()
	ch := &bootstrapCaptureChisel{}
	r := newReconcilerWithStub(&stubInventoryClient{}, NewInMemoryRAPRuntime(), ch)

	// Pre-seed the replica allocator with rac-winner holding the very port rac-loser will ask for.
	const contestedPort = localPortRangeStart + 7
	require.NoError(t, r.ports.reserveKnown("t", "rac-winner", contestedPort))

	spec := &RAPSpec{
		ResourceID:   "rac-loser",
		TenantID:     "t",
		LocalPort:    contestedPort,                 // declared by Inventory but already in use
		ProxyHost:    "",                            // exercised default too
		User:         "",                            // exercised default too
		SessionToken: "stale-user:stale-secret",     // must be replaced: tied to the old port
		DesiredState: remoteaccessv1.RemoteAccessState_REMOTE_ACCESS_STATE_ENABLED,
	}
	require.NoError(t, r.applyBootstrapDefaults("t", "rac-loser", spec))

	assert.NotEqual(t, contestedPort, spec.LocalPort,
		"self-heal must pick a different port; reusing the contested one would still conflict at runtime")
	assert.NotEmpty(t, spec.SessionToken, "self-heal must mint a fresh session token")
	assert.NotEqual(t, "stale-user:stale-secret", spec.SessionToken,
		"stale token (paired with contested port) must be discarded so chisel auth cannot be replayed against the winner")
	require.Len(t, ch.ensureCalls, 1,
		"self-heal must call EnsureUser exactly once for the freshly generated credential")
	assert.Contains(t, spec.SessionToken, ch.ensureCalls[0].user)

	// Winner is untouched.
	got, ok := r.ports.resourceToPort[sessionKey("t", "rac-winner")]
	require.True(t, ok, "winner binding must remain in the allocator after self-heal")
	assert.Equal(t, uint32(contestedPort), got)
}

// TestSpec_applyBootstrapDefaults_self_heal_bubbles_exhaustion verifies that when the allocator
// cannot provide a replacement port (pool fully used), the conflict-path self-heal surfaces the
// exhaustion error unchanged, instead of silently proceeding with a stale port.
func TestSpec_applyBootstrapDefaults_self_heal_bubbles_exhaustion(t *testing.T) {
	t.Parallel()
	ch := &bootstrapCaptureChisel{}
	r := newReconcilerWithStub(&stubInventoryClient{}, NewInMemoryRAPRuntime(), ch)

	// Exhaust the pool so allocateOrGet has nothing to hand out.
	poolSize := int(localPortRangeEnd - localPortRangeStart + 1)
	for i := 0; i < poolSize; i++ {
		_, err := r.ports.allocateOrGet("t", fmt.Sprintf("rac-filler-%d", i))
		require.NoError(t, err)
	}

	spec := &RAPSpec{
		ResourceID:   "rac-loser",
		TenantID:     "t",
		LocalPort:    localPortRangeStart, // held by rac-filler-0 after the loop above
		SessionToken: "stale:token",
		DesiredState: remoteaccessv1.RemoteAccessState_REMOTE_ACCESS_STATE_ENABLED,
	}
	err := r.applyBootstrapDefaults("t", "rac-loser", spec)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrLocalPortPoolExhausted,
		"when self-heal cannot allocate a replacement, the exhaustion error must propagate verbatim so the operator sees the real root cause")
	// No EnsureUser call: credentials are minted AFTER the port is resolved.
	assert.Empty(t, ch.ensureCalls)
	// Sanity: the ErrLocalPortConflict sentinel used internally is NOT what we surface up.
	assert.False(t, errors.Is(err, ErrLocalPortConflict),
		"surface error must be the terminal failure (exhaustion), not the precursor conflict")
}
