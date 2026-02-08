// SPDX-FileCopyrightText: (C) 2025 Intel Corporation
//
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"net"
	"time"

	"google.golang.org/grpc/codes"

	inv_errors "github.com/open-edge-platform/infra-core/inventory/v2/pkg/errors"
)

type RemoteAccessConfigMgrConfig struct {
	EnableTracing       bool
	EnableMetrics       bool
	TraceURL            string
	InventoryAddr       string
	CACertPath          string
	TLSKeyPath          string
	TLSCertPath         string
	InsecureGRPC        bool
	EnableHostDiscovery bool
	EnableUUIDCache     bool
	UUIDCacheTTL        time.Duration
	UUIDCacheTTLOffset  int

	// Inventory client settings
	InventoryTimeout        time.Duration
	ListAllInventoryTimeout time.Duration

	// Northbound handler settings
	ReconcileTickerPeriod time.Duration
	ReconcileParallelism  int
}

func (c RemoteAccessConfigMgrConfig) Validate() error {
	if c.InventoryAddr == "" {
		return inv_errors.Errorfc(codes.InvalidArgument,
			"empty inventory address: %s", c.InventoryAddr)
	}

	_, err := net.ResolveTCPAddr("tcp", c.InventoryAddr)
	if err != nil {
		return inv_errors.Errorfc(codes.InvalidArgument,
			"invalid inventory address %s: %s", c.InventoryAddr, err)
	}

	// Validate timeouts
	if c.InventoryTimeout <= 0 {
		return inv_errors.Errorfc(codes.InvalidArgument,
			"inventory timeout must be greater than 0, got: %v", c.InventoryTimeout)
	}
	if c.ListAllInventoryTimeout <= 0 {
		return inv_errors.Errorfc(codes.InvalidArgument,
			"list all inventory timeout must be greater than 0, got: %v", c.ListAllInventoryTimeout)
	}

	// Validate northbound handler settings
	if c.ReconcileTickerPeriod <= 0 {
		return inv_errors.Errorfc(codes.InvalidArgument,
			"reconcile ticker period must be greater than 0, got: %v", c.ReconcileTickerPeriod)
	}
	if c.ReconcileParallelism <= 0 {
		return inv_errors.Errorfc(codes.InvalidArgument,
			"reconcile parallelism must be greater than 0, got: %d", c.ReconcileParallelism)
	}

	// TODO: Enable TLS validation when certificates are available in test/prod environments
	// if !c.InsecureGRPC {
	// 	if c.CACertPath == "" || c.TLSCertPath == "" || c.TLSKeyPath == "" {
	// 		return inv_errors.Errorfc(codes.InvalidArgument,
	// 			"gRPC connections should be secure, but one of secrets is not provided")
	// 	}
	// }

	return nil
}
