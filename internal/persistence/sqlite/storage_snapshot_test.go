// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestStorageSnapshotCopiesOnlyCommittedWarehouse(t *testing.T) {
	ctx := t.Context()
	source := seedUpgradeStores(t)

	live, err := Open(ctx, source, nil)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = live.Close() }()

	before, err := snapshotState(ctx, live)
	if err != nil || len(before.Resources) == 0 {
		t.Fatal(before, err)
	}

	tx, err := live.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec("INSERT INTO connections(id,provider) VALUES('uncommitted','synthetic')"); err != nil {
		t.Fatal(err)
	}

	archive := filepath.Join(filepath.Dir(source.WarehousePath), "history.tar")
	if err := SnapshotWarehouse(ctx, source, archive); err != nil {
		t.Fatal(err)
	}

	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}

	target := vacantRecoveryProfile(t)
	if _, err := Restore(ctx, target, archive, true); !errors.Is(err, ErrBackupScope) {
		t.Fatalf("snapshot should refuse credential restore: %v", err)
	}

	paths, err := Restore(ctx, target, archive, false)
	if err != nil || len(paths) != 1 || paths[0] != target.WarehousePath {
		t.Fatal(paths, err)
	}

	if _, err := os.Stat(target.ConnectionsPath); !os.IsNotExist(err) {
		t.Fatal("snapshot restore created an access store", err)
	}

	restored, err := Open(ctx, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = restored.Close() }()

	after, err := snapshotState(ctx, restored)
	if err != nil || after.WarehouseID == before.WarehouseID ||
		!reflect.DeepEqual(after.Resources, before.Resources) {
		t.Fatal("restored history or publication identity differs", err)
	}

	var uncommitted, credentials, paused int

	err = restored.db.QueryRow(`SELECT (SELECT count(*) FROM connections WHERE id='uncommitted'),
		(SELECT count(*) FROM access.credentials), (SELECT paused FROM profile)`).
		Scan(&uncommitted, &credentials, &paused)
	if err != nil || uncommitted != 0 || credentials != 0 || paused != 1 {
		t.Fatal(uncommitted, credentials, paused, err)
	}
}

func TestStorageSnapshotRejectsChangedArchive(t *testing.T) {
	live, warehouse := testStore(t)
	profile := testProfile(warehouse)
	archive := filepath.Join(filepath.Dir(warehouse), "history.tar")
	if err := SnapshotWarehouse(t.Context(), profile, archive); err != nil {
		t.Fatal(err)
	}

	if err := live.Close(); err != nil {
		t.Fatal(err)
	}

	//nolint:gosec // Deliberately mutate a synthetic backup in the private fixture.
	file, err := os.OpenFile(archive, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := file.WriteAt([]byte{'!'}, 512); err != nil {
		_ = file.Close()

		t.Fatal(err)
	}

	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	target := vacantRecoveryProfile(t)
	_, err = Restore(t.Context(), target, archive, false)
	if !errors.Is(err, ErrCorrupt) || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatal(err)
	}

	if _, err := os.Stat(target.WarehousePath); !os.IsNotExist(err) {
		t.Fatal("corrupt snapshot installed", err)
	}
}
