// SPDX-FileCopyrightText: (C) 2025 Intel Corporation
// SPDX-License-Identifier: Apache-2.0

package reconcilers

import (
	"regexp"
	"sync"
	"testing"

	chserver "github.com/jpillora/chisel/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testChiselServer(t *testing.T) *chserver.Server {
	t.Helper()
	srv, err := chserver.NewServer(&chserver.Config{
		KeySeed: "test-chisel-registrar-seed",
		Reverse: true,
	})
	require.NoError(t, err)
	return srv
}

// newChiselRegistrarForTest builds the production registrar with an arbitrary backend (tests only).
func newChiselRegistrarForTest(b chiselUserBackend) ChiselUserRegistrar {
	return &chiselServerRegistrar{backend: b}
}

type spyChiselUserBackend struct {
	mu    sync.Mutex
	users map[string]spyUserRecord
}

type spyUserRecord struct {
	pass  string
	addrs []string
}

func newSpyChiselUserBackend() *spyChiselUserBackend {
	return &spyChiselUserBackend{users: make(map[string]spyUserRecord)}
}

func (s *spyChiselUserBackend) AddUser(user, pass string, addrs ...string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.users[user] = spyUserRecord{
		pass:  pass,
		addrs: append([]string(nil), addrs...),
	}
	return nil
}

func (s *spyChiselUserBackend) DeleteUser(user string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.users, user)
}

func (s *spyChiselUserBackend) get(user string) (spyUserRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.users[user]
	return u, ok
}

// reverseRemoteAllowedByPatterns mirrors chisel settings.User.HasAccess using raw pattern strings.
// It does not assume a single pattern or a specific literal ".*", only that some pattern allows a typical reverse UserAddr.
func reverseRemoteAllowedByPatterns(addrs []string, sampleRemote string) bool {
	for _, p := range addrs {
		re, err := regexp.Compile(p)
		if err != nil {
			continue
		}
		if re.MatchString(sampleRemote) {
			return true
		}
	}
	return false
}

func TestSpec_ChiselServerRegistrar_nil_backend(t *testing.T) {
	t.Run("ensure_user_returns_error_without_panic", func(t *testing.T) {
		reg := newChiselRegistrarForTest(nil)
		err := reg.EnsureUser("u", "p")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "nil backend")
	})
	t.Run("remove_user_is_noop_without_panic", func(t *testing.T) {
		reg := newChiselRegistrarForTest(nil)
		reg.RemoveUser("any")
	})
}

func TestSpec_NewChiselServerRegistrar(t *testing.T) {
	t.Run("when_server_is_nil_then_registrar_is_noop", func(t *testing.T) {
		reg := NewChiselServerRegistrar(nil)
		require.NotNil(t, reg)
		require.NoError(t, reg.EnsureUser("any-user", "any-pass"))
		reg.RemoveUser("any-user")
		reg.RemoveUser("nonexistent")
	})

	t.Run("when_server_is_non_nil_then_returns_wrapper", func(t *testing.T) {
		srv := testChiselServer(t)
		reg := NewChiselServerRegistrar(srv)
		require.NotNil(t, reg)
		require.NoError(t, reg.EnsureUser("smoke-user", "smoke-pass"))
		reg.RemoveUser("smoke-user")
	})
}

func TestSpec_ChiselRegistrar_EnsureUser(t *testing.T) {
	t.Run("when_user_is_new_then_backend_receives_pass_and_patterns_allowing_reverse", func(t *testing.T) {
		spy := newSpyChiselUserBackend()
		reg := newChiselRegistrarForTest(spy)
		const user, pass = "rac-user-1", "secret-one"
		require.NoError(t, reg.EnsureUser(user, pass))

		rec, ok := spy.get(user)
		require.True(t, ok, "EnsureUser should call AddUser for the RAC user")
		assert.Equal(t, pass, rec.pass)
		assert.NotEmpty(t, rec.addrs, "EnsureUser should pass at least one remote allow pattern")
		// chisel validates reverse remotes with UserAddr like "R:127.0.0.1:<port>" (see chisel server_handler).
		assert.True(t, reverseRemoteAllowedByPatterns(rec.addrs, "R:127.0.0.1:21123"),
			"registered patterns should allow a typical reverse tunnel address")
	})

	t.Run("when_same_user_ensure_again_then_backend_receives_new_password", func(t *testing.T) {
		spy := newSpyChiselUserBackend()
		reg := newChiselRegistrarForTest(spy)
		const user = "rac-user-2"
		require.NoError(t, reg.EnsureUser(user, "first"))
		require.NoError(t, reg.EnsureUser(user, "second"))

		rec, ok := spy.get(user)
		require.True(t, ok)
		assert.Equal(t, "second", rec.pass)
	})
}

func TestSpec_ChiselRegistrar_RemoveUser(t *testing.T) {
	t.Run("when_user_never_added_then_remove_is_noop", func(t *testing.T) {
		spy := newSpyChiselUserBackend()
		reg := newChiselRegistrarForTest(spy)
		reg.RemoveUser("never-added")
		_, ok := spy.get("never-added")
		assert.False(t, ok)
	})

	t.Run("when_user_was_added_then_remove_deletes_user", func(t *testing.T) {
		spy := newSpyChiselUserBackend()
		reg := newChiselRegistrarForTest(spy)
		const name = "to-remove"
		require.NoError(t, reg.EnsureUser(name, "pw"))
		_, ok := spy.get(name)
		require.True(t, ok)

		reg.RemoveUser(name)

		_, ok = spy.get(name)
		assert.False(t, ok, "RemoveUser should call DeleteUser for the RAC user")
	})
}
