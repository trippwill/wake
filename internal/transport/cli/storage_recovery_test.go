// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.trippwill.dev/wake/internal/config"
	persistence "go.trippwill.dev/wake/internal/persistence/sqlite"
)

func TestStorageBackupAndRestoreCLI(t *testing.T) {
	cfg, configPath, _ := setupDaemonFixture(t)
	if err := config.Prepare(cfg); err != nil {
		t.Fatal(err)
	}

	store, err := persistence.Open(t.Context(), persistence.Profile{
		WarehousePath: cfg.Storage.WarehousePath, ConnectionsPath: cfg.Storage.ConnectionsPath,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	archive := filepath.Join(cfg.Paths.ConfigDir, "offline.tar")

	var stdout, stderr bytes.Buffer

	backupArgs := []string{"--json", "storage", "backup", "--config", configPath, archive}
	status := Run(backupArgs, &stdout, &stderr)

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	if status == exitOK || !strings.Contains(stderr.String(), "Wake stopped") {
		t.Fatal(status, stdout.String(), stderr.String())
	}

	stdout.Reset()
	stderr.Reset()

	if status := Run(backupArgs, &stdout, &stderr); status != exitOK {
		t.Fatal(status, stdout.String(), stderr.String())
	}

	var result storageResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil || result.Event != "backup" ||
		result.Archive != archive || !result.WithCredentials {
		t.Fatal(stdout.String(), err)
	}

	stdout.Reset()
	stderr.Reset()

	if status := Run(
		[]string{"storage", "backup", "--config", configPath, archive + ".other"},
		&stdout,
		&stderr,
	); status != exitOK ||
		!strings.Contains(stdout.String(), "Retain the credential key separately") {
		t.Fatal(status, stdout.String(), stderr.String())
	}

	for _, path := range []string{cfg.Storage.WarehousePath, cfg.Storage.ConnectionsPath} {
		if err := os.Rename(path, path+".original"); err != nil {
			t.Fatal(err)
		}
	}

	stdout.Reset()
	stderr.Reset()

	// The automatic upgrade backup uses the same paired archive format.
	upgradeArchive := cfg.Storage.WarehousePath + ".upgrade-backup.tar"
	if err := os.Rename(archive, upgradeArchive); err != nil {
		t.Fatal(err)
	}

	restoreArgs := []string{"storage", "restore", "--config", configPath, "--with-credentials", upgradeArchive}
	if status := Run(restoreArgs, &stdout, &stderr); status != exitOK ||
		!strings.Contains(stdout.String(), "original credential key") {
		t.Fatal(status, stdout.String(), stderr.String())
	}
}

func TestStorageSnapshotWarehouseCLI(t *testing.T) {
	cfg, configPath, _ := setupDaemonFixture(t)
	if err := config.Prepare(cfg); err != nil {
		t.Fatal(err)
	}

	store, err := persistence.Open(t.Context(), persistence.Profile{
		WarehousePath: cfg.Storage.WarehousePath, ConnectionsPath: cfg.Storage.ConnectionsPath,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	archive := filepath.Join(cfg.Paths.ConfigDir, "history.tar")

	var stdout, stderr bytes.Buffer

	status := Run(
		[]string{"--json", "storage", "snapshot", "warehouse", "--config", configPath, archive},
		&stdout,
		&stderr,
	)

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	if status != exitOK {
		t.Fatal(status, stdout.String(), stderr.String())
	}

	var result storageResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil || result.Event != "snapshot" ||
		result.Archive != archive || result.WithCredentials {
		t.Fatal(stdout.String(), err)
	}

	stdout.Reset()
	stderr.Reset()

	if status := Run(
		[]string{"storage", "snapshot", "warehouse", "--config", configPath, archive + ".other"},
		&stdout,
		&stderr,
	); status != exitOK ||
		!strings.Contains(stdout.String(), "Saved a warehouse snapshot") {
		t.Fatal(status, stdout.String(), stderr.String())
	}

	if err := os.Rename(cfg.Storage.WarehousePath, cfg.Storage.WarehousePath+".original"); err != nil {
		t.Fatal(err)
	}

	stdout.Reset()
	stderr.Reset()

	restore := []string{"storage", "restore", "--config", configPath, archive}
	if status := Run(restore, &stdout, &stderr); status != exitOK ||
		!strings.Contains(stdout.String(), "Start Wake to read retained history") {
		t.Fatal(status, stdout.String(), stderr.String())
	}

	stdout.Reset()
	stderr.Reset()

	if status := Run(restore, &stdout, &stderr); status == exitOK ||
		!strings.Contains(stderr.String(), cfg.Storage.WarehousePath) {
		t.Fatal(status, stdout.String(), stderr.String())
	}
}

func TestStorageRecoveryProtectsInstancePaths(t *testing.T) {
	cfg, configPath, _ := setupDaemonFixture(t)
	if err := config.Prepare(cfg); err != nil {
		t.Fatal(err)
	}

	existing := filepath.Join(cfg.Paths.ConfigDir, "existing.tar")
	if err := os.WriteFile(existing, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name string
		args []string
		code int
		want string
	}{
		{"configuration", []string{"backup", configPath}, exitFailure, "unsafe"},
		{"key", []string{"snapshot", "warehouse", cfg.Credentials.KeyPath}, exitFailure, "unsafe"},
		{"sidecar", []string{"snapshot", "warehouse", cfg.Storage.WarehousePath + "-wal"}, exitFailure, "unsafe"},
		{"existing archive", []string{"snapshot", "warehouse", existing}, exitFailure, "output already exists"},
		{"missing output directory", []string{"backup", filepath.Join(cfg.Paths.ConfigDir, "missing", "archive")}, exitFailure, "no such file"},
		{"protected restore source", []string{"restore", configPath}, exitFailure, "unsafe"},
		{"missing restore archive", []string{"restore", filepath.Join(cfg.Paths.ConfigDir, "absent.tar")}, exitFailure, "no such file"},
		{"missing snapshot output", []string{"snapshot", "warehouse"}, exitUsage, "requires one archive file path"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer

			args := append([]string{"storage"}, test.args...)
			args = append(args, "--config", configPath)
			if result := Run(args, &stdout, &stderr); result != test.code ||
				!strings.Contains(stderr.String(), test.want) || stdout.Len() != 0 {
				t.Fatal(result, stdout.String(), stderr.String())
			}
		})
	}

	//nolint:gosec // Read the synthetic archive created by this test.
	data, err := os.ReadFile(existing)
	if err != nil || string(data) != "keep" {
		t.Fatal("existing output changed", err)
	}
}
