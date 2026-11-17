// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStorageBackupRejectsInvalidSourceState(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  error
	}{
		{"missing metadata", "DROP TABLE main.store_metadata", ErrUnsupportedSchema},
		{"unknown format", "UPDATE main.store_metadata SET version='wake-warehouse/999'", ErrUnsupportedSchema},
		{"extra table", "CREATE TABLE main.unexpected(value TEXT)", ErrUnsupportedSchema},
		{
			"renamed warehouse column",
			"ALTER TABLE main.connections RENAME COLUMN name TO wrong_name",
			ErrUnsupportedSchema,
		},
		{
			"renamed access column",
			"ALTER TABLE sync.configurations RENAME COLUMN settings TO wrong_settings",
			ErrUnsupportedSchema,
		},
		{"missing view", "DROP VIEW main.admin_activity", ErrUnsupportedSchema},
		{
			"altered search index",
			"DROP INDEX activity_publication_search_v1; CREATE INDEX activity_publication_search_v1 ON published_resources(id)",
			ErrUnsupportedSchema,
		},
		{"invalid rule history", "UPDATE main.history_update SET applied=999", ErrHistoryVersion},
		{"missing rule history", "DELETE FROM main.history_update", nil},
		{"missing recovery profile", "DELETE FROM sync.profile", nil},
		{"wrong access store", "UPDATE sync.profile SET access_id='other'", ErrAssociation},
		{"provider differs", "UPDATE sync.configurations SET provider='other'", ErrAssociation},
		{
			"orphaned credentials",
			"PRAGMA foreign_keys=OFF; UPDATE access.credentials SET connection_id='missing'",
			ErrCorrupt,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := t.Context()
			profile := seedUpgradeStores(t)

			store, err := Open(ctx, profile, nil)
			if err != nil {
				t.Fatal(err)
			}

			_, err = store.db.ExecContext(ctx, test.query)
			if err := errors.Join(err, store.Close()); err != nil {
				t.Fatal(err)
			}

			archive := filepath.Join(filepath.Dir(profile.WarehousePath), "invalid.backup")
			if err := Backup(ctx, profile, archive); err == nil || test.want != nil && !errors.Is(err, test.want) {
				t.Fatalf("backup error = %v, want %v", err, test.want)
			}

			if _, err := os.Stat(archive); !os.IsNotExist(err) {
				t.Fatalf("invalid source yielded an archive: %v", err)
			}
		})
	}
}

func TestStorageBackupRefusesPendingMaintenance(t *testing.T) {
	ctx := t.Context()
	profile := seedUpgradeStores(t)
	archive := filepath.Join(filepath.Dir(profile.WarehousePath), "manual.backup")

	for _, test := range []struct {
		name string
		path string
		want error
	}{
		{"rollback journal", profile.WarehousePath + "-journal", ErrUpgradeRecovery},
		{"upgrade intent", profile.WarehousePath + UpgradeIntentSuffix, ErrUpgradeRecovery},
		{"restore intent", profile.WarehousePath + RestoreIntentSuffix, ErrRestorePending},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := os.WriteFile(test.path, []byte("pending"), 0o600); err != nil {
				t.Fatal(err)
			}

			t.Cleanup(func() { _ = os.Remove(test.path) })

			if err := Backup(ctx, profile, archive); !errors.Is(err, test.want) {
				t.Fatalf("backup error = %v, want %v", err, test.want)
			}

			if _, err := os.Stat(archive); !os.IsNotExist(err) {
				t.Fatalf("pending maintenance yielded an archive: %v", err)
			}
		})
	}
}

