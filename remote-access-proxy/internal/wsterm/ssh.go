// SPDX-FileCopyrightText: (C) 2025 Intel Corporation
// SPDX-License-Identifier: Apache-2.0

package wsterm

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"time"

	"golang.org/x/crypto/ssh"
)

// BuildSSHAuthMethods loads key and/or password auth for reverse SSH.
func BuildSSHAuthMethods(privateKeyPath, password string) []ssh.AuthMethod {
	methods := make([]ssh.AuthMethod, 0, 2)
	if privateKeyPath != "" {
		if keyData, err := os.ReadFile(privateKeyPath); err == nil {
			if signer, err := ssh.ParsePrivateKey(keyData); err == nil {
				methods = append(methods, ssh.PublicKeys(signer))
			}
		}
	}
	if password != "" {
		methods = append(methods, ssh.Password(password))
	}
	return methods
}

// DialSSH opens an interactive session over TCP to reverseSSHAddr (usually 127.0.0.1:local_port).
func DialSSH(
	rows, cols int,
	term string,
	reverseSSHAddr string,
	sshUser string,
	privateKeyPath string,
	password string,
) (*ssh.Client, *ssh.Session, io.WriteCloser, io.Reader, error) {
	authMethods := BuildSSHAuthMethods(privateKeyPath, password)
	if len(authMethods) == 0 {
		return nil, nil, nil, nil, fmt.Errorf("no ssh auth methods configured")
	}
	sshCfg := &ssh.ClientConfig{
		User:            sshUser,
		Auth:            authMethods,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	}

	cli, err := ssh.Dial("tcp", reverseSSHAddr, sshCfg)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	sess, err := cli.NewSession()
	if err != nil {
		_ = cli.Close()
		return nil, nil, nil, nil, err
	}

	if rows <= 0 {
		rows = 24
	}
	if cols <= 0 {
		cols = 80
	}
	if term == "" {
		term = "xterm-256color"
	}

	if err := sess.RequestPty(term, rows, cols, ssh.TerminalModes{}); err != nil {
		_ = sess.Close()
		_ = cli.Close()
		return nil, nil, nil, nil, err
	}

	stdin, err := sess.StdinPipe()
	if err != nil {
		_ = sess.Close()
		_ = cli.Close()
		return nil, nil, nil, nil, err
	}
	stdout, err := sess.StdoutPipe()
	if err != nil {
		_ = sess.Close()
		_ = cli.Close()
		return nil, nil, nil, nil, err
	}
	stderr, _ := sess.StderrPipe()
	reader := io.MultiReader(stdout, stderr)

	if err := sess.Shell(); err != nil {
		_ = sess.Close()
		_ = cli.Close()
		return nil, nil, nil, nil, err
	}

	return cli, sess, stdin, reader, nil
}

// WaitPort polls until addr accepts TCP or max elapses.
func WaitPort(addr string, max time.Duration) error {
	deadline := time.Now().Add(max)
	for {
		conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("port not ready")
		}
		time.Sleep(200 * time.Millisecond)
	}
}
