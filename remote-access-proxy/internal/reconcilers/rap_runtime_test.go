// SPDX-FileCopyrightText: (C) 2025 Intel Corporation
// SPDX-License-Identifier: Apache-2.0

package reconcilers

import (
	"context"
	"testing"
	"time"

	remoteaccessv1 "github.com/open-edge-platform/infra-core/inventory/v2/pkg/api/remoteaccess/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSpec_DefaultRAPRuntime_EnsureSession_nil_spec(t *testing.T) {
	t.Parallel()
	rt := NewDefaultRAPRuntime("127.0.0.1")
	_, err := rt.EnsureSession(context.Background(), "t", "r", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nil RAPSpec")
}

func TestSpec_DefaultRAPRuntime_probe_host_default(t *testing.T) {
	t.Parallel()
	rt := NewDefaultRAPRuntime("   ")
	dr, ok := rt.(*DefaultRAPRuntime)
	require.True(t, ok, "NewDefaultRAPRuntime should return *DefaultRAPRuntime")
	assert.Equal(t, "127.0.0.1", dr.probeHost)
}

func TestSpec_reverseTunnelAcceptsTCP_port_zero(t *testing.T) {
	t.Parallel()
	assert.False(t, reverseTunnelAcceptsTCP("127.0.0.1", 0, 10*time.Millisecond))
}

func TestSpec_DefaultRAPRuntime_DisableSession_idempotent(t *testing.T) {
	t.Parallel()
	rt := NewInMemoryRAPRuntime()
	ctx := context.Background()
	spec := &RAPSpec{
		LocalPort:    9,
		ProxyHost:    "p",
		User:         "u",
		SessionToken: "a:b",
		DesiredState: remoteaccessv1.RemoteAccessState_REMOTE_ACCESS_STATE_ENABLED,
	}
	_, err := rt.EnsureSession(ctx, "ten", "res", spec)
	require.NoError(t, err)
	require.NoError(t, rt.DisableSession(ctx, "ten", "res", "test"))
	require.NoError(t, rt.DisableSession(ctx, "ten", "res", "again"))
	_, err = rt.EnsureSession(ctx, "ten", "res", spec)
	require.NoError(t, err)
}
