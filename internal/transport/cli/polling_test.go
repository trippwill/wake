// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestConfigCheckPollingDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(dir, "runtime"))

	for _, test := range []struct {
		name, table, setting string
		valid                bool
	}{
		{"registered stream", "polling.defaults", `"ibkr-flex:confirmations" = "10m"`, true},
		{"unknown stream", "polling.defaults", `"ibkr-flex:unknown" = "10m"`, false},
		{"below stream minimum", "polling.defaults", `"snaptrade:activities" = "10m"`, false},
		{"invalid duration", "polling.defaults", `"snaptrade:activities" = "tomorrow"`, false},
		{"wrong table spelling", "Polling.defaults", `"snaptrade:activities" = "8h"`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			data := "[" + test.table + "]\n" + test.setting + "\n"
			if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
				t.Fatal(err)
			}

			var stdout, stderr bytes.Buffer

			code := run([]string{"config", "check", "--config", path}, &stdout, &stderr)
			if (code == exitOK) != test.valid {
				t.Fatalf("config check exit %d, want valid=%t: %s", code, test.valid, stderr.String())
			}
		})
	}
}
