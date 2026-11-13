// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"archive/tar"
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestRestoreRechecksPlanAndDisplacesOriginalPair(t *testing.T) {
	ctx := t.Context()
	source := seedUpgradeStores(t)

	archive, err := Backup(ctx, source, nil)
	if err != nil {
		t.Fatal(err)
	}

	target := seedUpgradeStores(t)
	plan, err := Restore(ctx, target, archive, "", nil)
	if err != nil || len(plan.Displaced) != 2 {
		t.Fatal(plan, err)
	}

	journal := target.WarehousePath + "-journal"
	if err = os.WriteFile(journal, []byte("old journal"), 0o600); err != nil {
		t.Fatal(err)
	}

	before := storeBytes(t, target)
	if _, err = Restore(ctx, target, archive, plan.ID, nil); !errors.Is(err, ErrRestoreChanged) {
		t.Fatal(err)
	}

	if after := storeBytes(t, target); !bytes.Equal(after[0], before[0]) || !bytes.Equal(after[1], before[1]) {
		t.Fatal("stale confirmation changed the database pair")
	}

	plan, err = Restore(ctx, target, archive, "", nil)
	if err != nil {
		t.Fatal(err)
	}

	result, err := Restore(ctx, target, archive, plan.ID, nil)
	if err != nil {
		t.Fatal(err)
	}

	for i, path := range target.paths() {
		//nolint:gosec // The operation records files in this synthetic profile.
		displaced, e := os.ReadFile(filepath.Join(result.RecoveryDirs[i], "previous", filepath.Base(path)))
		if e != nil || !bytes.Equal(displaced, before[i]) {
			t.Fatal("original database was not preserved", e)
		}
	}

	if _, err = os.Stat(journal); !os.IsNotExist(err) {
		t.Fatal("restored warehouse inherited old journal", err)
	}

	s, err := Open(ctx, target, nil)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = s.Close() }()

	profile, err := s.queries.Profile(ctx)
	if err != nil || profile.Paused != 1 {
		t.Fatal(profile, err)
	}
}

