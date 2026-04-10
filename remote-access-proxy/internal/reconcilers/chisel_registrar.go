// SPDX-FileCopyrightText: (C) 2025 Intel Corporation
// SPDX-License-Identifier: Apache-2.0

package reconcilers

import (
	"errors"

	chserver "github.com/jpillora/chisel/server"
)

// ChiselUserRegistrar registers per-RAC Chisel users (must match RAC session_token user:pass).
type ChiselUserRegistrar interface {
	EnsureUser(user, pass string) error
	RemoveUser(user string)
}

type noopChiselRegistrar struct{}

func (noopChiselRegistrar) EnsureUser(user, pass string) error { return nil }
func (noopChiselRegistrar) RemoveUser(user string)             {}

// chiselUserBackend matches *chserver.Server AddUser/DeleteUser (per-RAC user index).
type chiselUserBackend interface {
	AddUser(user, pass string, addrs ...string) error
	DeleteUser(user string)
}

type chiselServerRegistrar struct {
	backend chiselUserBackend
}

// NewChiselServerRegistrar wraps the Chisel server for per-RAC AddUser/DeleteUser.
func NewChiselServerRegistrar(srv *chserver.Server) ChiselUserRegistrar {
	if srv == nil {
		return noopChiselRegistrar{}
	}
	return &chiselServerRegistrar{backend: srv}
}

// EnsureUser adds or replaces a user. Pattern ".*" allows reverse remotes for tunnel setup.
func (c *chiselServerRegistrar) EnsureUser(user, pass string) error {
	if c == nil || c.backend == nil {
		return errors.New("chisel registrar: nil backend")
	}
	return c.backend.AddUser(user, pass, ".*")
}

func (c *chiselServerRegistrar) RemoveUser(user string) {
	if c == nil || c.backend == nil {
		return
	}
	c.backend.DeleteUser(user)
}
