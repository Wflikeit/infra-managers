// SPDX-FileCopyrightText: (C) 2025 Intel Corporation
// SPDX-License-Identifier: Apache-2.0

package vaultssh

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewSigner_validation(t *testing.T) {
	t.Parallel()

	_, err := NewSigner(SignerConfig{})
	require.Error(t, err)

	_, err = NewSigner(SignerConfig{
		VaultAddress: "http://127.0.0.1:8200",
		Mount:              "ssh",
		SignRole:           "rap",
		KubernetesAuthRole: "",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "kubernetes auth role")

	_, err = NewSigner(SignerConfig{
		VaultAddress:       "http://127.0.0.1:8200",
		Mount:              "",
		SignRole:           "rap",
		KubernetesAuthRole: "rap",
	})
	require.Error(t, err)

	s, err := NewSigner(SignerConfig{
		VaultAddress:       "http://127.0.0.1:8200",
		Mount:              "ssh-client-signer",
		SignRole:           "rap-term",
		KubernetesAuthRole: "rap",
	})
	require.NoError(t, err)
	require.NotNil(t, s)
}
