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
	"go.trippwill.dev/wake/internal/daemon"
)

func TestUpgradeCLIPreviewCreatesNoFilesAndRejectsStaleConfirmation(t *testing.T) {
	dir := t.TempDir()
	warehouse := filepath.Join(dir, "missing", "warehouse.db")
	access := filepath.Join(dir, "missing", "access.db")

	args := []string{"--json", "upgrade", "--warehouse-path", warehouse, "--connections-path", access}

	var stdout, stderr bytes.Buffer
	if status := Run(args, &stdout, &stderr); status != 0 {
		t.Fatal(status, stderr.String())
	}

	var plan daemon.UpgradePlan
	if err := json.Unmarshal(stdout.Bytes(), &plan); err != nil || plan.ID != "" ||
		plan.Stores[0].Target != "wake-warehouse/1" || plan.Stores[1].Target != "wake-access/1" {
		t.Fatal(stdout.String(), err)
	}

	if _, err := os.Stat(filepath.Dir(warehouse)); !os.IsNotExist(err) {
		t.Fatal("preview prepared directories", err)
	}

	cfg, path, _ := setupDaemonFixture(t)

	stdout.Reset()
	stderr.Reset()

	if status := Run(
		[]string{"upgrade", "--config", path, "--confirm", "stale"},
		&stdout,
		&stderr,
	); status != exitFailure ||
		!strings.Contains(stderr.String(), "plan changed") {
		t.Fatal(status, stdout.String(), stderr.String())
	}

	if _, err := os.Stat(cfg.Storage.WarehousePath); !os.IsNotExist(err) {
		t.Fatal("stale confirmation created a warehouse", err)
	}
}

func TestUpgradeConfirmationCommandQuotesExplicitProfile(t *testing.T) {
	resolved := config.Resolution{
		ConfigPath:   "/tmp/Wake's config.toml",
		ConfigSource: "flag",
		Values: []config.Value{
			{Key: "storage.warehouse_path", Value: "/tmp/wake store.db", Source: "flag"},
		},
	}

	got := upgradeConfirmationCommand(resolved, "abc")
	want := `wake upgrade --config '/tmp/Wake'"'"'s config.toml' --warehouse-path='/tmp/wake store.db' --confirm abc`
	if got != want {
		t.Fatal(got)
	}
}
