// SPDX-FileCopyrightText: (C) 2025 Intel Corporation
// SPDX-License-Identifier: Apache-2.0

package wsterm

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	remoteaccessv1 "github.com/open-edge-platform/infra-core/inventory/v2/pkg/api/remoteaccess/v1"
)

var fixedNow = time.Date(2026, 4, 8, 12, 0, 0, 0, time.UTC)

func testRACComplete() *remoteaccessv1.RemoteAccessConfiguration {
	return &remoteaccessv1.RemoteAccessConfiguration{
		ExpirationTimestamp: uint64(fixedNow.Add(time.Hour).Unix()),
		LocalPort:           21123,
		ProxyHost:           "wss://rap.example/chisel",
		User:                "edge-user",
		SessionToken:        "racuser:racpass",
		CurrentState:        remoteaccessv1.RemoteAccessState_REMOTE_ACCESS_STATE_ENABLED,
		DesiredState:        remoteaccessv1.RemoteAccessState_REMOTE_ACCESS_STATE_ENABLED,
	}
}

func TestRACBindingIncomplete(t *testing.T) {
	t.Parallel()
	complete := testRACComplete()
	if RACBindingIncomplete(complete) {
		t.Fatal("complete RAC should not be binding-incomplete")
	}
	cases := []struct {
		name string
		mut  func(*remoteaccessv1.RemoteAccessConfiguration)
	}{
		{"no_expiration", func(ra *remoteaccessv1.RemoteAccessConfiguration) { ra.ExpirationTimestamp = 0 }},
		{"no_proxy_host", func(ra *remoteaccessv1.RemoteAccessConfiguration) { ra.ProxyHost = "" }},
		{"no_user", func(ra *remoteaccessv1.RemoteAccessConfiguration) { ra.User = "" }},
		{"no_session_token", func(ra *remoteaccessv1.RemoteAccessConfiguration) { ra.SessionToken = "" }},
		{"proxy_whitespace", func(ra *remoteaccessv1.RemoteAccessConfiguration) { ra.ProxyHost = "   " }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ra := testRACComplete()
			tc.mut(ra)
			if !RACBindingIncomplete(ra) {
				t.Fatalf("expected incomplete for %s", tc.name)
			}
		})
	}
}

func TestTermGateDenied(t *testing.T) {
	t.Parallel()
	complete := testRACComplete()

	t.Run("nil", func(t *testing.T) {
		t.Parallel()
		st, code, msg := TermGateDenied(nil, fixedNow)
		if st != http.StatusNotFound || code != "rac_not_found" || msg == "" {
			t.Fatalf("got %d %q %q", st, code, msg)
		}
	})

	t.Run("ok", func(t *testing.T) {
		t.Parallel()
		st, code, msg := TermGateDenied(complete, fixedNow)
		if st != 0 || code != "" || msg != "" {
			t.Fatalf("expected allow, got %d %q %q", st, code, msg)
		}
	})

	t.Run("expired", func(t *testing.T) {
		t.Parallel()
		ra := testRACComplete()
		ra.ExpirationTimestamp = uint64(fixedNow.Unix() - 1)
		st, code, _ := TermGateDenied(ra, fixedNow)
		if st != http.StatusForbidden || code != "rac_expired" {
			t.Fatalf("got %d %q", st, code)
		}
	})

	t.Run("current_error_uses_configuration_status", func(t *testing.T) {
		t.Parallel()
		ra := testRACComplete()
		ra.CurrentState = remoteaccessv1.RemoteAccessState_REMOTE_ACCESS_STATE_ERROR
		ra.ConfigurationStatus = "session expired"
		st, code, msg := TermGateDenied(ra, fixedNow)
		if st != http.StatusForbidden || code != "rac_error" || msg != "session expired" {
			t.Fatalf("got %d %q %q", st, code, msg)
		}
	})

	t.Run("desired_disabled", func(t *testing.T) {
		t.Parallel()
		ra := testRACComplete()
		ra.DesiredState = remoteaccessv1.RemoteAccessState_REMOTE_ACCESS_STATE_DISABLED
		st, code, _ := TermGateDenied(ra, fixedNow)
		if st != http.StatusForbidden || code != "rac_disabled" {
			t.Fatalf("got %d %q", st, code)
		}
	})

	t.Run("rac_initializing_incomplete_binding", func(t *testing.T) {
		t.Parallel()
		ra := testRACComplete()
		ra.SessionToken = ""
		st, code, _ := TermGateDenied(ra, fixedNow)
		if st != http.StatusServiceUnavailable || code != "rac_initializing" {
			t.Fatalf("got %d %q", st, code)
		}
	})

	t.Run("tunnel_unavailable_complete_binding_zero_port", func(t *testing.T) {
		t.Parallel()
		ra := testRACComplete()
		ra.LocalPort = 0
		st, code, _ := TermGateDenied(ra, fixedNow)
		if st != http.StatusServiceUnavailable || code != "tunnel_unavailable" {
			t.Fatalf("got %d %q", st, code)
		}
	})
}

func TestWriteJSONError(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	WriteJSONError(rec, http.StatusForbidden, "rac_expired", "Remote access session has expired.")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status %d", rec.Code)
	}
	ct := rec.Header().Get("Content-Type")
	if ct != "application/json; charset=utf-8" {
		t.Fatalf("Content-Type %q", ct)
	}
	var body HTTPErrorBody
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Code != "rac_expired" || body.Message != "Remote access session has expired." {
		t.Fatalf("body %+v", body)
	}
}
