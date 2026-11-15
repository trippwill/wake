// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"go.trippwill.dev/wake/internal/application"
)

func TestFinancialOpenIgnoresOptionalAccessFile(t *testing.T) {
	ctx := t.Context()
	dir := filepath.Join(t.TempDir(), "data")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	profile := Profile{
		WarehousePath:   filepath.Join(dir, "wake.db"),
		ConnectionsPath: filepath.Join(dir, "connections.db"),
	}

	fresh, err := OpenFinancial(ctx, profile, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fresh.Close() }()

	if _, err = os.Stat(profile.ConnectionsPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("created optional access", err)
	}

	if _, err = fresh.db.ExecContext(
		ctx,
		`INSERT INTO connections(id,provider) VALUES('local','synthetic')`,
	); err != nil {
		t.Fatal(err)
	}

	if err = fresh.Close(); err != nil {
		t.Fatal(err)
	}

	if err = os.WriteFile(
		profile.ConnectionsPath,
		[]byte("synthetic corrupt optional file"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	store, err := OpenFinancial(ctx, profile, nil)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = store.Close() }()

	if view, e := store.Overview(
		ctx,
	); e != nil || len(view.Status.Connections) != 1 ||
		!view.Status.Connections[0].UpstreamUnavailable {
		t.Fatal("local overview consulted optional access or lost retained source", e)
	}

	if _, err = store.Snapshot(ctx, application.AnySnapshotVersion()); err != nil {
		t.Fatal("local API snapshot", err)
	}

	actual, err := os.ReadFile(profile.ConnectionsPath)
	if err != nil || string(actual) != "synthetic corrupt optional file" {
		t.Fatal("opened or changed the optional file", err)
	}
}

func TestFinancialOpenRefusesUnresolvedJournal(t *testing.T) {
	ctx := t.Context()
	profile, store := profileFixture(t)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	journal := profile.WarehousePath + "-journal"
	//nolint:gosec // Journal belongs to a disposable profile in t.TempDir.
	if err := os.WriteFile(journal, []byte("synthetic unresolved journal"), 0o600); err != nil {
		t.Fatal(err)
	}

	if opened, err := OpenFinancial(ctx, profile, nil); opened != nil || !errors.Is(err, ErrUpgradeRecovery) {
		t.Fatal("opened warehouse without original recovery participants", err)
	}

	//nolint:gosec // Journal belongs to the private fixture above.
	actual, err := os.ReadFile(journal)
	if err != nil || string(actual) != "synthetic unresolved journal" {
		t.Fatal("journal was modified", err)
	}
}
