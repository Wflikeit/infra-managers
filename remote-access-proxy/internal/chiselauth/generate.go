// SPDX-FileCopyrightText: (C) 2025 Intel Corporation
// SPDX-License-Identifier: Apache-2.0

package chiselauth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// GenerateCredentials returns a user:pass string for Chisel server Auth and RAC session_token.
// User is rap + 8 hex chars; password is 48 hex chars (24 random bytes). No ':' in password.
func GenerateCredentials() (auth string, err error) {
	var u [4]byte
	if _, err = rand.Read(u[:]); err != nil {
		return "", fmt.Errorf("chisel auth user entropy: %w", err)
	}
	var p [24]byte
	if _, err = rand.Read(p[:]); err != nil {
		return "", fmt.Errorf("chisel auth password entropy: %w", err)
	}
	user := "rap" + hex.EncodeToString(u[:])
	pass := hex.EncodeToString(p[:])
	return user + ":" + pass, nil
}

// UsernameForRAC returns a stable Chisel username derived from RAC resource_id (unique, no colons).
func UsernameForRAC(resourceID string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(resourceID)))
	return "r" + hex.EncodeToString(sum[:8])
}

// GeneratePasswordHex returns a random password suitable for Chisel (hex, no ':').
func GeneratePasswordHex() (string, error) {
	var p [24]byte
	if _, err := rand.Read(p[:]); err != nil {
		return "", fmt.Errorf("chisel password entropy: %w", err)
	}
	return hex.EncodeToString(p[:]), nil
}
