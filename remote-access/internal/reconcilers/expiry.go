// SPDX-FileCopyrightText: (C) 2025 Intel Corporation
// SPDX-License-Identifier: Apache-2.0

package reconcilers

import (
	"strings"

	remoteaccessv1 "github.com/open-edge-platform/infra-core/inventory/v2/pkg/api/remoteaccess/v1"
)

const (
	// ExpirationPastReason is returned by evaluateSpec when expiration_timestamp <= now.
	ExpirationPastReason = "expiration_timestamp is in the past"
	// ConnectionInactiveStatus is written by RAP after replica teardown; RAM waits for this before hard delete.
	// Keep in sync with remote-access-proxy/internal/reconcilers/rap_expiry.go.
	ConnectionInactiveStatus = "remote access connection inactive"
)

func specIndicatesExpirationPast(reason string) bool {
	return strings.Contains(reason, ExpirationPastReason)
}

func rapReportedConnectionInactive(ra *remoteaccessv1.RemoteAccessConfiguration) bool {
	if ra == nil {
		return false
	}
	return ra.GetConfigurationStatusCode() ==
		remoteaccessv1.RemoteAccessConfigurationStatus_REMOTE_ACCESS_CONFIGURATION_STATUS_CONNECTION_INACTIVE
}
