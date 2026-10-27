// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package cli

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAdminProvisioningFailuresAndProxyGuard(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	var stdout, stderr bytes.Buffer
	if code := runAdmin([]string{"set-proxy-guard"}, &stdout, &stderr); code != exitOK {
		t.Fatalf("proxy guard = %d, %s", code, stderr.String())
	}

	guardLine := stdout.String()
	guard := strings.TrimSpace(
		strings.TrimPrefix(guardLine, "wake: reverse-proxy guard (store it in the proxy only): "),
	)
	if guard == "" || !strings.HasPrefix(guardLine, "wake: reverse-proxy guard") {
		t.Fatal("proxy guard missing from provisioning output")
	}

	configDir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "wake")

	//nolint:gosec // Read only a generated synthetic guard inside the test directory.
	stored, err := os.ReadFile(filepath.Join(configDir, "admin-proxy.key"))
	if err != nil {
		t.Fatal(err)
	}
	defer clear(stored)

	if guard != base64.RawURLEncoding.EncodeToString(stored) {
		t.Fatal("displayed guard differs from persisted guard")
	}
	// A file at the configuration directory must fail closed for provisioning.
	blocked := filepath.Join(t.TempDir(), "blocked")
	if err = os.WriteFile(blocked, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("XDG_CONFIG_HOME", blocked)

	for _, command := range []string{"set-proxy-guard"} {
		stdout.Reset()
		stderr.Reset()

		if code := runAdmin([]string{command}, &stdout, &stderr); code != exitFailure || stdout.Len() != 0 {
			t.Fatalf("blocked %s = %d, stdout = %q", command, code, stdout.String())
		}
	}
}