func TestRestoreResumesOnceAcrossInstallAndCleanup(t *testing.T) {
	for _, stop := range []string{"recorded", "one-installed", "cleanup"} {
		t.Run(stop, func(t *testing.T) {
			ctx := t.Context()
			source, target := seedUpgradeStores(t), seedUpgradeStores(t)

			archive, err := Backup(ctx, source, nil)
			if err != nil {
				t.Fatal(err)
			}

			plan, err := Restore(ctx, target, archive, "", nil)
			if err != nil {
				t.Fatal(err)
			}

			intent := testRestoreIntent(t, target, plan)
			initialID := readRestoredID(t, ctx, intent.Staged[0].Path)
			if stop != "recorded" {
				simulateRestoreProgress(t, intent, stop == "cleanup")
			}

			if _, err = Open(ctx, target, nil); !errors.Is(err, ErrRestorePending) {
				t.Fatal("startup accepted pending restore", err)
			}

			if stop == "one-installed" {
				shared := Profile{WarehousePath: source.WarehousePath, ConnectionsPath: target.ConnectionsPath}
				if _, err = Open(ctx, shared, nil); !errors.Is(err, ErrRestorePending) {
					t.Fatal("startup ignored shared access guard", err)
				}
			}

			pending, err := Restore(ctx, target, archive, "", nil)
			if err != nil || !pending.Pending || pending.ID != plan.ID {
				t.Fatal(pending, err)
			}

			if _, err = Restore(ctx, target, archive, plan.ID, nil); err != nil {
				t.Fatal(err)
			}

			if got := readRestoredID(t, ctx, target.WarehousePath); got != initialID {
				t.Fatal("retry rotated the staged warehouse ID", got)
			}

			if err = CheckRestorePending(target.paths()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func testRestoreIntent(t *testing.T, profile Profile, plan RestorePlan) restoreIntent {
	t.Helper()
	ctx := t.Context()
	intent := restoreIntent{Plan: plan}

	var err error

	intent.Originals, err = restoreInventory(ctx, profile.paths(), nil)
	if err != nil {
		t.Fatal(err)
	}

	var paths [2]string
	for i, path := range profile.paths() {
		parent, e := os.Stat(filepath.Dir(path))
		if e != nil {
			t.Fatal(e)
		}

		intent.Parents[i] = restoreIdentity(parent)

		dir, e := os.MkdirTemp(filepath.Dir(path), ".wake-restore-")
		if e != nil {
			t.Fatal(e)
		}

		if e = os.Mkdir(filepath.Join(dir, "previous"), 0o700); e != nil {
			t.Fatal(e)
		}

		intent.Plan.RecoveryDirs[i] = dir
		paths[i] = filepath.Join(dir, "snapshot.db")
	}

	if _, _, err = unpackBackup(ctx, plan.Archive, paths); err != nil {
		t.Fatal(err)
	}

	if err = pauseRestoredWarehouse(ctx, paths[0]); err != nil {
		t.Fatal(err)
	}

	for i, path := range paths {
		intent.Staged[i], err = inspectRestoreFile(ctx, path, i)
		if err != nil {
			t.Fatal(err)
		}
	}

	if err = writePrivateRecord(profile.WarehousePath+restoreSuffix, intent); err != nil {
		t.Fatal(err)
	}

	return intent
}

func simulateRestoreProgress(t *testing.T, intent restoreIntent, complete bool) {
	t.Helper()

	ctx := t.Context()
	paths := intent.Plan.Stores
	if err := writePrivateRecord(paths[1]+restoreSuffix, intent.Plan.ID); err != nil {
		t.Fatal(err)
	}

	for _, old := range intent.Originals {
		if err := moveRestoreFile(ctx, old, displacedPath(intent, old)); err != nil {
			t.Fatal(err)
		}
	}

	if err := moveRestoreFile(ctx, intent.Staged[0], paths[0]); err != nil {
		t.Fatal(err)
	}

	if complete {
		if err := moveRestoreFile(ctx, intent.Staged[1], paths[1]); err != nil {
			t.Fatal(err)
		}

		intent.Committed = true
		if err := writePrivateRecord(paths[0]+restoreSuffix, intent); err != nil {
			t.Fatal(err)
		}

		if err := os.Remove(paths[1] + restoreSuffix); err != nil {
			t.Fatal(err)
		}
	}
}

func readRestoredID(t *testing.T, ctx context.Context, path string) string {
	t.Helper()

	db := sql.OpenDB(&connector{paths: [2]string{path}, probe: true})
	defer func() { _ = db.Close() }()

	var id string
	if err := db.QueryRowContext(ctx, profileStatement("Metadata", "main")).
		Scan(new(string), new(string), &id); err != nil {
		t.Fatal(err)
	}

	return id
}

func TestRestoreLegacyArchiveAndRejectsTruncation(t *testing.T) {
	ctx := t.Context()
	source := seedUpgradeStores(t)
	archive := filepath.Join(filepath.Dir(source.WarehousePath), "legacy.tar")

	var data bytes.Buffer

	tarWriter := tar.NewWriter(&data)
	for i, raw := range storeBytes(t, source) {
		if err := tarWriter.WriteHeader(
			&tar.Header{Name: backupEntries[i], Size: int64(len(raw)), Mode: 0o600},
		); err != nil {
			t.Fatal(err)
		}

		if _, err := io.Copy(tarWriter, bytes.NewReader(raw)); err != nil {
			t.Fatal(err)
		}
	}

	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(archive, data.Bytes()[:600], 0o600); err != nil {
		t.Fatal(err)
	}

	target := seedUpgradeStores(t)
	if _, err := Restore(ctx, target, archive, "", nil); err == nil {
		t.Fatal("accepted truncated archive")
	}

	if err := os.WriteFile(archive, data.Bytes()[:data.Len()-512], 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Restore(ctx, target, archive, "", nil); !errors.Is(err, ErrCorrupt) {
		t.Fatal("accepted a truncated tar trailer", err)
	}

	if err := os.WriteFile(archive, data.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	plan, err := Restore(ctx, target, archive, "", nil)
	if err != nil {
		t.Fatal(err)
	}

	if _, err = Restore(ctx, target, archive, plan.ID, nil); err != nil {
		t.Fatal(err)
	}
}

func TestRestoreRejectsInvalidPairs(t *testing.T) {
	ctx := t.Context()
	source, other := seedUpgradeStores(t), seedUpgradeStores(t)

	target := seedUpgradeStores(t)
	before := storeBytes(t, target)
	good := storeBytes(t, source)
	for _, tc := range []struct {
		name   string
		access []byte
		want   error
	}{
		{"mixed", storeBytes(t, other)[1], ErrAssociation},
		{"damaged", []byte("invalid sqlite"), ErrUnsupportedSchema},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "invalid.tar")
			writeLegacyPair(t, path, [2][]byte{good[0], tc.access})

			if _, err := Restore(ctx, target, path, "", nil); !errors.Is(err, tc.want) {
				t.Fatal(err)
			}

			after := storeBytes(t, target)
			if !bytes.Equal(after[0], before[0]) || !bytes.Equal(after[1], before[1]) {
				t.Fatal("invalid archive changed the existing pair")
			}
		})
	}
}

func writeLegacyPair(t *testing.T, path string, pair [2][]byte) {
	t.Helper()

	var data bytes.Buffer

	w := tar.NewWriter(&data)
	for i, file := range pair {
		if err := w.WriteHeader(
			&tar.Header{Name: backupEntries[i], Size: int64(len(file)), Mode: 0o600},
		); err != nil {
			t.Fatal(err)
		}

		if _, err := w.Write(file); err != nil {
			t.Fatal(err)
		}
	}

	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, data.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}
