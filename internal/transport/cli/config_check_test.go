// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package cli

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"go.trippwill.dev/wake/internal/credential"
)

func TestConfigCheckKeyIsOptional(t *testing.T) {
	dir := setupEnvironment(t)
	path := filepath.Join(dir, "config.toml")
	keyPath := filepath.Join(dir, "provided.key")

	writeTestConfig(t, path, []byte("[credentials]\nkey_path = 'provided.key'\n"))

	args := []string{"--json", "config", "check", "--config", path}

	var stdout, stderr bytes.Buffer

	if code := run(args, &stdout, &stderr); code != exitOK || strings.Contains(stdout.String(), "key_path") {
		t.Fatal(code, stdout.String(), stderr.String())
	}

	args = append(args, "--check-key")

	stdout.Reset()
	stderr.Reset()

	if code := run(args, &stdout, &stderr); code != exitFailure ||
		!strings.Contains(stderr.String(), keyPath) || stdout.Len() != 0 {
		t.Fatal(code, stdout.String(), stderr.String())
	}

	if err := credential.CreateMasterKey(keyPath); err != nil {
		t.Fatal(err)
	}

	key := readTestMaterial(t, keyPath)

	stdout.Reset()
	stderr.Reset()

	if code := run(args, &stdout, &stderr); code != exitOK {
		t.Fatal(code, stderr.String())
	}

	var result struct {
		Valid      bool   `json:"valid"`
		ConfigPath string `json:"config_path"`
		KeyPath    string `json:"key_path"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil ||
		!result.Valid || result.ConfigPath != path || result.KeyPath != keyPath {
		t.Fatal("unexpected config check result", stdout.String(), err)
	}

	stdout.Reset()
	stderr.Reset()

	if code := run(args[1:], &stdout, &stderr); code != exitOK ||
		!strings.Contains(stdout.String(), "credential encryption key file is valid: "+keyPath) ||
		!bytes.Equal(key, readTestMaterial(t, keyPath)) {
		t.Fatal(code, stdout.String(), stderr.String())
	}
}
