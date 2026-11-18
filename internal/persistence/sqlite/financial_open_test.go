// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"go.trippwill.dev/wake/internal/application"
)

func TestCancelledFinancialCreationLeavesDestinationVacant(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	profile := Profile{WarehousePath: filepath.Join(dir, "wake.db")}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if store, err := OpenFinancial(ctx, profile, nil); store != nil || !errors.Is(err, context.Canceled) {
		t.Fatal("canceled creation did not stop", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatal("canceled creation left warehouse or staging files", entries, err)
	}

	store, err := OpenFinancial(t.Context(), profile, nil)
	if err != nil {
		t.Fatal(err)
	}

	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
}

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

	if _, err = store.Snapshot(ctx, application.HistoryPublication{}); err != nil {
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

func TestFinancialPreviewValidatesExistingWarehouse(t *testing.T) {
	for _, tc := range []struct {
		name, statement, confirm string
		want                     error
	}{
		{name: "current"},
		{name: "stale confirmation", confirm: "earlier-plan", want: ErrUpgradePlanChanged},
		{name: "old format", statement: "UPDATE store_metadata SET version='wake-warehouse/2'", want: ErrUnsupportedSchema},
		{name: "missing lineage", statement: "DELETE FROM financial_lineage", want: ErrCorrupt},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			dir := filepath.Join(t.TempDir(), "private")
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}

			profile := Profile{WarehousePath: filepath.Join(dir, "wake.db")}

			store, err := OpenFinancial(ctx, profile, nil)
			if err != nil {
				t.Fatal(err)
			}

			defer func() { _ = store.Close() }()

			if _, err = PreviewFinancial(ctx, profile.WarehousePath, ""); !errors.Is(err, ErrUnsafeStore) {
				t.Fatal("preview opened an owned warehouse", err)
			}

			if tc.statement != "" {
				if _, err = store.db.ExecContext(ctx, tc.statement); err != nil {
					t.Fatal(err)
				}
			}

			if err = store.Close(); err != nil {
				t.Fatal(err)
			}

			//nolint:gosec // This file belongs to the private synthetic profile.
			before, err := os.ReadFile(profile.WarehousePath)
			if err != nil {
				t.Fatal(err)
			}

			plan, err := PreviewFinancial(ctx, profile.WarehousePath, tc.confirm)
			if !errors.Is(err, tc.want) {
				t.Fatalf("preview error = %v, want %v", err, tc.want)
			}

			if tc.want == nil && (len(plan.Stores) != 1 || plan.Stores[0].Path != profile.WarehousePath ||
				plan.Stores[0].Current != schemaVersion || plan.Stores[0].ID == "" || plan.AppliedRule != 1) {
				t.Fatal("invalid installed format preview", plan)
			}

			if tc.statement != "" {
				if _, err = OpenFinancial(ctx, profile, nil); !errors.Is(err, tc.want) {
					t.Fatalf("startup error = %v, want %v", err, tc.want)
				}
			}

			//nolint:gosec // This file belongs to the private synthetic profile.
			after, err := os.ReadFile(profile.WarehousePath)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("format checking changed the warehouse", err)
			}
		})
	}
}

func TestFinancialMaintenanceIntentsBlockStartupAndPreview(t *testing.T) {
	ctx := t.Context()
	dir := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	profile := Profile{WarehousePath: filepath.Join(dir, "wake.db")}

	intent := profile.WarehousePath + RestoreIntentSuffix
	if err := os.WriteFile(intent, []byte("pending restore"), 0o600); err != nil {
		t.Fatal(err)
	}

	check := func(want error) {
		t.Helper()

		if _, err := OpenFinancial(ctx, profile, nil); !errors.Is(err, want) {
			t.Fatalf("startup error = %v, want %v", err, want)
		}

		if _, err := PreviewFinancial(ctx, profile.WarehousePath, ""); !errors.Is(err, want) {
			t.Fatalf("preview error = %v, want %v", err, want)
		}

		if _, err := os.Stat(profile.WarehousePath); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("maintenance created a warehouse", err)
		}
	}
	check(ErrRestorePending)

	if err := os.Remove(intent); err != nil {
		t.Fatal(err)
	}

	if err := writeUpgradeIntent(
		profile.WarehousePath+UpgradeIntentSuffix,
		upgradeIntent{Plan: UpgradePlan{ID: "pending"}},
	); err != nil {
		t.Fatal(err)
	}

	check(ErrUpgradeRecovery)
}

func TestFinancialActivationPreservesOccupiedDestination(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	stage := filepath.Join(dir, "prepared")
	if err := os.WriteFile(stage, []byte("prepared"), 0o600); err != nil {
		t.Fatal(err)
	}

	target := filepath.Join(dir, "wake.db")

	guard, err := lockStoreDirectory(target)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = guard.Close() }()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err = activateFinancial(ctx, stage, target); !errors.Is(err, context.Canceled) {
		t.Fatal("activation ignored cancellation", err)
	}

	if _, err = os.Stat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("canceled activation published a file", err)
	}

	if err = os.WriteFile(target, []byte("other store"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err = activateFinancial(t.Context(), stage, target); !errors.Is(err, ErrRestoreOccupied) {
		t.Fatal("activation accepted an occupied destination", err)
	}

	//nolint:gosec // This file belongs to the private synthetic profile.
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "other store" {
		t.Fatal("activation replaced the destination", err)
	}
}

func TestFinancialOpenRefusesLinkedWarehouse(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	profile := Profile{WarehousePath: filepath.Join(dir, "wake.db")}

	store, err := OpenFinancial(t.Context(), profile, nil)
	if err != nil {
		t.Fatal(err)
	}

	if err = store.Close(); err != nil {
		t.Fatal(err)
	}

	link := filepath.Join(dir, "linked.db")
	if err = os.Symlink(profile.WarehousePath, link); err != nil {
		t.Fatal(err)
	}

	if linked, err := OpenFinancial(
		t.Context(),
		Profile{WarehousePath: link},
		nil,
	); linked != nil ||
		!errors.Is(err, ErrUnsafeStore) {
		t.Fatal("startup opened a linked warehouse", err)
	}
}

func TestFinancialOpenRequiresWritableWarehouse(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("file permission admission requires an unprivileged operator")
	}

	ctx := t.Context()
	dir := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(dir, 0o500); err != nil {
		t.Fatal(err)
	}

	profile := Profile{WarehousePath: filepath.Join(dir, "wake.db")}
	if opened, err := OpenFinancial(ctx, profile, nil); opened != nil || !errors.Is(err, os.ErrPermission) {
		t.Fatal("accepted creation in a read-only directory", err)
	}

	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}

	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	store, err := OpenFinancial(ctx, profile, nil)
	if err != nil {
		t.Fatal(err)
	}

	if err = store.Close(); err != nil {
		t.Fatal(err)
	}

	if err = os.Chmod(profile.WarehousePath, 0o400); err != nil {
		t.Fatal(err)
	}

	if opened, err := OpenFinancial(ctx, profile, nil); opened != nil || err == nil {
		t.Fatal("accepted an existing read-only warehouse", err)
	}
}
