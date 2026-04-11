// SPDX-FileCopyrightText: (C) 2025 Intel Corporation
// SPDX-License-Identifier: Apache-2.0

package reconcilers

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type captureChisel struct {
	lastUser, lastPass string
	removed            []string
}

func (c *captureChisel) EnsureUser(user, pass string) error {
	c.lastUser, c.lastPass = user, pass
	return nil
}

func (c *captureChisel) RemoveUser(user string) {
	c.removed = append(c.removed, user)
}

func TestSpec_syncChiselFromToken_trims_username_before_EnsureUser(t *testing.T) {
	t.Parallel()
	ch := &captureChisel{}
	r := &RAPReconciler{chisel: ch}
	require.NoError(t, r.syncChiselFromToken("  alice:secret"))
	assert.Equal(t, "alice", ch.lastUser)
	assert.Equal(t, "secret", ch.lastPass)
}

func TestSpec_removeChiselUserFromToken_trims_username_before_RemoveUser(t *testing.T) {
	t.Parallel()
	ch := &captureChisel{}
	r := &RAPReconciler{chisel: ch}
	r.removeChiselUserFromToken("  bob:x")
	require.Len(t, ch.removed, 1)
	assert.Equal(t, "bob", ch.removed[0])
}
