// SPDX-FileCopyrightText: (C) 2025 Intel Corporation
// SPDX-License-Identifier: Apache-2.0

package reconcilers

import chserver "github.com/jpillora/chisel/server"

// ChiselUserRegistrar registers per-RAC Chisel users (must match RAC session_token user:pass).
type ChiselUserRegistrar interface {
	EnsureUser(user, pass string) error
	RemoveUser(user string)
}

type noopChiselRegistrar struct{}

func (noopChiselRegistrar) EnsureUser(user, pass string) error { return nil }
func (noopChiselRegistrar) RemoveUser(user string)             {}

type chiselServerRegistrar struct {
	srv *chserver.Server
}

// NewChiselServerRegistrar wraps the Chisel server for per-RAC AddUser/DeleteUser.
func NewChiselServerRegistrar(srv *chserver.Server) ChiselUserRegistrar {
	if srv == nil {
		return noopChiselRegistrar{}
	}
	return &chiselServerRegistrar{srv: srv}
}

// EnsureUser adds or replaces a user. Pattern ".*" allows reverse remotes for tunnel setup.
func (c *chiselServerRegistrar) EnsureUser(user, pass string) error {
	return c.srv.AddUser(user, pass, ".*")
}

func (c *chiselServerRegistrar) RemoveUser(user string) {
	c.srv.DeleteUser(user)
}
