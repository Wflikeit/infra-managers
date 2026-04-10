// SPDX-FileCopyrightText: (C) 2025 Intel Corporation
// SPDX-License-Identifier: Apache-2.0

package chiselauth

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	chclient "github.com/jpillora/chisel/client"
	chserver "github.com/jpillora/chisel/server"
	chshare "github.com/jpillora/chisel/share"
	"github.com/jpillora/chisel/share/cnet"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

const (
	// Bound how long we wait for chclient.Wait() after Close() so tests cannot hang on a wedged client.
	chiselClientWaitAfterClose = 12 * time.Second
	// freeTCPPort leaves a TOCTOU window; retry server start a few times before blaming auth/config.
	chiselServerStartAttempts      = 12
	chiselServerTCPReadyPerAttempt = 4 * time.Second
)

// Dependency contract for jpillora/chisel as used by RAP: real chserver plus minimal clients.
//
// Negative path: WebSocket + crypto/ssh only — never completes password auth, so the server never reaches
// the fragile post-auth / pre-"config" window (see upstream server_handler).
//
// Positive path: official chclient with a reverse remote; we require a TCP byte to reach a local sink,
// which only happens after SSH auth and Chisel config (see share/tunnel Proxy.pipeRemote).

// freeTCPPort returns a port that was free at measurement time. It does not hold the port: between
// closing this probe listener and the next bind, another process may take it. startTestChiselServer
// retries with a fresh port to reduce flakes; a stolen port can still surface as a failed attempt, not as
// a false auth failure, once attempts are exhausted.
func freeTCPPort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	_, portStr, err := net.SplitHostPort(ln.Addr().String())
	require.NoError(t, err)
	require.NoError(t, ln.Close())
	return portStr
}

func attemptChiselSSHPassword(t *testing.T, port, user, pass string) error {
	t.Helper()
	wsURL := "ws://127.0.0.1:" + port + "/"
	d := websocket.Dialer{
		Subprotocols:     []string{chshare.ProtocolVersion},
		HandshakeTimeout: 8 * time.Second,
	}
	ws, _, err := d.Dial(wsURL, nil)
	if err != nil {
		return err
	}
	defer ws.Close()

	conn := cnet.NewWebSocketConn(ws)
	_, _, _, err = ssh.NewClientConn(conn, "", &ssh.ClientConfig{
		User:            user,
		Auth:            []ssh.AuthMethod{ssh.Password(pass)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		ClientVersion:   "SSH-" + chshare.ProtocolVersion + "-client",
		Timeout:         10 * time.Second,
	})
	return err
}

func waitTCPReadyErr(addr string, maxWait time.Duration) error {
	deadline := time.Now().Add(maxWait)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			_ = c.Close()
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return fmt.Errorf("tcp %s not ready within %s", addr, maxWait)
}

func waitChiselGoroutineDone(done <-chan struct{}, maxWait time.Duration) {
	select {
	case <-done:
	case <-time.After(maxWait):
	}
}

// awaitChiselClientWait closes the client when closeFirst is true, then waits for cli.Wait() with a hard
// deadline (avoids hanging the test if Wait does not unblock after Close).
func awaitChiselClientWait(t *testing.T, cli *chclient.Client, closeFirst bool, waitErr <-chan error, maxWait time.Duration) error {
	t.Helper()
	if closeFirst {
		_ = cli.Close()
	}
	select {
	case err := <-waitErr:
		return err
	case <-time.After(maxWait):
		t.Fatalf("chisel client Wait did not complete within %s (closeFirst=%v)", maxWait, closeFirst)
		return nil
	}
}

func startTestChiselServer(t *testing.T, cfg *chserver.Config) (srv *chserver.Server, port string, stop func()) {
	t.Helper()
	for attempt := 0; attempt < chiselServerStartAttempts; attempt++ {
		p := freeTCPPort(t)
		s, err := chserver.NewServer(cfg)
		require.NoError(t, err)
		s.Info = false
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			defer close(done)
			_ = s.StartContext(ctx, "127.0.0.1", p)
		}()
		addr := "127.0.0.1:" + p
		if waitTCPReadyErr(addr, chiselServerTCPReadyPerAttempt) == nil {
			return s, p, func() {
				cancel()
				_ = s.Close()
				waitChiselGoroutineDone(done, 5*time.Second)
			}
		}
		cancel()
		_ = s.Close()
		waitChiselGoroutineDone(done, 5*time.Second)
	}
	t.Fatalf("chisel server never accepted TCP after %d attempts (port contention / bind failure?)", chiselServerStartAttempts)
	return nil, "", nil
}

