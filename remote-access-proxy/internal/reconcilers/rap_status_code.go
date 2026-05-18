// SPDX-FileCopyrightText: (C) 2025 Intel Corporation
// SPDX-License-Identifier: Apache-2.0

package reconcilers

import remoteaccessv1 "github.com/open-edge-platform/infra-core/inventory/v2/pkg/api/remoteaccess/v1"

// ConnectionInactiveCode is written by RAP after teardown; RAM hard-deletes when it sees this code.
// Keep in sync with remote-access/internal/reconcilers/expiry.go (rapReportedConnectionInactive).
const ConnectionInactiveCode = remoteaccessv1.RemoteAccessConfigurationStatus_REMOTE_ACCESS_CONFIGURATION_STATUS_CONNECTION_INACTIVE

func rapTunnelStatusCode(agentReverseTunnelUp bool) remoteaccessv1.RemoteAccessConfigurationStatus {
	if agentReverseTunnelUp {
		return remoteaccessv1.RemoteAccessConfigurationStatus_REMOTE_ACCESS_CONFIGURATION_STATUS_TUNNEL_ACTIVE
	}
	return remoteaccessv1.RemoteAccessConfigurationStatus_REMOTE_ACCESS_CONFIGURATION_STATUS_PROVISIONING
}