func TestStorageSnapshotRejectsIncompleteAndUnexpectedRecords(t *testing.T) {
	content := []byte("synthetic snapshot bytes")
	hash := sha256.Sum256(content)
	digest := []byte(hex.EncodeToString(hash[:]))

	for _, test := range []struct {
		name       string
		record     string
		checksum   []byte
		unexpected bool
		cut        int
		want       error
	}{
		{"unknown archive", "warehouse.db", nil, false, 0, ErrBackupScope},
		{"missing checksum", snapshotWarehouseEntry, nil, false, 0, ErrCorrupt},
		{"short checksum", snapshotWarehouseEntry, []byte("short"), false, 0, ErrCorrupt},
		{"extra member", snapshotWarehouseEntry, digest, true, 0, ErrCorrupt},
		{"wrong digest", snapshotWarehouseEntry, bytes.Repeat([]byte{'0'}, len(digest)), false, 0, ErrCorrupt},
		{"truncated warehouse", snapshotWarehouseEntry, digest, false, 512 + 1, io.ErrUnexpectedEOF},
		{"truncated checksum", snapshotWarehouseEntry, digest, false, 3*512 + 1, io.ErrUnexpectedEOF},
		{"complete archive", snapshotWarehouseEntry, digest, false, 0, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			var data bytes.Buffer

			archive := tar.NewWriter(&data)

			if err := writeRecoveryEntry(
				archive,
				test.record,
				int64(len(content)),
				bytes.NewReader(content),
			); err != nil {
				t.Fatal(err)
			}

			if test.checksum != nil {
				if err := writeRecoveryEntry(
					archive,
					snapshotDigestEntry,
					int64(len(test.checksum)),
					bytes.NewReader(test.checksum),
				); err != nil {
					t.Fatal(err)
				}
			}

			if test.unexpected {
				if err := writeRecoveryEntry(archive, "unexpected", 1, strings.NewReader("x")); err != nil {
					t.Fatal(err)
				}
			}

			if err := archive.Close(); err != nil {
				t.Fatal(err)
			}

			if test.cut != 0 {
				data.Truncate(test.cut)
			}

			if err := readWarehouseSnapshot(tar.NewReader(&data), io.Discard); !errors.Is(err, test.want) {
				t.Fatalf("snapshot error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestStorageRestoreRejectsInvalidPairedArchives(t *testing.T) {
	ctx := t.Context()
	dir := filepath.Dir(vacantRecoveryProfile(t).WarehousePath)

	for _, test := range []struct {
		name    string
		entries []string
		want    error
	}{
		{"missing access", backupEntries[:1], ErrBackupScope},
		{"mixed versions", []string{legacyBackupEntries[0], backupEntries[1]}, ErrBackupScope},
		{"extra legacy member", []string{legacyBackupEntries[0], legacyBackupEntries[1], "extra"}, ErrBackupScope},
		{"missing checksums", backupEntries[:], ErrCorrupt},
		{"invalid stores", legacyBackupEntries[:], ErrUnsupportedSchema},
	} {
		t.Run(test.name, func(t *testing.T) {
			var data bytes.Buffer

			archive := tar.NewWriter(&data)
			for _, name := range test.entries {
				if err := writeRecoveryEntry(archive, name, 1, strings.NewReader("x")); err != nil {
					t.Fatal(err)
				}
			}

			if err := archive.Close(); err != nil {
				t.Fatal(err)
			}

			file := filepath.Join(dir, "invalid.tar")
			if err := os.WriteFile(file, data.Bytes(), 0o600); err != nil {
				t.Fatal(err)
			}

			target := vacantRecoveryProfile(t)
			if _, err := Restore(ctx, target, file, true); !errors.Is(err, test.want) {
				t.Fatalf("restore error = %v, want %v", err, test.want)
			}

			for _, path := range target.paths() {
				items, err := os.ReadDir(filepath.Dir(path))
				if err != nil || len(items) != 0 {
					t.Fatalf("failed restore left files in %s: %v, %v", filepath.Dir(path), items, err)
				}
			}
		})
	}
}

func TestStorageOperationsRespectMaintenanceIntents(t *testing.T) {
	ctx := t.Context()
	profile := seedUpgradeStores(t)
	archive := filepath.Join(filepath.Dir(profile.WarehousePath), "manual.backup")
	if err := Backup(ctx, profile, archive); err != nil {
		t.Fatal(err)
	}

	snapshot := filepath.Join(filepath.Dir(archive), "warehouse.tar")
	if err := SnapshotWarehouse(ctx, profile, snapshot); err != nil {
		t.Fatal(err)
	}

	target := vacantRecoveryProfile(t)
	upgradePath := target.WarehousePath + UpgradeIntentSuffix
	if err := writeUpgradeIntent(upgradePath, upgradeIntent{Plan: UpgradePlan{ID: "pending"}}); err != nil {
		t.Fatal(err)
	}

	if _, err := Restore(ctx, target, archive, true); !errors.Is(err, ErrUpgradeRecovery) {
		t.Fatalf("restore during an upgrade: %v", err)
	}

	if err := SnapshotWarehouse(ctx, target, archive); !errors.Is(err, ErrUpgradeRecovery) {
		t.Fatalf("snapshot during an upgrade: %v", err)
	}

	if err := os.Remove(upgradePath); err != nil {
		t.Fatal(err)
	}

	intentPath := target.WarehousePath + RestoreIntentSuffix
	if err := os.WriteFile(intentPath, []byte("unreadable restore intent"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, creds := range []bool{false, true} {
		if _, err := Restore(ctx, target, archive, creds); !errors.Is(err, ErrRestorePending) {
			t.Fatalf("restore with credentials=%t and an unreadable intent: %v", creds, err)
		}
	}

	if err := SnapshotWarehouse(ctx, target, archive); !errors.Is(err, ErrRestorePending) {
		t.Fatalf("snapshot during a restore: %v", err)
	}

	if err := os.Remove(intentPath); err != nil {
		t.Fatal(err)
	}

	guard, err := lockStoreDirectory(target.WarehousePath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = guard.Close() }()

	if store, err := Open(ctx, target, nil); !errors.Is(err, ErrUnsafeStore) {
		if store != nil {
			_ = store.Close()
		}

		t.Fatalf("startup while the creation guard is held: %v", err)
	}

	if _, err := Restore(ctx, target, archive, true); !errors.Is(err, ErrUnsafeStore) {
		t.Fatalf("restore while the creation guard is held: %v", err)
	}

	if _, err := Restore(ctx, target, snapshot, false); !errors.Is(err, ErrUnsafeStore) {
		t.Fatalf("warehouse restore while the creation guard is held: %v", err)
	}

	paths := target.paths()
	if err := checkVacantStores(paths[:]); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(intentPath); !os.IsNotExist(err) {
		t.Fatalf("failed restore published an intent: %v", err)
	}
}

func TestStorageSnapshotRejectsUnusableSource(t *testing.T) {
	ctx := t.Context()
	source := vacantRecoveryProfile(t)
	output := filepath.Join(filepath.Dir(source.WarehousePath), "warehouse.tar")

	if err := SnapshotWarehouse(ctx, source, output); !errors.Is(err, ErrUnsafeStore) {
		t.Fatalf("snapshot from missing warehouse: %v", err)
	}

	if err := os.WriteFile(source.WarehousePath, []byte("not a SQLite database"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := SnapshotWarehouse(ctx, source, output); err == nil {
		t.Fatal("snapshot accepted an invalid warehouse")
	}

	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatalf("invalid source yielded an archive: %v", err)
	}
}

func TestStorageSnapshotRejectsSparseMemberBeforeCopying(t *testing.T) {
	archive := sparseRecoveryArchive(t, []string{snapshotWarehouseEntry}, 0)

	var output bytes.Buffer

	err := readWarehouseSnapshot(tar.NewReader(bytes.NewReader(archive)), &output)
	if !errors.Is(err, ErrBackupScope) || output.Len() != 0 {
		t.Fatalf("sparse snapshot: got error %v after writing %d bytes", err, output.Len())
	}
}

func TestRestoreRejectsNonregularArchiveMembers(t *testing.T) {
	for _, tc := range []struct {
		name   string
		member string
		read   func(*testing.T, *tar.Reader) error
		want   error
	}{
		{"snapshot", snapshotWarehouseEntry, func(_ *testing.T, archive *tar.Reader) error {
			return readWarehouseSnapshot(archive, io.Discard)
		}, ErrBackupScope},
		{"upgrade", backupEntries[0], func(_ *testing.T, archive *tar.Reader) error {
			return readBackupPair(archive)
		}, ErrCorrupt},
		{"installation", installationEntries[0], func(t *testing.T, archive *tar.Reader) error {
			_, err := consumeRestoreArchive(archive, func(int, io.Reader) error {
				t.Fatal("nonregular member was supplied to staging")

				return nil
			})

			return err
		}, ErrCorrupt},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var data bytes.Buffer

			writer := tar.NewWriter(&data)
			if err := writer.WriteHeader(&tar.Header{Name: tc.member, Typeflag: tar.TypeSymlink, Linkname: "other"}); err != nil {
				t.Fatal(err)
			}

			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}

			if err := tc.read(t, tar.NewReader(&data)); !errors.Is(err, tc.want) {
				t.Fatalf("nonregular member error = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestStoragePairedRestoreRejectsSparseMembers(t *testing.T) {
	for i, kind := range storeKinds {
		t.Run(kind, func(t *testing.T) {
			archive := sparseRecoveryArchive(t, backupEntries[:], i)
			target := vacantRecoveryProfile(t)
			file := filepath.Join(t.TempDir(), "sparse.tar")
			if err := os.WriteFile(file, archive, 0o600); err != nil {
				t.Fatal(err)
			}

			if _, err := Restore(t.Context(), target, file, true); !errors.Is(err, ErrBackupScope) {
				t.Fatalf("sparse paired restore: %v", err)
			}

			if err := readBackupPair(tar.NewReader(bytes.NewReader(archive))); !errors.Is(err, ErrCorrupt) {
				t.Fatalf("sparse upgrade archive: %v", err)
			}

			for _, path := range target.paths() {
				items, err := os.ReadDir(filepath.Dir(path))
				if err != nil || len(items) != 0 {
					t.Fatalf("rejected restore left files in %s: %v, %v", filepath.Dir(path), items, err)
				}
			}
		})
	}
}

func sparseRecoveryArchive(t *testing.T, names []string, sparseIndex int) []byte {
	t.Helper()

	var data bytes.Buffer

	archive := tar.NewWriter(&data)

	for i, name := range names {
		entry := tar.Header{Name: name, Typeflag: tar.TypeReg, Size: 1, Mode: 0o600}
		if i == sparseIndex {
			// Go's writer reserves GNU.sparse keys. Write equal-length vendor
			// keys and substitute the prefix in the PAX body after closing.
			entry.PAXRecords = map[string]string{
				"WAK.sparse.map":       "0,1",
				"WAK.sparse.numblocks": "1",
				"WAK.sparse.size":      "2",
			}
		}

		if err := archive.WriteHeader(&entry); err != nil {
			t.Fatal(err)
		}

		if _, err := archive.Write([]byte{'x'}); err != nil {
			t.Fatal(err)
		}
	}

	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}

	return bytes.ReplaceAll(data.Bytes(), []byte("WAK.sparse."), []byte("GNU.sparse."))
}

func TestStorageRestoreValidatesPairedBackupChecksums(t *testing.T) {
	ctx := t.Context()
	source := seedUpgradeStores(t)

	store, err := Open(ctx, source, nil)
	if err != nil {
		t.Fatal(err)
	}

	_, err = store.db.Exec(`INSERT INTO reference_evidence
VALUES ('backup-checksum','now',X'',CAST('original-paired-backup-evidence' AS BLOB),200,X'')`)
	if err := errors.Join(err, store.Close()); err != nil {
		t.Fatal(err)
	}

	dir := filepath.Dir(source.WarehousePath)
	archive := filepath.Join(dir, "paired.tar")
	if err := Backup(ctx, source, archive); err != nil {
		t.Fatal(err)
	}

	//nolint:gosec // Read the synthetic archive produced by this test.
	contents, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}

	original := []byte("original-paired-backup-evidence")
	if bytes.Count(contents, original) != 1 {
		t.Fatal("expected one evidence value in the archive")
	}

	altered := bytes.Replace(contents, original, []byte("modified-paired-backup-evidence"), 1)
	//nolint:gosec // Overwrite only the synthetic archive produced by this test.
	if err := os.WriteFile(archive, altered, 0o600); err != nil {
		t.Fatal(err)
	}

	target := vacantRecoveryProfile(t)
	if _, err := Restore(ctx, target, archive, true); !errors.Is(err, ErrCorrupt) ||
		!strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("restore accepted altered evidence: %v", err)
	}

	for _, path := range target.paths() {
		items, err := os.ReadDir(filepath.Dir(path))
		if err != nil || len(items) != 0 {
			t.Fatalf("failed restore left files in %s: %v, %v", filepath.Dir(path), items, err)
		}
	}
}

func TestInstallationManifestBindsDatabaseMetadata(t *testing.T) {
	ctx := t.Context()
	profile := seedUpgradeStores(t)
	archive := filepath.Join(filepath.Dir(profile.WarehousePath), "installation.tar")
	if err := BackupInstallation(ctx, profile, archive, InstallationEvidence{
		Configuration: []byte("# synthetic configuration\n"),
		LocalAuth:     []byte(`{"version":1,"grants":[]}`),
	}); err != nil {
		t.Fatal(err)
	}

	data := readRestoreFixture(t, archive)
	copied := 0
	manifest, err := consumeRestoreArchive(
		tar.NewReader(bytes.NewReader(data)),
		func(_ int, member io.Reader) error {
			copied++
			_, err := io.Copy(io.Discard, member)

			return err
		},
	)
	if err != nil || manifest == nil || len(manifest.Members) != len(installationEntries) ||
		copied != storeCount {
		t.Fatal(manifest, err, copied)
	}

	for _, tc := range []struct{ before, after string }{
		{`"scope":"installation"`, `"scope":"installati0n"`},
		{`"target_rule":0`, `"target_rule":1`},
		{"# synthetic configuration", "# corrupted configuration"},
	} {
		t.Run(tc.before, func(t *testing.T) {
			if len(tc.before) != len(tc.after) || !bytes.Contains(data, []byte(tc.before)) {
				t.Fatal("fixture lacks expected content")
			}

			changed := bytes.Replace(data, []byte(tc.before), []byte(tc.after), 1)
			path := filepath.Join(filepath.Dir(archive), "altered.tar")
			if err := os.WriteFile(path, changed, 0o600); err != nil {
				t.Fatal(err)
			}

			target := vacantRecoveryProfile(t)
			if _, err := Restore(ctx, target, path, true); !errors.Is(err, ErrCorrupt) {
				t.Fatal("restore admitted inconsistent archive", err)
			}

			if _, err := os.Lstat(target.WarehousePath + RestoreIntentSuffix); !os.IsNotExist(err) {
				t.Fatal("refused archive published a restore intent", err)
			}
		})
	}
}

func TestInstallationArchiveRejectsInvalidMembers(t *testing.T) {
	for _, test := range []struct {
		name    string
		entries []string
		want    error
	}{
		{"empty", nil, ErrBackupScope},
		{"missing database", installationEntries[:1], ErrCorrupt},
		{"wrong database", []string{installationEntries[0], installationEntries[2]}, ErrCorrupt},
		{"missing manifest", installationEntries[:storeCount], ErrCorrupt},
		{"out of order evidence", []string{
			installationEntries[0], installationEntries[1], installationEntries[2],
			installationEntries[4], installationEntries[3], installationManifestEntry,
		}, ErrCorrupt},
	} {
		t.Run(test.name, func(t *testing.T) {
			var data bytes.Buffer

			archive := tar.NewWriter(&data)
			for _, name := range test.entries {
				if err := writeRecoveryEntry(archive, name, 1, strings.NewReader("x")); err != nil {
					t.Fatal(err)
				}
			}

			if err := archive.Close(); err != nil {
				t.Fatal(err)
			}

			_, err := consumeRestoreArchive(tar.NewReader(&data), func(_ int, member io.Reader) error {
				_, err := io.Copy(io.Discard, member)
				return err
			})
			if !errors.Is(err, test.want) {
				t.Fatal(err)
			}
		})
	}
}

func TestInstallationArchiveRejectsOversizedManifest(t *testing.T) {
	var data bytes.Buffer

	writer := tar.NewWriter(&data)
	for _, name := range installationEntries[:storeCount] {
		if err := writeRecoveryEntry(writer, name, 1, strings.NewReader("x")); err != nil {
			t.Fatal(err)
		}
	}

	// The manifest declares an oversized body, which must be rejected before reading.
	if err := writer.WriteHeader(&tar.Header{Name: installationManifestEntry, Size: 16<<10 + 1}); err != nil {
		t.Fatal(err)
	}

	var staged int

	manifest, err := consumeRestoreArchive(tar.NewReader(&data), func(_ int, member io.Reader) error {
		staged++
		_, err := io.Copy(io.Discard, member)

		return err
	})
	if !errors.Is(err, ErrCorrupt) || manifest != nil || staged != storeCount {
		t.Fatalf("oversized manifest = %v, staged %d stores, error = %v", manifest, staged, err)
	}
}

func TestInstallationBackupKeepsEmptyConfiguration(t *testing.T) {
	ctx := t.Context()
	profile := seedUpgradeStores(t)
	path := filepath.Join(filepath.Dir(profile.WarehousePath), "empty-config.tar")
	if err := BackupInstallation(
		ctx,
		profile,
		path,
		InstallationEvidence{Configuration: []byte{}},
	); err != nil {
		t.Fatal(err)
	}

	data := readRestoreFixture(t, path)
	manifest, err := consumeRestoreArchive(
		tar.NewReader(bytes.NewReader(data)),
		func(_ int, member io.Reader) error {
			_, err := io.Copy(io.Discard, member)
			return err
		},
	)
	if err != nil || len(manifest.Members) != storeCount+1 ||
		manifest.Members[storeCount].Name != installationEntries[storeCount] ||
		manifest.Members[storeCount].Size != 0 {
		t.Fatal("empty configuration was not captured", manifest, err)
	}
}

func TestStorageRestoreAcceptsLegacyPairedBackup(t *testing.T) {
	source := seedUpgradeStores(t)

	store, err := Open(t.Context(), source, nil)
	if err != nil {
		t.Fatal(err)
	}

	_, err = store.db.Exec("DROP INDEX activity_publication_search_v1")
	if err := errors.Join(err, store.Close()); err != nil {
		t.Fatal(err)
	}

	target := vacantRecoveryProfile(t)
	if _, err := Restore(t.Context(), target, legacyPairedBackup(t, source), true); err != nil {
		t.Fatal(err)
	}

	assertRestoredPair(t, target)
}

func TestStorageRestoreRejectsLegacySchemaMismatchBeforeInstallation(t *testing.T) {
	source := seedUpgradeStores(t)

	store, err := Open(t.Context(), source, nil)
	if err != nil {
		t.Fatal(err)
	}

	_, err = store.db.Exec("ALTER TABLE main.connections RENAME COLUMN name TO wrong_name")
	if err := errors.Join(err, store.Close()); err != nil {
		t.Fatal(err)
	}

	target := vacantRecoveryProfile(t)
	if _, err := Restore(
		t.Context(),
		target,
		legacyPairedBackup(t, source),
		true,
	); !errors.Is(
		err,
		ErrUnsupportedSchema,
	) {
		t.Fatalf("restore accepted an incompatible schema: %v", err)
	}

	paths := target.paths()
	if err := checkVacantStores(paths[:]); err != nil {
		t.Fatal(err)
	}
}

func legacyPairedBackup(t *testing.T, source Profile) string {
	t.Helper()

	contents := storeBytes(t, source)

	var data bytes.Buffer

	archive := tar.NewWriter(&data)
	for i, name := range legacyBackupEntries {
		if err := writeRecoveryEntry(
			archive,
			name,
			int64(len(contents[i])),
			bytes.NewReader(contents[i]),
		); err != nil {
			t.Fatal(err)
		}
	}

	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(filepath.Dir(source.WarehousePath), "legacy.tar")
	if err := os.WriteFile(path, data.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}
