// SPDX-FileCopyrightText: (C) 2025 Intel Corporation
// SPDX-License-Identifier: Apache-2.0

package wsterm

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildSSHAuthMethods(t *testing.T) {
	t.Parallel()

	t.Run("empty_path_and_password", func(t *testing.T) {
		t.Parallel()
		assert.Empty(t, BuildSSHAuthMethods("", ""))
	})

	t.Run("password_only", func(t *testing.T) {
		t.Parallel()
		m := BuildSSHAuthMethods("", "secret")
		require.Len(t, m, 1)
	})

	t.Run("invalid_key_path_ignored_password_still_used", func(t *testing.T) {
		t.Parallel()
		m := BuildSSHAuthMethods(filepath.Join(t.TempDir(), "nonexistent"), "pw")
		require.Len(t, m, 1)
	})

	t.Run("garbage_key_file_no_password", func(t *testing.T) {
		t.Parallel()
		p := filepath.Join(t.TempDir(), "bad.pem")
		require.NoError(t, os.WriteFile(p, []byte("not a private key"), 0600))
		assert.Empty(t, BuildSSHAuthMethods(p, ""))
	})

	t.Run("valid_ed25519_pkcs8_pem", func(t *testing.T) {
		t.Parallel()
		_, priv, err := ed25519.GenerateKey(rand.Reader)
		require.NoError(t, err)
		der, err := x509.MarshalPKCS8PrivateKey(priv)
		require.NoError(t, err)
		p := filepath.Join(t.TempDir(), "id_ed25519")
		require.NoError(t, os.WriteFile(p, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0600))
		m := BuildSSHAuthMethods(p, "")
		require.Len(t, m, 1)
	})

	t.Run("valid_ed25519_pem_and_password", func(t *testing.T) {
		t.Parallel()
		_, priv, err := ed25519.GenerateKey(rand.Reader)
		require.NoError(t, err)
		der, err := x509.MarshalPKCS8PrivateKey(priv)
		require.NoError(t, err)
		p := filepath.Join(t.TempDir(), "id_ed25519")
		require.NoError(t, os.WriteFile(p, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0600))
		m := BuildSSHAuthMethods(p, "extra")
		require.Len(t, m, 2)
	})
}

func TestWaitPort(t *testing.T) {
	t.Parallel()

	t.Run("succeeds_when_port_accepts", func(t *testing.T) {
		t.Parallel()
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		defer ln.Close()
		addr := ln.Addr().String()
		go func() {
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				_ = c.Close()
			}
		}()
		done := make(chan error, 1)
		go func() { done <- WaitPort(addr, 5*time.Second) }()
		select {
		case err := <-done:
			require.NoError(t, err)
		case <-time.After(10 * time.Second):
			t.Fatal("WaitPort did not return")
		}
	})

	t.Run("fails_when_port_closed", func(t *testing.T) {
		t.Parallel()
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		addr := ln.Addr().String()
		require.NoError(t, ln.Close())

		max := 400 * time.Millisecond
		start := time.Now()
		err = WaitPort(addr, max)
		elapsed := time.Since(start)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "port not ready")
		// WaitPort may overshoot max (see ssh.go: DialTimeout 500ms + 200ms sleep). Still must
		// return quickly — not hang for seconds on a refused port.
		assert.Less(t, elapsed, max+800*time.Millisecond, "WaitPort should not block far beyond dial/sleep overhead")
	})

	t.Run("zero_max_still_returns_error", func(t *testing.T) {
		t.Parallel()
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		addr := ln.Addr().String()
		require.NoError(t, ln.Close())

		err = WaitPort(addr, 0)
		require.Error(t, err)
	})
}
