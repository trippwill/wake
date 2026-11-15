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

	for _, path := range []string{cfg.Storage.WarehousePath, cfg.Storage.ConnectionsPath} {
		if err := os.Rename(path, path+".original"); err != nil {
			t.Fatal(err)
		}
	}

	stdout.Reset()
	stderr.Reset()

	restoreArgs := []string{"storage", "restore", "--config", configPath, "--with-credentials", archive}
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
