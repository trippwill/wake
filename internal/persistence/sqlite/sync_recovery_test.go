// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
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

func TestSynchronizationRecoveryResumesAfterProcessExit(t *testing.T) {
	ctx := t.Context()

	profile := seedUpgradeStores(t)
	paths := profile.paths()
	financial := readRestoreFixture(t, paths[0])
	if err := os.Remove(paths[2]); err != nil {
		t.Fatal(err)
	}

	preview, err := RecoverSynchronization(ctx, profile, "", "")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := planSyncRecovery(ctx, paths, paths[0]+RestoreIntentSuffix, "", preview.ID); err != nil {
		t.Fatal(err)
	}

	//nolint:gosec // Re-execute this test binary against its disposable restore intent.
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestInterruptedInstallationChild$")
	child.Dir = filepath.Dir(paths[0])

	child.Env = append(os.Environ(), "WAKE_TEST_INTERRUPTED_RESTORE=1")

	output, err := child.CombinedOutput()
	if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 39 {
		t.Fatalf("interrupt recovery: %v\n%s", err, output)
	}

	if _, err := OpenFinancial(ctx, profile, nil); !errors.Is(err, ErrRestorePending) {
		t.Fatal("partially replaced store was visible", err)
	}

	if pending, err := RecoverSynchronization(
		ctx,
		profile,
		"",
		"",
	); err != nil || pending.ID != preview.ID ||
		!pending.Pending {
		t.Fatal("durable plan could not be inspected", pending, err)
	}

	different := profile
	different.SyncPath += ".different"
	if _, err := RecoverSynchronization(ctx, different, "", preview.ID); !errors.Is(err, ErrRestorePending) {
		t.Fatal("pending plan accepted a changed store path", err)
	}

	if _, err := RecoverSynchronization(
		ctx,
		profile,
		"",
		"another-plan",
	); !errors.Is(
		err,
		ErrSyncRecoveryPlan,
	) {
		t.Fatal("pending plan accepted a different confirmation", err)
	}

	altered := append(bytes.Clone(financial), 0)
	if err := os.WriteFile(paths[0], altered, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := RecoverSynchronization(ctx, profile, "", preview.ID); !errors.Is(err, ErrRestorePending) {
		t.Fatal("pending plan accepted a changed warehouse", err)
	}

	if _, err := OpenFinancial(ctx, profile, nil); !errors.Is(err, ErrRestorePending) {
		t.Fatal("rejected retry cleared the startup guard", err)
	}

	if err := os.WriteFile(paths[0], financial, 0o600); err != nil {
		t.Fatal(err)
	}

	if applied, err := RecoverSynchronization(ctx, profile, "", preview.ID); err != nil || !applied.Applied {
		t.Fatal("same plan did not resume", applied, err)
	}

	if !bytes.Equal(financial, readRestoreFixture(t, paths[0])) {
		t.Fatal("sync-only recovery rewrote the warehouse")
	}
}

func TestSynchronizationRecoveryRefusesInvalidRequiredState(t *testing.T) {
	for _, role := range []int{0, 1} {
		t.Run([]string{"warehouse", "access"}[role], func(t *testing.T) {
			profile := seedUpgradeStores(t)
			if err := os.WriteFile(profile.paths()[role], []byte("not a database"), 0o600); err != nil {
				t.Fatal(err)
			}

			if _, err := RecoverSynchronization(t.Context(), profile, "", ""); err == nil {
				t.Fatal("accepted damaged required state")
			}
		})
	}

	profile := seedUpgradeStores(t)
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()

	if _, err := RecoverSynchronization(cancelled, profile, "", ""); !errors.Is(err, context.Canceled) {
		t.Fatal("accepted cancelled recovery", err)
	}

	intent := profile.WarehousePath + RestoreIntentSuffix
	if err := os.WriteFile(intent, []byte("invalid restore metadata"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := RecoverSynchronization(t.Context(), profile, "", ""); !errors.Is(err, ErrRestorePending) {
		t.Fatal("accepted unrecognized maintenance intent", err)
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

func TestSynchronizationRecoveryRefusesUnrelatedMaintenance(t *testing.T) {
	fixtures := []struct {
		name    string
		prepare func(*testing.T, Profile)
		want    error
	}{
		{"overlapping stores", nil, ErrUnsafeStore},
		{"missing warehouse", func(t *testing.T, profile Profile) {
			if err := os.Remove(profile.WarehousePath); err != nil {
				t.Fatal(err)
			}
		}, os.ErrNotExist},
		{"pending upgrade", func(t *testing.T, profile Profile) {
			if err := os.WriteFile(
				profile.WarehousePath+UpgradeIntentSuffix,
				[]byte("pending"),
				0o600,
			); err != nil {
				t.Fatal(err)
			}
		}, ErrUpgradeRecovery},
		{"pending installation restore", func(t *testing.T, profile Profile) {
			intent := restoreIntent{ArchiveDigest: digest([]byte("other installation"))}
			if _, err := publishRestoreIntent(profile.WarehousePath+RestoreIntentSuffix, intent); err != nil {
				t.Fatal(err)
			}
		}, ErrRestorePending},
	}
	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			profile := seedUpgradeStores(t)
			if fixture.prepare == nil {
				profile.SyncPath = profile.ConnectionsPath
			} else {
				fixture.prepare(t, profile)
			}

			if _, err := RecoverSynchronization(t.Context(), profile, "", ""); !errors.Is(err, fixture.want) {
				t.Fatal("unsafe recovery preview succeeded", err)
			}
		})
	}
}

func TestSynchronizationRecoveryRejectsObsoleteOrInvalidArchivedSettings(t *testing.T) {
	ctx := t.Context()
	profile := seedUpgradeStores(t)
	change := func(query string) {
		t.Helper()

		store, err := OpenInstalled(ctx, profile, nil)
		if err != nil {
			t.Fatal(err)
		}

		_, err = store.db.Exec(query)
		if err := errors.Join(err, store.Close()); err != nil {
			t.Fatal(err)
		}
	}

	change(`INSERT INTO main.connections(id,provider,name) VALUES('obsolete','ibkr-flex','Obsolete source');
	INSERT INTO sync.configurations(id,provider,settings) VALUES('obsolete','ibkr-flex',X'7b7d');`)

	dir := filepath.Dir(profile.WarehousePath)
	obsolete := filepath.Join(dir, "obsolete.tar")
	if err := Backup(ctx, profile, obsolete); err != nil {
		t.Fatal(err)
	}

	change(
		`DELETE FROM sync.configurations WHERE id='obsolete'; DELETE FROM main.connections WHERE id='obsolete';`,
	)

	if _, err := RecoverSynchronization(ctx, profile, obsolete, ""); !errors.Is(err, ErrAssociation) {
		t.Fatal("import recreated an obsolete source", err)
	}

	change(`UPDATE sync.configurations SET settings=X'5b5d' WHERE id='synthetic';`)

	malformed := filepath.Join(dir, "invalid-settings.tar")
	if err := Backup(ctx, profile, malformed); err != nil {
		t.Fatal(err)
	}

	if _, err := RecoverSynchronization(ctx, profile, malformed, ""); !errors.Is(err, ErrCorrupt) {
		t.Fatal("malformed settings produced an approvable plan", err)
	}

	warehouse := filepath.Join(dir, "warehouse-only.tar")
	if err := SnapshotWarehouse(ctx, profile, warehouse); err != nil {
		t.Fatal(err)
	}

	if _, err := RecoverSynchronization(ctx, profile, warehouse, ""); !errors.Is(err, ErrBackupScope) {
		t.Fatal("warehouse snapshot was mistaken for configuration", err)
	}

	if _, err := RecoverSynchronization(
		ctx,
		profile,
		warehouse+".absent",
		"",
	); !errors.Is(
		err,
		os.ErrNotExist,
	) {
		t.Fatal("missing archive produced an approvable import", err)
	}
}