func TestSpec_ChiselDependency_authWithBarrierUser(t *testing.T) {
	barrier, err := GenerateCredentials()
	require.NoError(t, err)

	srv, port, stop := startTestChiselServer(t, &chserver.Config{
		KeySeed:   "rap-contract-test-seed",
		Auth:      barrier,
		Reverse:   true,
		KeepAlive: time.Second,
	})
	defer stop()

	t.Run("wrong_credentials_rejected_at_ssh_password_auth", func(t *testing.T) {
		err := attemptChiselSSHPassword(t, port, "intruder", "not-the-barrier-password")
		require.Error(t, err)
		low := strings.ToLower(err.Error())
		require.True(t, strings.Contains(low, "unable to authenticate") || strings.Contains(low, "handshake failed"),
			"expected SSH auth failure, got: %v", err)
	})

	t.Run("barrier_credentials_reverse_tunnel_delivers_bytes_to_sink", func(t *testing.T) {
		// R:serverListen:clientTarget — server listens on exposePort; client dials sink (held here).
		// See jpillora/chisel share/tunnel Proxy: listen on remote.Local, OpenChannel to remote.Remote.
		sink, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		defer sink.Close()
		_, sinkPort, err := net.SplitHostPort(sink.Addr().String())
		require.NoError(t, err)
		exposePort := freeTCPPort(t)
		remoteSpec := "R:127.0.0.1:" + exposePort + ":127.0.0.1:" + sinkPort

		acceptCh := make(chan net.Conn, 1)
		acceptErr := make(chan error, 1)
		go func() {
			c, err := sink.Accept()
			if err != nil {
				acceptErr <- err
				return
			}
			acceptCh <- c
		}()

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		cli, err := chclient.NewClient(&chclient.Config{
			Server:           "http://127.0.0.1:" + port,
			Fingerprint:      srv.GetFingerprint(),
			Auth:             barrier,
			Remotes:          []string{remoteSpec},
			MaxRetryCount:    0,
			MaxRetryInterval: time.Second,
			KeepAlive:        time.Second,
		})
		require.NoError(t, err)
		cli.Info = false
		require.NoError(t, cli.Start(ctx))

		waitErr := make(chan error, 1)
		go func() { waitErr <- cli.Wait() }()

		// Do not dial expose with waitTCPReadyErr first: that probe consumes one server Accept and one
		// client dial-to-sink, so the first sink.Accept can be an already-closed stub (Read -> EOF).
		exposeAddr := "127.0.0.1:" + exposePort
		deadline := time.Now().Add(20 * time.Second)
		var upstream net.Conn
	loopDial:
		for time.Now().Before(deadline) {
			// Non-blocking check: client death is visible before the next DialTimeout (up to ~400ms lag).
			select {
			case err := <-waitErr:
				if err != nil {
					t.Fatalf("chclient exited before reverse listen on %s: %v", exposeAddr, err)
				}
				t.Fatalf("chclient Wait returned nil before reverse listen on %s", exposeAddr)
			default:
			}
			c, err := net.DialTimeout("tcp", exposeAddr, 400*time.Millisecond)
			if err == nil {
				upstream = c
				break loopDial
			}
		}
		if upstream == nil {
			select {
			case err := <-waitErr:
				if err != nil {
					t.Fatalf("chclient exited while waiting for reverse listen on %s: %v", exposeAddr, err)
				}
				t.Fatalf("chclient Wait returned nil while waiting for reverse listen on %s", exposeAddr)
			default:
				t.Fatalf("reverse proxy never listened on %s (auth/config or port steal?)", exposeAddr)
			}
		}
		_, err = upstream.Write([]byte{0x42})
		require.NoError(t, err)

		var downstream net.Conn
		select {
		case downstream = <-acceptCh:
		case err := <-acceptErr:
			_ = upstream.Close()
			_ = awaitChiselClientWait(t, cli, true, waitErr, chiselClientWaitAfterClose)
			t.Fatalf("sink accept: %v", err)
		case <-time.After(15 * time.Second):
			_ = upstream.Close()
			_ = awaitChiselClientWait(t, cli, true, waitErr, chiselClientWaitAfterClose)
			t.Fatal("sink never accepted — reverse tunnel did not reach client after barrier auth")
		}
		defer downstream.Close()

		if rd, ok := downstream.(interface {
			SetReadDeadline(t time.Time) error
		}); ok {
			require.NoError(t, rd.SetReadDeadline(time.Now().Add(5*time.Second)))
		}

		buf := make([]byte, 1)
		_, err = downstream.Read(buf)
		require.NoError(t, err)
		require.Equal(t, byte(0x42), buf[0], "payload through tunnel should match")

		require.NoError(t, upstream.Close())
		err = awaitChiselClientWait(t, cli, true, waitErr, chiselClientWaitAfterClose)
		require.NoError(t, err)
	})
}
