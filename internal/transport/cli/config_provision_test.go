// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProvisionCredentialKeyPreservesExistingTOML(t *testing.T) {
	setupEnvironment(t)
	dir := filepath.Join(t.TempDir(), "profile")
	path := filepath.Join(dir, "existing.toml")
	keyPath := filepath.Join(dir, "operator.key")
	content := []byte(
		"# keep this comment and formatting\n[credentials]\nkey_path='operator.key'\n[storage]\nconnections_path='optional/connections.db'\n",
	)
	writeTestConfig(t, path, content)

	args := []string{"--json", "access", "provision", "credential-key", "--config", path}

	var stdout, stderr bytes.Buffer
	if code := run(args, &stdout, &stderr); code != exitOK {
		t.Fatal(code, stderr.String())
	}

	var result keyProvisionResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil ||
		!result.KeyGenerated || result.ConfigPath != path || result.KeyPath != keyPath {
		t.Fatal("unexpected key provisioning result", stdout.String(), err)
	}

	key := readTestMaterial(t, keyPath)

	info, err := os.Stat(keyPath)
	if err != nil || info.Mode().Perm() != 0o400 || len(key) != 32 {
		t.Fatal("expected a private 32-byte key", info, err)
	}

	stdout.Reset()
	stderr.Reset()

	if code := run(args[1:], &stdout, &stderr); code != exitOK ||
		!strings.Contains(stdout.String(), "Using existing credential encryption key") {
		t.Fatal(code, stdout.String(), stderr.String())
	}

	if !bytes.Equal(content, readTestMaterial(t, path)) || !bytes.Equal(key, readTestMaterial(t, keyPath)) {
		t.Fatal("provisioning changed the existing TOML or replaced the key")
	}

	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 2 {
		t.Fatal("provisioning created other instance files", entries, err)
	}

	accessDir := filepath.Join(dir, "optional")
	if err = os.Mkdir(accessDir, 0o700); err != nil {
		t.Fatal(err)
	}

	journal := filepath.Join(accessDir, "connections.db-journal")
	pending := []byte("pending SQLite recovery")
	if err = os.WriteFile(journal, pending, 0o600); err != nil {
		t.Fatal(err)
	}

	if err = os.Remove(keyPath); err != nil {
		t.Fatal(err)
	}

	stdout.Reset()
	stderr.Reset()

	if code := run(args, &stdout, &stderr); code != exitFailure ||
		!strings.Contains(stderr.String(), "upgrade recovery required") {
		t.Fatal("provisioning ignored an orphaned access journal", code, stderr.String())
	}

	if _, err = os.Lstat(keyPath); !errors.Is(err, os.ErrNotExist) ||
		!bytes.Equal(pending, readTestMaterial(t, journal)) {
		t.Fatal("provisioning changed an unresolved access profile", err)
	}
}

func TestProvisionCredentialKeyRequiresExistingTOML(t *testing.T) {
	dir := setupEnvironment(t)

	var stdout, stderr bytes.Buffer
	if code := run([]string{"access", "provision", "credential-key"}, &stdout, &stderr); code != exitFailure ||
		!strings.Contains(stderr.String(), "config.toml") {
		t.Fatal(code, stderr.String())
	}

	if _, err := os.Lstat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("provisioning created an instance directory without a TOML", err)
	}
}
