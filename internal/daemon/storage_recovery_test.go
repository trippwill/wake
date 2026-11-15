// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package daemon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"go.trippwill.dev/wake/internal/config"
	persistence "go.trippwill.dev/wake/internal/persistence/sqlite"
)

func TestBackupStorageRefusesOwnedOrChangedConfiguration(t *testing.T) {
	ctx := t.Context()
	cfg := shellConfig(t)
	path := filepath.Join(cfg.Paths.ConfigDir, "config.toml")
	cfg.Paths.ConfigFile = path
	archive := filepath.Join(cfg.Paths.ConfigDir, "install.tar")

	owner, err := acquireProcessOwnership(cfg.Paths.RuntimeDir)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.close()

	if _, err = BackupStorage(ctx, cfg, path, archive); !errors.Is(err, ErrAlreadyOwned) {
		t.Fatal("backup accepted a running instance", err)
	}

	owner.close()

	if _, err = BackupStorage(ctx, cfg, path, archive); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("backup accepted missing configuration", err)
	}

	for _, contents := range []string{"[", fmt.Sprintf("[paths]\nruntime_dir=%q\n", cfg.Paths.RuntimeDir)} {
		if err = os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}

		if _, err = BackupStorage(ctx, cfg, path, archive); !errors.Is(err, config.ErrInvalid) {
			t.Fatal("backup accepted different configuration", err)
		}
	}

	if _, err = os.Lstat(archive); !os.IsNotExist(err) {
		t.Fatal("refused backup published an archive", err)
	}
}

func TestFinancialRecoveryWithUnusableOptionalPaths(t *testing.T) {
	ctx := t.Context()
	cfg := shellConfig(t)
	blocked := filepath.Join(cfg.Paths.ConfigDir, "optional")
	if err := os.WriteFile(blocked, []byte("untouched"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg.Storage.ConnectionsPath = filepath.Join(blocked, "connections.db")
	cfg.Storage.SyncPath = filepath.Join(blocked, "sync.db")

	store, err := persistence.OpenFinancial(
		ctx,
		persistence.Profile{WarehousePath: cfg.Storage.WarehousePath},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = store.Close() }()

	archive := filepath.Join(cfg.Paths.ConfigDir, "warehouse.tar")
	if _, err = SnapshotWarehouse(ctx, cfg, "", archive); err != nil {
		t.Fatal(err)
	}

	if err = store.Close(); err != nil {
		t.Fatal(err)
	}

	cfg.Storage.WarehousePath = filepath.Join(resetPrivateDir(t), "restored.db")

	invalid := cfg
	invalid.Storage.WarehousePath = filepath.Join(blocked, "warehouse.db")
	if _, err = SnapshotWarehouse(ctx, invalid, "", archive+".unwritten"); err == nil {
		t.Fatal("snapshot accepted an invalid financial directory")
	}

	invalid = cfg
	invalid.Paths.ConfigDir = blocked
	if _, err = RestoreStorage(ctx, invalid, "", archive, false); err == nil {
		t.Fatal("restore accepted an invalid instance directory")
	}

	owner, err := acquireProcessOwnership(cfg.Paths.RuntimeDir)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.close()

	if _, err = RestoreStorage(ctx, cfg, "", archive, false); !errors.Is(err, ErrAlreadyOwned) {
		t.Fatal("restore accepted an owned runtime", err)
	}

	if _, err = os.Stat(cfg.Storage.WarehousePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("refused restore installed a warehouse", err)
	}

	owner.close()

	if _, err = RestoreStorage(ctx, cfg, "", archive, false); err != nil {
		t.Fatal("restore required optional directories", err)
	}
	//nolint:gosec // This file belongs to the test's disposable profile.
	data, err := os.ReadFile(blocked)
	if err != nil || string(data) != "untouched" {
		t.Fatal("financial recovery changed optional paths", err)
	}
}
