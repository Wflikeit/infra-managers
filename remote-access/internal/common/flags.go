// SPDX-FileCopyrightText: (C) 2025 Intel Corporation
// SPDX-License-Identifier: Apache-2.0

package common

import "time"

const (
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
	DefaultInventoryTimeoutDuration        = 5 * time.Second
	DefaultListAllInventoryTimeoutDuration = time.Minute
	DefaultReconcileTickerPeriodDuration   = 1 * time.Hour
	DefaultReconcileParallelism            = 1
)

var (
	// DefaultInventoryTimeout is the default timeout for inventory API calls
	DefaultInventoryTimeout = DefaultInventoryTimeoutDuration
	// DefaultListAllInventoryTimeout is the default timeout for listing all resources
	DefaultListAllInventoryTimeout = DefaultListAllInventoryTimeoutDuration
	// DefaultReconcileTickerPeriod is the default periodic reconciliation interval
	DefaultReconcileTickerPeriod = DefaultReconcileTickerPeriodDuration
)
