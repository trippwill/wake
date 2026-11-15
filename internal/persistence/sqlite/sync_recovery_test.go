// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"go.trippwill.dev/wake/provider"
)

func TestRecoverSynchronization(t *testing.T) {
	ctx := t.Context()
	profile := seedUpgradeStores(t)
	paths := profile.paths()
	warehouse := readRestoreFixture(t, paths[0])
	access := readRestoreFixture(t, paths[1])

	if err := os.Remove(paths[2]); err != nil {
		t.Fatal(err)
	}

	journal := paths[2] + "-journal"
	if err := os.WriteFile(journal, []byte("interrupted transaction"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := RecoverSynchronization(ctx, profile, "", ""); !errors.Is(err, ErrRestoreOccupied) {
		t.Fatal("reset accepted an orphaned journal", err)
	}

	if err := os.Remove(journal); err != nil {
		t.Fatal(err)
	}

	preview, err := RecoverSynchronization(ctx, profile, "", "")
	if err != nil || preview.ID == "" || !preview.RetainAccess || preview.Applied {
		t.Fatal(preview, err)
	}

	if _, err := os.Lstat(paths[2]); !os.IsNotExist(err) {
		t.Fatal("preview created optional storage", err)
	}

	stale := []byte("stale or damaged sync database")
	if err := os.WriteFile(paths[2], stale, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := RecoverSynchronization(ctx, profile, "", preview.ID); !errors.Is(err, ErrSyncRecoveryPlan) {
		t.Fatal("stale preview authorized replacement", err)
	}

	preview, err = RecoverSynchronization(ctx, profile, "", "")
	if err != nil {
		t.Fatal(err)
	}

	applied, err := RecoverSynchronization(ctx, profile, "", preview.ID)
	if err != nil || !applied.Applied || applied.Pending {
		t.Fatal(applied, err)
	}

	if !bytes.Equal(warehouse, readRestoreFixture(t, paths[0])) {
		t.Fatal("sync recovery changed the financial database")
	}

	for i, expected := range [][]byte{access, stale} {
		saved, err := filepath.Glob(filepath.Join(filepath.Dir(paths[i+1]), ".wake-original-*-"+storeKinds[i+1]))
		if err != nil || len(saved) != 1 || !bytes.Equal(expected, readRestoreFixture(t, saved[0])) {
			t.Fatal("original optional store not preserved", saved, err)
		}
	}

	store, err := Open(ctx, profile, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	if _, err := store.UpstreamRevision(ctx); !errors.Is(err, provider.ErrUnavailable) {
		t.Fatal("recovered association has an active upstream grant", err)
	}

	var bindings, continuations int
	if err := store.db.QueryRow(`SELECT count(*) FROM access.credentials
		WHERE connection_id='synthetic' AND encrypted=X'010203'`).Scan(&bindings); err != nil || bindings != 1 {
		t.Fatal("credential identity or ciphertext changed", bindings, err)
	}

	if err := store.db.QueryRow("SELECT count(*) FROM sync.work").
		Scan(&continuations); err != nil ||
		continuations != 0 {
		t.Fatal("old provider work survived recovery", continuations, err)
	}
}

func TestRecoverSynchronizationImportsOnlyReviewedConfiguration(t *testing.T) {
	ctx := t.Context()
	profile := seedUpgradeStores(t)
	paths := profile.paths()
	configure := func(queryID string) {
		t.Helper()

		store, err := Open(ctx, profile, nil)
		if err != nil {
			t.Fatal(err)
		}

		_, err = store.db.Exec(
			"UPDATE sync.configurations SET settings=? WHERE id='synthetic'",
			[]byte(`{"query_id":"`+queryID+`"}`),
		)
		if err := errors.Join(err, store.Close()); err != nil {
			t.Fatal(err)
		}
	}
	configure("123")

	archive := filepath.Join(filepath.Dir(paths[0]), "settings.tar")
	if err := Backup(ctx, profile, archive); err != nil {
		t.Fatal(err)
	}

	configure("456")

	if _, err := RecoverSynchronization(
		ctx,
		seedUpgradeStores(t),
		archive,
		"",
	); !errors.Is(
		err,
		ErrAssociation,
	) {
		t.Fatal("accepted another financial lineage", err)
	}

	p, err := RecoverSynchronization(ctx, profile, archive, "")
	if err != nil || len(p.Current) != 1 || len(p.Proposed) != 1 ||
		!bytes.Contains(
			p.Current[0].Settings,
			[]byte("456"),
		) || !bytes.Contains(p.Proposed[0].Settings, []byte("123")) {
		t.Fatal("missing configuration differences", p, err)
	}

	if _, err := RecoverSynchronization(ctx, profile, "", p.ID); !errors.Is(err, ErrSyncRecoveryPlan) {
		t.Fatal("confirmation reused after dropping the import", err)
	}

	if _, err := RecoverSynchronization(ctx, profile, archive, p.ID); err != nil {
		t.Fatal(err)
	}

	store, err := Open(ctx, profile, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	connection, err := store.Connection(ctx, "synthetic")
	query, _ := connection.Settings.Get("query_id")
	if err != nil || query != "123" {
		t.Fatal("reviewed settings not imported", err)
	}

	if _, err := store.UpstreamRevision(ctx); !errors.Is(err, provider.ErrUnavailable) {
		t.Fatal("configuration import admitted network work", err)
	}
}
