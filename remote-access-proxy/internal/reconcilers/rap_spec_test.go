// SPDX-FileCopyrightText: (C) 2025 Intel Corporation
// SPDX-License-Identifier: Apache-2.0

package reconcilers

import (
	"strings"
	"testing"
	"time"

	computev1 "github.com/open-edge-platform/infra-core/inventory/v2/pkg/api/compute/v1"
	remoteaccessv1 "github.com/open-edge-platform/infra-core/inventory/v2/pkg/api/remoteaccess/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fixed time for deterministic expiration checks
var specEvalNow = time.Date(2026, 4, 11, 12, 0, 0, 0, time.UTC)

func baseRAC(tenantID, resID string) *remoteaccessv1.RemoteAccessConfiguration {
	return &remoteaccessv1.RemoteAccessConfiguration{
		ResourceId:          resID,
		TenantId:            tenantID,
		Instance:            &computev1.InstanceResource{ResourceId: "inst-1"},
		DesiredState:        remoteaccessv1.RemoteAccessState_REMOTE_ACCESS_STATE_ENABLED,
		CurrentState:        remoteaccessv1.RemoteAccessState_REMOTE_ACCESS_STATE_ENABLED,
		ExpirationTimestamp: uint64(specEvalNow.Add(time.Hour).Unix()),
		LocalPort:           30001,
		ProxyHost:           "proxy.example",
		User:                "root",
		SessionToken:        "u:p",
	}
}

func TestSpec_evaluateSpec(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		ra       *remoteaccessv1.RemoteAccessConfiguration
		want     SpecReadiness
		reasonFn func(string) bool // substring or empty reason checks
	}{
		{
			name: "nil",
			ra:   nil,
			want: SpecInvalid,
			reasonFn: func(s string) bool {
				return s == "configuration is nil"
			},
		},
		{
			name: "missing resource_id",
			ra: func() *remoteaccessv1.RemoteAccessConfiguration {
				r := baseRAC("t", "rid")
				r.ResourceId = " "
				return r
			}(),
			want: SpecInvalid,
			reasonFn: func(s string) bool {
				return strings.Contains(s, "missing resource_id")
			},
		},
		{
			name: "missing instance",
			ra: func() *remoteaccessv1.RemoteAccessConfiguration {
				r := baseRAC("t", "rid")
				r.Instance = nil
				return r
			}(),
			want: SpecInvalid,
			reasonFn: func(s string) bool {
				return strings.Contains(s, "missing instance reference")
			},
		},
		{
			name: "missing tenant_id",
			ra: func() *remoteaccessv1.RemoteAccessConfiguration {
				r := baseRAC("t", "rid")
				r.TenantId = ""
				return r
			}(),
			want: SpecInvalid,
			reasonFn: func(s string) bool {
				return strings.Contains(s, "missing tenant_id")
			},
		},
		{
			name: "desired UNSPECIFIED",
			ra: func() *remoteaccessv1.RemoteAccessConfiguration {
				r := baseRAC("t", "rid")
				r.DesiredState = remoteaccessv1.RemoteAccessState_REMOTE_ACCESS_STATE_UNSPECIFIED
				return r
			}(),
			want: SpecInvalid,
			reasonFn: func(s string) bool {
				return strings.Contains(s, "desired_state is UNSPECIFIED")
			},
		},
		{
			name: "ENABLED expiration in past",
			ra: func() *remoteaccessv1.RemoteAccessConfiguration {
				r := baseRAC("t", "rid")
				r.ExpirationTimestamp = uint64(specEvalNow.Add(-time.Hour).Unix())
				return r
			}(),
			want: SpecInvalid,
			reasonFn: func(s string) bool {
				return strings.Contains(s, "expiration_timestamp is in the past")
			},
		},
		{
			name: "DISABLED ignores expiration in past",
			ra: func() *remoteaccessv1.RemoteAccessConfiguration {
				r := baseRAC("t", "rid")
				r.DesiredState = remoteaccessv1.RemoteAccessState_REMOTE_ACCESS_STATE_DISABLED
				r.CurrentState = remoteaccessv1.RemoteAccessState_REMOTE_ACCESS_STATE_DISABLED
				r.ExpirationTimestamp = uint64(specEvalNow.Add(-time.Hour).Unix())
				return r
			}(),
			want:     SpecReady,
			reasonFn: func(s string) bool { return s == "" },
		},
		{
			name: "ENABLED expiration not set",
			ra: func() *remoteaccessv1.RemoteAccessConfiguration {
				r := baseRAC("t", "rid")
				r.ExpirationTimestamp = 0
				return r
			}(),
			want: SpecPending,
			reasonFn: func(s string) bool {
				return strings.Contains(s, "expiration_timestamp not set")
			},
		},
		{
			name: "pending local_port",
			ra: func() *remoteaccessv1.RemoteAccessConfiguration {
				r := baseRAC("t", "rid")
				r.LocalPort = 0
				return r
			}(),
			want: SpecPending,
			reasonFn: func(s string) bool {
				return strings.Contains(s, "local_port not allocated")
			},
		},
		{
			name: "pending proxy_host",
			ra: func() *remoteaccessv1.RemoteAccessConfiguration {
				r := baseRAC("t", "rid")
				r.ProxyHost = ""
				return r
			}(),
			want: SpecPending,
			reasonFn: func(s string) bool {
				return strings.Contains(s, "proxy_host not set")
			},
		},
		{
			name: "pending session_token",
			ra: func() *remoteaccessv1.RemoteAccessConfiguration {
				r := baseRAC("t", "rid")
				r.SessionToken = ""
				return r
			}(),
			want: SpecPending,
			reasonFn: func(s string) bool {
				return strings.Contains(s, "session_token not set")
			},
		},
		{
			name:     "ready full base",
			ra:       baseRAC("tenant-1", "rmtac-abc"),
			want:     SpecReady,
			reasonFn: func(s string) bool { return s == "" },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := evaluateSpec(tc.ra, specEvalNow)
			assert.Equal(t, tc.want, got.Readiness, "readiness: %s", got.Reason)
			require.True(t, tc.reasonFn(got.Reason), "reason=%q", got.Reason)
		})
	}
}

