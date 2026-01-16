// SPDX-FileCopyrightText: (C) 2025 Intel Corporation
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"net"
	"time"

	"google.golang.org/grpc/codes"

	inv_errors "github.com/open-edge-platform/infra-core/inventory/v2/pkg/errors"
)

// RemoteAccessProxyConfig contains configuration for Remote Access Proxy
type RemoteAccessProxyConfig struct {
	// Inventory connection settings
	InventoryAddr string

	// TLS settings
	CACertPath   string
	TLSKeyPath   string
	TLSCertPath  string
	InsecureGRPC bool

	// Inventory client settings
	InventoryTimeout        time.Duration
	ListAllInventoryTimeout time.Duration

	// Inventory client cache settings
	EnableUUIDCache    bool
	UUIDCacheTTL       time.Duration
	UUIDCacheTTLOffset int

	// Observability settings
	EnableTracing bool
	TraceURL      string
	EnableMetrics bool
	MetricsAddr   string

	// OAM settings
	OAMServerAddr string

	// Chisel server settings
	ChiselBindAddr  string
	ChiselPort      string
	ChiselKeySeed   string
	ChiselAuth      string
	ChiselKeepAlive time.Duration

	// WebSocket terminal server settings
	WebSocketAddr string

	// Reverse SSH settings
	ReverseSSHAddr        string
	ReverseSSHWaitTimeout time.Duration

	// Northbound handler settings
	ReconcileTickerPeriod time.Duration
	ReconcileParallelism  int
}

// Validate validates the configuration
func (c RemoteAccessProxyConfig) Validate() error {
	if c.InventoryAddr == "" {
		return inv_errors.Errorfc(codes.InvalidArgument,
			"empty inventory address: %s", c.InventoryAddr)
	}

	_, err := net.ResolveTCPAddr("tcp", c.InventoryAddr)
	if err != nil {
		return inv_errors.Errorfc(codes.InvalidArgument,
			"invalid inventory address %s: %s", c.InventoryAddr, err)
	}

	if !c.InsecureGRPC {
		if c.CACertPath == "" || c.TLSCertPath == "" || c.TLSKeyPath == "" {
			return inv_errors.Errorfc(codes.InvalidArgument,
				"gRPC connections should be secure, but one of secrets is not provided")
		}
	}

	if c.ChiselBindAddr == "" {
		return inv_errors.Errorfc(codes.InvalidArgument,
			"chisel bind address cannot be empty")
	}

	if c.ChiselPort == "" {
		return inv_errors.Errorfc(codes.InvalidArgument,
			"chisel port cannot be empty")
	}

	if c.WebSocketAddr == "" {
		return inv_errors.Errorfc(codes.InvalidArgument,
			"websocket address cannot be empty")
	}

	if c.ReverseSSHAddr == "" {
		return inv_errors.Errorfc(codes.InvalidArgument,
			"reverse SSH address cannot be empty")
	}

	return nil
}
