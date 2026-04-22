// SPDX-FileCopyrightText: (C) 2025 Intel Corporation
// SPDX-License-Identifier: Apache-2.0

package wsterm

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRAPSSHPrincipal(t *testing.T) {
	t.Parallel()

	t.Run("ok", func(t *testing.T) {
		t.Parallel()
		p, err := RAPSSHPrincipal("550e8400-e29b-41d4-a716-446655440000")
		require.NoError(t, err)
		assert.Equal(t, "rap:550e8400-e29b-41d4-a716-446655440000", p)
	})

	t.Run("trims_space", func(t *testing.T) {
		t.Parallel()
		p, err := RAPSSHPrincipal("  550e8400-e29b-41d4-a716-446655440000  ")
		require.NoError(t, err)
		assert.Equal(t, "rap:550e8400-e29b-41d4-a716-446655440000", p)
	})

	t.Run("empty_tenant", func(t *testing.T) {
		t.Parallel()
		_, err := RAPSSHPrincipal("")
		require.Error(t, err)
	})
}
