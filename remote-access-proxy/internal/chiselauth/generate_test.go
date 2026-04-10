// SPDX-FileCopyrightText: (C) 2025 Intel Corporation
// SPDX-License-Identifier: Apache-2.0

package chiselauth

import (
	"strings"
	"testing"
)

func TestSpec_UsernameForRAC(t *testing.T) {
	t.Run("when_same_resource_id_then_username_stable", func(t *testing.T) {
		a := UsernameForRAC("remoteaccess-abc12345")
		b := UsernameForRAC("remoteaccess-abc12345")
		if a != b {
			t.Fatalf("expected stable username, got %q vs %q", a, b)
		}
	})

	t.Run("when_different_resource_id_then_usernames_differ", func(t *testing.T) {
		a := UsernameForRAC("remoteaccess-abc12345")
		c := UsernameForRAC("remoteaccess-ffff9999")
		if a == c {
			t.Fatalf("expected different resource IDs to differ, got %q", a)
		}
	})

	t.Run("when_username_generated_then_has_expected_prefix_and_length", func(t *testing.T) {
		a := UsernameForRAC("remoteaccess-abc12345")
		if len(a) < 8 || a[0] != 'r' {
			t.Fatalf("unexpected username %q", a)
		}
	})
}

func TestSpec_GenerateCredentials(t *testing.T) {
	t.Run("when_generated_then_form_is_user_colon_pass", func(t *testing.T) {
		a, err := GenerateCredentials()
		if err != nil {
			t.Fatal(err)
		}
		parts := strings.SplitN(a, ":", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			t.Fatalf("expected user:pass, got %q", a)
		}
		if len(parts[0]) < 4 || !strings.HasPrefix(parts[0], "rap") {
			t.Fatalf("unexpected user %q", parts[0])
		}
		if len(parts[1]) < 32 {
			t.Fatalf("password too short: %d", len(parts[1]))
		}
	})

	t.Run("when_called_twice_then_values_differ", func(t *testing.T) {
		a, err := GenerateCredentials()
		if err != nil {
			t.Fatal(err)
		}
		b, err := GenerateCredentials()
		if err != nil {
			t.Fatal(err)
		}
		if a == b {
			t.Fatal("expected distinct credentials")
		}
	})
}
