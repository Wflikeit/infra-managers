// SPDX-FileCopyrightText: (C) 2025 Intel Corporation
// SPDX-License-Identifier: Apache-2.0

// Package wsterm implements the browser /term WebSocket terminal on RAP (reverse SSH to the edge).
//
// Wire protocol (text WebSocket frames, JSON): client sends {"type":"resize","rows":n,"cols":m}
// (typically first after open) and {"type":"stdio","data":"..."} for keystrokes; server sends
// {"type":"stdio","data":"..."} for PTY output and {"type":"error","code","message"} before closing
// on handshake failures. Inventory-backed handlers require tenant_id and resource_id query params
// to load RAC (reverse port); ssh_user overrides ra.User when present. For the pre-upgrade term gate,
// a non-empty ssh_user also satisfies the “binding has a UNIX user” check if Inventory has not set user yet.
//
// Chisel agent<->RAP tunnels and SSH EN<->RAP paths are separate concerns (see reconcilers and cmd).
package wsterm
