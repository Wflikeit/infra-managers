// SPDX-FileCopyrightText: (C) 2025 Intel Corporation
// SPDX-License-Identifier: Apache-2.0

package common

import "time"

const (
	// Chisel server flags
	ChiselBindAddr            = "chiselBindAddr"
	ChiselBindAddrDescription = "Chisel server bind address (default: 0.0.0.0)"

	ChiselPort            = "chiselPort"
	ChiselPortDescription = "Chisel server port (default: 8080)"

	ChiselKeySeed            = "chiselKeySeed"
	ChiselKeySeedDescription = "Chisel key seed for server key generation"

	ChiselAuth            = "chiselAuth"
	ChiselAuthDescription = "Chisel authentication in format user:pass"

	ChiselKeepAlive            = "chiselKeepAlive"
	ChiselKeepAliveDescription = "Chisel keepalive interval (default: 25s)"

	// WebSocket server flags
	WebSocketAddr            = "wsAddr"
	WebSocketAddrDescription = "WebSocket terminal server address (default: 127.0.0.1:50052)"

	// Reverse SSH flags
	ReverseSSHAddr            = "reverseSSHAddr"
	ReverseSSHAddrDescription = "Reverse SSH address (default: 127.0.0.1:8000)"

	ReverseSSHWaitTimeout            = "reverseSSHWaitTimeout"
	ReverseSSHWaitTimeoutDescription = "Timeout for waiting for reverse SSH port to be ready (default: 10s)"

	// Inventory client flags
	InventoryTimeout            = "inventoryTimeout"
	InventoryTimeoutDescription = "Inventory API calls timeout (default: 5s)"

	ListAllInventoryTimeout            = "listAllInventoryTimeout"
	ListAllInventoryTimeoutDescription = "Timeout used when listing all resources for a given type from Inventory (default: 1m)"

	// Northbound handler flags
	ReconcileTickerPeriod            = "reconcileTickerPeriod"
	ReconcileTickerPeriodDescription = "Periodic reconciliation interval (default: 1h)"

	ReconcileParallelism            = "reconcileParallelism"
	ReconcileParallelismDescription = "Number of parallel reconciliations (default: 1)"
)

const (
	DefaultChiselBindAddr = "0.0.0.0"
	DefaultChiselPort     = "8080"
	DefaultChiselKeySeed  = "edge-demo-seed"
	DefaultChiselAuth     = "admin:secret"
	DefaultWebSocketAddr  = "127.0.0.1:50052"
	DefaultReverseSSHAddr = "127.0.0.1:8000"
)

const (
	DefaultReverseSSHWaitTimeoutDuration   = 10 * time.Second
	DefaultInventoryTimeoutDuration        = 5 * time.Second
	DefaultListAllInventoryTimeoutDuration = time.Minute
	DefaultReconcileTickerPeriodDuration   = 1 * time.Hour
	DefaultReconcileParallelism            = 1
)

var (
	// DefaultChiselKeepAlive is the default keepalive interval for Chisel server
	DefaultChiselKeepAlive = 25 * time.Second
	// DefaultReverseSSHWaitTimeout is the default timeout for waiting for reverse SSH port
	DefaultReverseSSHWaitTimeout = DefaultReverseSSHWaitTimeoutDuration
	// DefaultInventoryTimeout is the default timeout for inventory API calls
	DefaultInventoryTimeout = DefaultInventoryTimeoutDuration
	// DefaultListAllInventoryTimeout is the default timeout for listing all resources
	DefaultListAllInventoryTimeout = DefaultListAllInventoryTimeoutDuration
	// DefaultReconcileTickerPeriod is the default periodic reconciliation interval
	DefaultReconcileTickerPeriod = DefaultReconcileTickerPeriodDuration
)
