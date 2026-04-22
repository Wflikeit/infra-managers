// SPDX-FileCopyrightText: (C) 2025 Intel Corporation
// SPDX-License-Identifier: Apache-2.0

package wsterm

import (
	"fmt"
	"strings"
)

// RAPSSHPrincipal returns the OpenSSH principal string for Vault user-certificate issuance
// and AuthorizedPrincipalsFile on the edge node. Format: rap:<tenant_id>.
//
// Only the tenant ID is used so the edge can be provisioned during onboarding, when a
// RemoteAccessConfiguration resource_id may not exist yet. The /term handler still requires
// tenant_id and resource_id in the query string for Inventory lookup (RAC → reverse SSH port);
// resource_id is not embedded in the principal.
func RAPSSHPrincipal(tenantID string) (string, error) {
	t := strings.TrimSpace(tenantID)
	if t == "" {
		return "", fmt.Errorf("tenant_id is required for SSH principal")
	}
	return fmt.Sprintf("rap:%s", t), nil
}
