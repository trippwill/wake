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
	"time"

	"go.trippwill.dev/wake/internal/application"
)

func TestStorageSnapshotCopiesOnlyCommittedWarehouse(t *testing.T) {
	ctx := t.Context()
	source := seedUpgradeStores(t)

	live, err := Open(ctx, source, nil)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = live.Close() }()

	// Keep the fixture large enough to exercise more than one backup step.
	if _, err := live.db.Exec(
		`INSERT INTO reference_evidence VALUES('large','now',X'',zeroblob(2097152),200,X'')`,
	); err != nil {
		t.Fatal(err)
	}

	var lineage string
	if err := live.db.QueryRowContext(ctx, profileStatement("FinancialLineage", "")).
		Scan(&lineage); err != nil {
		t.Fatal(err)
	}

	positionsBefore, err := live.PositionsPublications(ctx)
	if err != nil || len(positionsBefore) != 1 {
		t.Fatal(positionsBefore, err)
	}

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

	restored, err := OpenFinancial(ctx, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = restored.Close() }()

	after, err := snapshotState(ctx, restored)
	if err != nil || after.BaselineID == before.BaselineID ||
		!reflect.DeepEqual(after.Resources, before.Resources) {
		t.Fatal("restored history or publication identity differs", err)
	}

	var uncommitted int
	if err = restored.db.QueryRow(`SELECT count(*) FROM connections WHERE id='uncommitted'`).
		Scan(&uncommitted); err != nil ||
		uncommitted != 0 {
		t.Fatal("snapshot included an uncommitted source", uncommitted, err)
	}

	var restoredLineage string
	if err = restored.db.QueryRowContext(ctx, profileStatement("FinancialLineage", "")).
		Scan(&restoredLineage); err != nil ||
		restoredLineage != lineage {
		t.Fatal("snapshot lost financial lineage", restoredLineage, err)
	}

	conditional, err := restored.Snapshot(ctx, application.MatchingSnapshotVersions(before.Version))
	if err != nil || conditional.Unchanged {
		t.Fatal("restored snapshot reused its consumer baseline", err)
	}

	positionsAfter, err := restored.PositionsPublications(ctx)
	if err != nil || len(positionsAfter) != 1 || positionsAfter[0].AccountID != positionsBefore[0].AccountID ||
		positionsAfter[0].BaselineID != after.BaselineID || positionsAfter[0].Version == positionsBefore[0].Version {
		t.Fatal("unexpected restored positions baseline", positionsAfter, err)
	}

	targetPaths := target.paths()
	for _, path := range targetPaths[1:] {
		if _, err = os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("restore or startup created an optional store", path, err)
		}
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

func TestStorageSnapshotRestoreCannotActivateWhileDirectoryOwned(t *testing.T) {
	ctx := t.Context()
	source := seedUpgradeStores(t)
	archive := filepath.Join(filepath.Dir(source.WarehousePath), "history.tar")
	if err := SnapshotWarehouse(ctx, source, archive); err != nil {
		t.Fatal(err)
	}

	target := vacantRecoveryProfile(t)

	guard, err := lockStoreDirectory(target.WarehousePath)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = guard.Close() }()

	if _, err = Restore(ctx, target, archive, false); !errors.Is(err, ErrUnsafeStore) {
		t.Fatal("restore activated while the directory was owned", err)
	}

	entries, err := os.ReadDir(filepath.Dir(target.WarehousePath))
	if err != nil || len(entries) != 0 {
		t.Fatal("failed restore left warehouse or staging files", entries, err)
	}

	if err = guard.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err = Restore(ctx, target, archive, false); err != nil {
		t.Fatal("restore could not be retried", err)
	}
}

func TestStorageSnapshotLeavesNewerOptionalStoresUntouched(t *testing.T) {
	ctx := t.Context()
	profile, live := profileFixture(t)
	initial, witness := upstreamTokens(t, live)
	if initial != witness {
		t.Fatal("initial witnesses disagree")
	}

	archive := filepath.Join(t.TempDir(), "history.tar")
	if err := SnapshotWarehouse(ctx, profile, archive); err != nil {
		t.Fatal(err)
	}

	if err := live.Delay(ctx, "ibkr-flex", "requests", "scope", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	newer, witness := upstreamTokens(t, live)
	if newer == initial || newer != witness {
		t.Fatal("access witness did not advance")
	}

	if err := live.Close(); err != nil {
		t.Fatal(err)
	}

	before := storeBytes(t, profile)

	// Preserve the current warehouse to make a vacant destination.
	//nolint:gosec // Both paths are in the synthetic profile's t.TempDir.
	if err := os.Rename(profile.WarehousePath, profile.WarehousePath+".original"); err != nil {
		t.Fatal(err)
	}

	if _, err := Restore(ctx, profile, archive, false); err != nil {
		t.Fatal(err)
	}

	restored, err := OpenFinancial(ctx, profile, nil)
	if err != nil {
		t.Fatal("restored warehouse could not open with newer optional stores", err)
	}

	if _, err = restored.Snapshot(ctx, application.AnySnapshotVersion()); err != nil {
		t.Fatal(err)
	}

	if err = restored.Close(); err != nil {
		t.Fatal(err)
	}

	after := storeBytes(t, profile)
	for i := 1; i < len(before); i++ {
		if !reflect.DeepEqual(before[i], after[i]) {
			t.Fatal("restore changed optional store", storeKinds[i])
		}
	}
}
