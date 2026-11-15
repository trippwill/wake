// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"bytes"
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
			if _, err := prepareRestore(ctx, file, target.paths(), intentPath, false, nil); err != nil {
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

	intent, err := prepareRestore(ctx, file, target.paths(), intentPath, false, nil)
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

func TestReplaceInstallationResumesAndPreservesOriginals(t *testing.T) {
	ctx := t.Context()
	source, target := seedUpgradeStores(t), seedUpgradeStores(t)
	archivePath := filepath.Join(filepath.Dir(source.WarehousePath), "installation.tar")
	if err := Backup(ctx, source, archivePath); err != nil {
		t.Fatal(err)
	}

	paths := target.paths()

	var originals [storeCount][]byte
	for i, path := range paths {
		originals[i] = readRestoreFixture(t, path)
	}

	archive, err := openBackup(archivePath)
	if err != nil {
		t.Fatal(err)
	}

	intentPath := paths[0] + RestoreIntentSuffix
	intent, err := prepareRestore(ctx, archive, paths, intentPath, true, nil)
	_ = archive.Close()

	if err != nil {
		t.Fatal(err)
	}

	// Two completed renames and an absent destination are both valid retry states.
	for i, state := range intent.Stores[:2] {
		if err := renameNewFile(state.Path, state.Original.Path); err != nil {
			t.Fatal(err)
		}

		if i == 0 {
			if err := renameNewFile(state.Stage, state.Path); err != nil {
				t.Fatal(err)
			}
		}
	}

	if _, err := OpenFinancial(ctx, target, nil); !errors.Is(err, ErrRestorePending) {
		t.Fatal("financial startup accepted partially replaced files", err)
	}

	if _, err := Restore(ctx, target, archivePath, true); !errors.Is(err, ErrRestorePending) {
		t.Fatal("retry accepted a different replacement choice", err)
	}

	stage := intent.Stores[2].Stage
	contents := readRestoreFixture(t, stage)
	if err := os.WriteFile(stage, []byte("changed in place"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := restore(ctx, target, archivePath, true, true, nil); !errors.Is(err, ErrRestorePending) {
		t.Fatal("retry accepted modified bytes at the same inode", err)
	}

	if !bytes.Equal(originals[2], readRestoreFixture(t, paths[2])) {
		t.Fatal("refused retry changed the synchronization store")
	}

	if err := os.WriteFile(stage, contents, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := restore(ctx, target, archivePath, true, true, nil); err != nil {
		t.Fatal(err)
	}

	for i, state := range intent.Stores {
		if !bytes.Equal(originals[i], readRestoreFixture(t, state.Original.Path)) {
			t.Fatal("replacement changed original database", state.Path)
		}
	}

	assertRestoredPair(t, target)

	store, err := Open(ctx, target, nil)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = store.Close() }()

	var staleWork, previousGrants int
	if err := store.db.QueryRow("SELECT count(*) FROM sync.work WHERE state_version<>''").
		Scan(&staleWork); err != nil ||
		staleWork != 0 {
		t.Fatal("restored provider continuation", staleWork, err)
	}

	if err := store.db.QueryRow("SELECT count(*) FROM access.upstream_grant WHERE granted_revision<>''").
		Scan(&previousGrants); err != nil ||
		previousGrants != 0 {
		t.Fatal("restored upstream grant", previousGrants, err)
	}
}

func readRestoreFixture(t *testing.T, path string) []byte {
	t.Helper()
	//nolint:gosec // The test owns each input and destination.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	return data
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
