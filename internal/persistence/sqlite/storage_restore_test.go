// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

type restoreLogWriter func([]byte) (int, error)

func (w restoreLogWriter) Write(data []byte) (int, error) { return w(data) }

func TestStorageStartupLeavesDestinationsVacantWhenRestorePublishesIntent(t *testing.T) {
	ctx := t.Context()
	source := seedUpgradeStores(t)
	archive := filepath.Join(filepath.Dir(source.WarehousePath), "manual.backup")
	if err := Backup(ctx, source, archive); err != nil {
		t.Fatal(err)
	}

	file, err := openBackup(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()

	target := vacantRecoveryProfile(t)
	intentPath := target.WarehousePath + RestoreIntentSuffix
	writer := restoreLogWriter(func(data []byte) (int, error) {
		if strings.Contains(string(data), "event=storage_vacant store=connections") {
			if _, err := prepareRestore(ctx, file, target.paths(), intentPath); err != nil {
				t.Fatal(err)
			}
		}

		return len(data), nil
	})

	logger := slog.New(slog.NewTextHandler(writer, &slog.HandlerOptions{Level: slog.LevelDebug}))
	if store, err := Open(ctx, target, logger); !errors.Is(err, ErrRestorePending) {
		if store != nil {
			_ = store.Close()
		}

		t.Fatalf("startup must refuse a pending restore: %v", err)
	}

	if err := checkVacantStores([]string{target.WarehousePath, target.ConnectionsPath}); err != nil {
		t.Fatal(err)
	}

	if _, err := Restore(ctx, target, archive, true); err != nil {
		t.Fatal(err)
	}
}

func TestStorageBackupRestoresEvidenceAndAccess(t *testing.T) {
	ctx := t.Context()
	source := seedUpgradeStores(t)
	archive := filepath.Join(filepath.Dir(source.WarehousePath), "manual.backup")
	if err := Backup(ctx, source, archive); err != nil {
		t.Fatal(err)
	}

	if err := Backup(ctx, source, archive); !errors.Is(err, os.ErrExist) {
		t.Fatalf("backup should protect an existing archive: %v", err)
	}

	target := vacantRecoveryProfile(t)
	if _, err := Restore(ctx, target, archive, false); !errors.Is(err, ErrBackupScope) ||
		!strings.Contains(err.Error(), "--with-credentials") {
		t.Fatalf("paired backup should require explicit credential restore: %v", err)
	}

	paths, err := Restore(ctx, target, archive, true)
	targetPaths := target.paths()
	if err != nil || !slices.Equal(paths, targetPaths[:]) {
		t.Fatal(paths, err)
	}

	assertRestoredPair(t, target)
}

func TestStorageRestoreRefusesBothOccupiedDestinations(t *testing.T) {
	ctx := t.Context()
	target := vacantRecoveryProfile(t)
	blocking := []string{
		target.WarehousePath + "-wal",
		target.ConnectionsPath,
		target.ConnectionsPath + "-mjabcdef9ab",
	}
	for _, path := range blocking {
		if err := os.WriteFile(path, []byte("preserve"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	_, err := Restore(ctx, target, filepath.Join(t.TempDir(), "missing.backup"), true)
	if !errors.Is(err, ErrRestoreOccupied) {
		t.Fatal(err)
	}

	for _, path := range blocking {
		if !strings.Contains(err.Error(), path) {
			t.Errorf("missing blocked path %s", path)
		}
		//nolint:gosec // Private synthetic restore destination.
		data, err := os.ReadFile(path)
		if err != nil || string(data) != "preserve" {
			t.Fatal(path, err)
		}
	}

	if _, err := os.Stat(target.WarehousePath); !os.IsNotExist(err) {
		t.Fatal("unexpected warehouse", err)
	}
}

func TestStorageRestoreResumesPublishedUpgradeBackup(t *testing.T) {
	ctx := t.Context()

	source := seedUpgradeStores(t)
	plan := UpgradePlan{
		Stores:     make([]UpgradeStore, storeCount),
		BackupPath: source.WarehousePath + upgradeBackupSuffix,
	}

	var owners [storeCount]*os.File
	for i, p := range source.paths() {
		f, err := lockStoreFile(p, false)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = f.Close() }()

		owners[i] = f
		plan.Stores[i].Path = p
	}

	if _, err := createUpgradeBackup(ctx, plan, owners); err != nil {
		t.Fatal(err)
	}

	target := vacantRecoveryProfile(t)
	intentPath := target.WarehousePath + RestoreIntentSuffix

	file, err := openBackup(plan.BackupPath)
	if err != nil {
		t.Fatal(err)
	}

	intent, err := prepareRestore(ctx, file, target.paths(), intentPath)
	_ = file.Close()

	if err != nil {
		t.Fatal(err)
	}

	// Interruption after installing the warehouse, before installing access.
	if err := renameNewFile(intent.Stores[0].Stage, target.WarehousePath); err != nil {
		t.Fatal(err)
	}

	otherProfile := target
	otherProfile.ConnectionsPath += ".other"
	if _, err := Restore(ctx, otherProfile, plan.BackupPath, true); !errors.Is(err, ErrRestorePending) {
		t.Fatalf("retry with a different access destination: %v", err)
	}

	otherArchive := filepath.Join(filepath.Dir(plan.BackupPath), "other.backup")
	if err := os.WriteFile(otherArchive, []byte("wrong archive"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Restore(ctx, target, otherArchive, true); !errors.Is(err, ErrRestorePending) {
		t.Fatalf("retry with a different archive: %v", err)
	}

	savedStage := intent.Stores[1].Stage + ".saved"
	if err := os.Rename(intent.Stores[1].Stage, savedStage); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(intent.Stores[1].Stage, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Restore(ctx, target, plan.BackupPath, true); !errors.Is(err, ErrRestorePending) {
		t.Fatalf("retry with a replaced staging file: %v", err)
	}

	if err := os.Rename(savedStage, intent.Stores[1].Stage); err != nil {
		t.Fatal(err)
	}

	if store, err := Open(ctx, target, nil); !errors.Is(err, ErrRestorePending) {
		if store != nil {
			_ = store.Close()
		}

		t.Fatalf("startup must refuse a partial restore: %v", err)
	}

	journal := target.WarehousePath + "-journal"
	if err := os.WriteFile(journal, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Restore(ctx, target, plan.BackupPath, true); !errors.Is(err, ErrRestoreOccupied) ||
		!strings.Contains(err.Error(), journal) {
		t.Fatalf("retry must refuse a sidecar at the installed database: %v", err)
	}

	if _, err := os.Stat(journal); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(intentPath); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(target.ConnectionsPath); !os.IsNotExist(err) {
		t.Fatal("retry installed access despite the occupied sidecar", err)
	}

	if err := os.Remove(journal); err != nil {
		t.Fatal(err)
	}

	if _, err := Restore(ctx, target, plan.BackupPath, true); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(intentPath); !os.IsNotExist(err) {
		t.Fatal("completed restore left an intent", err)
	}

	assertRestoredPair(t, target)
}

func vacantRecoveryProfile(t *testing.T) Profile {
	t.Helper()

	profile := Profile{
		WarehousePath:   filepath.Join(t.TempDir(), "warehouse.db"),
		ConnectionsPath: filepath.Join(t.TempDir(), "access.db"),
	}
	for _, path := range profile.paths() {
		//nolint:gosec // Private synthetic restore destinations need owner traversal permission.
		if err := os.Chmod(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
	}

	return profile
}

func assertRestoredPair(t *testing.T, target Profile) {
	t.Helper()

	store, err := Open(t.Context(), target, nil)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = store.Close() }()

	view, err := store.Dashboard(t.Context())
	if err != nil || len(view.Executions) != 1 {
		t.Fatal(view, err)
	}

	var encrypted, paused int
	if err := store.db.QueryRow("SELECT count(*) FROM access.credentials WHERE encrypted=X'010203'").
		Scan(&encrypted); err != nil ||
		encrypted != 1 {
		t.Fatal(encrypted, err)
	}

	if err := store.db.QueryRow("SELECT paused FROM profile").Scan(&paused); err != nil || paused != 1 {
		t.Fatal(paused, err)
	}
}
