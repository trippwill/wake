// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestUnsupportedStoreIsUntouched(t *testing.T) {
	dir := t.TempDir()
	//nolint:gosec // Private test directory or synthetic database.
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(dir, "wake.db")

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}

	if _, err = db.Exec(
		"CREATE TABLE old_financial_data(value TEXT); INSERT INTO old_financial_data VALUES ('preserved')",
	); err != nil {
		t.Fatal(err)
	}

	_ = db.Close()
	//nolint:gosec // Private test directory or synthetic database.
	if err = os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	//nolint:gosec // Read only the synthetic test database.
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if s, err := Open(
		context.Background(),
		testProfile(path),
		nil,
	); !errors.Is(
		err,
		ErrUnsupportedSchema,
	) {
		if s != nil {
			_ = s.Close()
		}

		t.Fatal(err)
	}
	//nolint:gosec // Read only the synthetic test database.
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("unsupported database changed", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}

	for _, entry := range entries {
		if entry.Name() != "wake.db" && entry.Name() != "wake.db"+storeLockSuffix &&
			entry.Name() != "connections.db"+storeLockSuffix {
			t.Fatal("preflight created a database or journal", entry.Name())
		}
	}
}

func TestPreviousWarehouseFormatPreservesStores(t *testing.T) {
	s, path := testStore(t)
	if _, err := s.db.Exec(
		`UPDATE store_metadata SET version='wake-connection-accounts-1' WHERE singleton=1`,
	); err != nil {
		t.Fatal(err)
	}

	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	profile := testProfile(path)

	directory, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = directory.Close() }()

	before := map[string][]byte{}
	for _, file := range profile.paths() {
		data, err := directory.ReadFile(filepath.Base(file))
		if err != nil {
			t.Fatal(err)
		}

		before[file] = data
	}

	next, err := Open(t.Context(), profile, nil)
	if next != nil {
		_ = next.Close()
	}

	if !errors.Is(err, ErrUnsupportedSchema) {
		t.Fatal("previous warehouse format did not fail closed", err)
	}

	for file, data := range before {
		after, err := directory.ReadFile(filepath.Base(file))
		if err != nil || !bytes.Equal(data, after) {
			t.Fatal("previous warehouse format changed a retained store", file, err)
		}
	}
}

func TestPrivateStoreOwnershipAndRestart(t *testing.T) {
	dir := t.TempDir()
	//nolint:gosec // Private test directory or synthetic database.
	_ = os.Chmod(dir, 0o700)
	path := filepath.Join(dir, "wake.db")

	s, err := Open(
		context.Background(),
		testProfile(path),
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}

	if other, e := Open(
		context.Background(),
		testProfile(path),
		nil,
	); !errors.Is(
		e,
		ErrUnsafeStore,
	) {
		if other != nil {
			_ = other.Close()
		}

		t.Fatal("competing owner", e)
	}

	if err = s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = Open(
		context.Background(),
		testProfile(path),
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}

	_ = s.Close()

	alias := filepath.Join(dir, "alias.db")
	if err = os.Link(path, alias); err != nil {
		t.Fatal(err)
	}

	if _, err = Open(
		context.Background(),
		testProfile(alias),
		nil,
	); !errors.Is(
		err,
		ErrUnsafeStore,
	) {
		t.Fatal("hardlink accepted", err)
	}
}

//nolint:gosec // Copy only synthetic SQLite files between private test directories.
func TestUnsupportedWALStorePreflightPreservesAllFiles(t *testing.T) {
	sourceDir := t.TempDir()
	path := filepath.Join(sourceDir, "wake.db")

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = db.Close() }()

	if _, err = db.Exec(
		"PRAGMA journal_mode=WAL; PRAGMA wal_autocheckpoint=0; CREATE TABLE old_financial_data(value TEXT); INSERT INTO old_financial_data VALUES ('preserved')",
	); err != nil {
		t.Fatal(err)
	}

	target := t.TempDir()
	if err = os.Chmod(target, 0o700); err != nil {
		t.Fatal(err)
	}

	snapshot := map[string][]byte{}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		data, e := os.ReadFile(path + suffix)
		if e != nil {
			t.Fatal(e)
		}

		snapshot[suffix] = data
		if e = os.WriteFile(filepath.Join(target, "wake.db")+suffix, data, 0o600); e != nil {
			t.Fatal(e)
		}
	}

	if s, e := Open(
		context.Background(),
		testProfile(filepath.Join(target, "wake.db")), nil,
	); !errors.Is(
		e,
		ErrUnsupportedSchema,
	) {
		if s != nil {
			_ = s.Close()
		}

		t.Fatal("retired WAL format opened", e)
	}

	for suffix, before := range snapshot {
		after, e := os.ReadFile(filepath.Join(target, "wake.db") + suffix)
		if e != nil || !bytes.Equal(before, after) {
			t.Fatal("preflight modified unsupported WAL files", suffix, e)
		}
	}
}

func TestV1StoreFormatsRejectDevelopmentLabelsWithoutWrites(t *testing.T) {
	for _, fixture := range []struct {
		name, update, legacy string
	}{
		{name: "warehouse", update: "UPDATE main.store_metadata SET version=?", legacy: "wake-financial-variants-2"},
		{name: "access", update: "UPDATE access.store_metadata SET version=?", legacy: "wake-simplification-4"},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			ctx := t.Context()
			profile, store := profileFixture(t)
			for schema, expected := range map[string]string{"main": "wake-warehouse/1", "access": "wake-access/1"} {
				var metadata storeMetadata
				if err := store.db.QueryRowContext(ctx, profileStatement("Metadata", schema)).
					Scan(&metadata.kind, &metadata.version, &metadata.id); err != nil ||
					metadata.version != expected {
					t.Fatal(schema, metadata.version, err)
				}
			}

			if _, err := store.db.ExecContext(
				ctx,
				fixture.update,
				fixture.legacy,
			); err != nil {
				t.Fatal(err)
			}

			if err := store.Close(); err != nil {
				t.Fatal(err)
			}

			baseline := [2][]byte{}
			for i, path := range profile.paths() {
				var err error

				baseline[i], err = os.ReadFile(filepath.Clean(path))
				if err != nil {
					t.Fatal(err)
				}
			}

			reopened, err := Open(ctx, profile, nil)
			if reopened != nil {
				_ = reopened.Close()
			}

			if !errors.Is(err, ErrUnsupportedSchema) {
				t.Fatal("opening a development format", err)
			}

			for i, path := range profile.paths() {
				data, err := os.ReadFile(filepath.Clean(path))
				if err != nil || !bytes.Equal(data, baseline[i]) {
					t.Fatal("store changed during preflight", path, err)
				}
			}
		})
	}
}
