// SPDX-FileCopyrightText: (C) 2025 Intel Corporation
// SPDX-License-Identifier: Apache-2.0

package wsterm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	remoteaccessv1 "github.com/open-edge-platform/infra-core/inventory/v2/pkg/api/remoteaccess/v1"
	"github.com/open-edge-platform/infra-managers/remote-access-proxy/internal/wsterm/mocks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// now, if non-nil, is used for TermGateDenied (RAC expiry). When nil, the handler uses the real clock.
func termInventoryHandler(t *testing.T, getter InventoryRACGetter, now func() time.Time) http.HandlerFunc {
	t.Helper()
	cfg := InventoryHandlerConfig{
		HandlerConfig: HandlerConfig{
			ReverseSSHAddr:        "127.0.0.1:1",
			ReverseSSHWaitTimeout: time.Millisecond,
			SSHUser:               "vendev",
			PrivateKeyPath:        "",
			Password:              "",
		},
		NetClient:        getter,
		InventoryTimeout: time.Second,
		Now:              now,
	}
	return NewInventoryHandler(cfg)
}

func TestSpec_NewInventoryHandler_HTTP_before_websocket(t *testing.T) {
	tenant := "11111111-1111-1111-1111-111111111111"
	racID := "rmtacconf-deadbeef"
	q := url.Values{}
	q.Set("tenant_id", tenant)
	q.Set("resource_id", racID)
	path := "/term?" + q.Encode()

	t.Run("when_Get_returns_NotFound_then_404_rac_not_found_JSON", func(t *testing.T) {
		m := mocks.NewInventoryRACGetter(t)
		m.EXPECT().GetRemoteAccessConf(mock.Anything, tenant, racID, mock.AnythingOfType("time.Duration")).
			Return(nil, status.Error(codes.NotFound, "no such RAC"))
		h := termInventoryHandler(t, m, nil)
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		h(rec, req)

		require.Equal(t, http.StatusNotFound, rec.Code)
		var body HTTPErrorBody
		require.NoError(t, json.NewDecoder(rec.Body).Decode(&body))
		assert.Equal(t, "rac_not_found", body.Code)
		assert.NotEmpty(t, body.Message)
	})

	t.Run("when_Get_returns_non_NotFound_error_then_502_inventory_unavailable_JSON", func(t *testing.T) {
		m := mocks.NewInventoryRACGetter(t)
		m.EXPECT().GetRemoteAccessConf(mock.Anything, tenant, racID, mock.AnythingOfType("time.Duration")).
			Return(nil, errors.New("inventory backend hiccup"))
		h := termInventoryHandler(t, m, nil)
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		h(rec, req)

		require.Equal(t, http.StatusBadGateway, rec.Code)
		var body HTTPErrorBody
		require.NoError(t, json.NewDecoder(rec.Body).Decode(&body))
		assert.Equal(t, "inventory_unavailable", body.Code)
		assert.NotEmpty(t, body.Message)
	})

	t.Run("when_RAC_is_expired_then_403_rac_expired_JSON", func(t *testing.T) {
		ra := testRACComplete()
		ra.ExpirationTimestamp = uint64(fixedNow.Unix() - 1)
		m := mocks.NewInventoryRACGetter(t)
		m.EXPECT().GetRemoteAccessConf(mock.Anything, tenant, racID, mock.AnythingOfType("time.Duration")).
			Return(ra, nil)
		h := termInventoryHandler(t, m, func() time.Time { return fixedNow })
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		h(rec, req)

		require.Equal(t, http.StatusForbidden, rec.Code)
		var body HTTPErrorBody
		require.NoError(t, json.NewDecoder(rec.Body).Decode(&body))
		assert.Equal(t, "rac_expired", body.Code)
	})

	t.Run("when_RAC_desired_disabled_then_403_rac_disabled_JSON", func(t *testing.T) {
		ra := testRACComplete()
		ra.DesiredState = remoteaccessv1.RemoteAccessState_REMOTE_ACCESS_STATE_DISABLED
		m := mocks.NewInventoryRACGetter(t)
		m.EXPECT().GetRemoteAccessConf(mock.Anything, tenant, racID, mock.AnythingOfType("time.Duration")).
			Return(ra, nil)
		h := termInventoryHandler(t, m, func() time.Time { return fixedNow })
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		h(rec, req)

		require.Equal(t, http.StatusForbidden, rec.Code)
		var body HTTPErrorBody
		require.NoError(t, json.NewDecoder(rec.Body).Decode(&body))
		assert.Equal(t, "rac_disabled", body.Code)
	})
}

func TestSpec_NewInventoryHandler_VaultSessionAuth_requires_query_params(t *testing.T) {
	h := NewInventoryHandler(InventoryHandlerConfig{
		HandlerConfig: HandlerConfig{
			ReverseSSHAddr:        "127.0.0.1:1",
			ReverseSSHWaitTimeout: time.Millisecond,
			SSHUser:               "vendev",
			PrivateKeyPath:        "",
			Password:              "",
		},
		NetClient: mocks.NewInventoryRACGetter(t),
		SessionAuth: func(context.Context, *remoteaccessv1.RemoteAccessConfiguration, string, string) ([]ssh.AuthMethod, error) {
			return nil, errors.New("should not be called")
		},
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/term", nil)
	h(rec, req)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	var body HTTPErrorBody
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&body))
	assert.Equal(t, "term_params_required", body.Code)
}
