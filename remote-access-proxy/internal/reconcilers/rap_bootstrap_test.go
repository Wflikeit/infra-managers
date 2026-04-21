// SPDX-FileCopyrightText: (C) 2025 Intel Corporation
// SPDX-License-Identifier: Apache-2.0

package reconcilers

import (
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
