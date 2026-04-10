// SPDX-FileCopyrightText: (C) 2025 Intel Corporation
// SPDX-License-Identifier: Apache-2.0

// Package wsterm implements the browser /term WebSocket terminal on RAP (reverse SSH to the edge)
// and Inventory/RAC-based admission before upgrade. Chisel agent↔RAP tunnels and SSH EN↔RAP paths
// are separate concerns (see reconcilers and cmd).
package wsterm
