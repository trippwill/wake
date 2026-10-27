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

	if s, err := Open(context.Background(), path); !errors.Is(err, ErrUnsupportedSchema) {
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
	if err != nil || len(entries) != 1 {
		t.Fatal("preflight created sidecars", entries, err)
	}
}

func TestPrivateStoreOwnershipAndRestart(t *testing.T) {
	dir := t.TempDir()
	//nolint:gosec // Private test directory or synthetic database.
	_ = os.Chmod(dir, 0o700)
	path := filepath.Join(dir, "wake.db")

	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}

	if other, e := Open(context.Background(), path); !errors.Is(e, ErrUnsafeStore) {
		if other != nil {
			_ = other.Close()
		}

		t.Fatal("competing owner", e)
	}

	if err = s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}

	_ = s.Close()

	alias := filepath.Join(dir, "alias.db")
	if err = os.Link(path, alias); err != nil {
		t.Fatal(err)
	}

	if _, err = Open(context.Background(), alias); !errors.Is(err, ErrUnsafeStore) {
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
		filepath.Join(target, "wake.db"),
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