func TestSpec_buildRAPSpec_roundTrip(t *testing.T) {
	t.Parallel()
	ra := baseRAC("t-1", "rmtac-deadbeef")
	ra.ConfigurationStatus = "status text"
	got := buildRAPSpec(ra)
	require.NotNil(t, got)
	assert.Equal(t, ra.GetResourceId(), got.ResourceID)
	assert.Equal(t, ra.GetTenantId(), got.TenantID)
	assert.Equal(t, ra.GetProxyHost(), got.ProxyHost)
	assert.Equal(t, ra.GetLocalPort(), got.LocalPort)
	assert.Equal(t, ra.GetUser(), got.User)
	assert.Equal(t, ra.GetSessionToken(), got.SessionToken)
	assert.Equal(t, ra.GetDesiredState(), got.DesiredState)
	assert.Equal(t, ra.GetExpirationTimestamp(), got.ExpirationTs)
}

func TestSpec_rapDesiredStateNeedsRuntimeChisel(t *testing.T) {
	t.Parallel()
	assert.True(t, rapDesiredStateNeedsRuntimeChisel(remoteaccessv1.RemoteAccessState_REMOTE_ACCESS_STATE_ENABLED))
	assert.True(t, rapDesiredStateNeedsRuntimeChisel(remoteaccessv1.RemoteAccessState_REMOTE_ACCESS_STATE_CONFIGURED))
	assert.False(t, rapDesiredStateNeedsRuntimeChisel(remoteaccessv1.RemoteAccessState_REMOTE_ACCESS_STATE_DISABLED))
	assert.False(t, rapDesiredStateNeedsRuntimeChisel(remoteaccessv1.RemoteAccessState_REMOTE_ACCESS_STATE_ERROR))
}

func TestSpec_isExpiredInvalid(t *testing.T) {
	t.Parallel()
	assert.True(t, isExpiredInvalid(SpecStatus{
		Readiness: SpecInvalid,
		Reason:    "expiration_timestamp is in the past",
	}))
	assert.False(t, isExpiredInvalid(SpecStatus{
		Readiness: SpecInvalid,
		Reason:    "missing instance reference",
	}))
	assert.False(t, isExpiredInvalid(SpecStatus{Readiness: SpecReady, Reason: ""}))
}
