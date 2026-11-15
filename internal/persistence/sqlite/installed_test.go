// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestOpenInstalledRejectsSingleWitnessRollback(t *testing.T) {
	ctx := t.Context()

	profile := seedUpgradeStores(t)
	paths := profile.paths()

	var old [storeCount][]byte
	for i, path := range paths {
		old[i] = readRestoreFixture(t, path)
	}

	store, err := OpenInstalled(ctx, profile, nil)
	if err != nil {
		t.Fatal(err)
	}

	revision, err := store.UpstreamRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}

	err = store.Delay(ctx, "ibkr-flex", "pacing", "fixture", time.Now().Add(time.Hour))
	if err := errors.Join(err, store.Close()); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(paths[2], old[2], 0o600); err != nil {
		t.Fatal(err)
	}

	currentWarehouse := readRestoreFixture(t, paths[0])
	currentAccess := readRestoreFixture(t, paths[1])
	if _, err := OpenInstalled(ctx, profile, nil); !errors.Is(err, ErrAssociation) {
		t.Fatal("installed startup admitted a stale synchronization witness", err)
	}

	if !bytes.Equal(currentWarehouse, readRestoreFixture(t, paths[0])) ||
		!bytes.Equal(old[2], readRestoreFixture(t, paths[2])) ||
		!bytes.Equal(currentAccess, readRestoreFixture(t, paths[1])) {
		t.Fatal("failed attached startup modified the installation")
	}

	// Replacing both witnesses coherently cannot be distinguished from restart.
	if err := os.WriteFile(paths[1], old[1], 0o600); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenInstalled(ctx, profile, nil)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = reopened.Close() }()

	if current, err := reopened.UpstreamRevision(ctx); err != nil || current != revision {
		t.Fatal("unchanged association after coherent rollback", current, err)
	}
}

func TestInstalledCrashChild(t *testing.T) {
	if os.Getenv("WAKE_TEST_INSTALLED_CRASH") != "1" {
		t.Skip("invoked by the installed recovery test")
	}

	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	store, err := OpenInstalled(t.Context(), testProfile(filepath.Join(dir, "wake.db")), nil)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := store.db.Exec(
		"PRAGMA main.cache_size=1; PRAGMA access.cache_size=1; PRAGMA sync.cache_size=1;",
	); err != nil {
		t.Fatal(err)
	}

	tx, err := store.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}

	for _, statement := range []string{
		"UPDATE main.artifacts SET content=zeroblob(2097152)",
		"UPDATE access.credentials SET encrypted=zeroblob(2097152)",
		"UPDATE sync.work SET state=zeroblob(2097152)",
		"UPDATE sync.sync_association SET token='interrupted'",
	} {
		if _, err := tx.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}

	os.Exit(39)
}

func TestOpenInstalledRecoversInterruptedParticipants(t *testing.T) {
	ctx := t.Context()
	profile := seedUpgradeStores(t)
	paths := profile.paths()
	//nolint:gosec // Re-execute this test binary against its disposable stores.
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestInstalledCrashChild$")
	child.Dir = filepath.Dir(paths[0])

	child.Env = append(os.Environ(), "WAKE_TEST_INSTALLED_CRASH=1")

	output, err := child.CombinedOutput()
	if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 39 {
		t.Fatalf("interrupt attached write: %v\n%s", err, output)
	}

	// Without a participant, both attached and financial-only startup must fail
	// before SQLite can change the warehouse or journal.
	hotPath := paths[0] + "-journal"
	before := readRestoreFixture(t, hotPath)
	missing := paths[1] + ".hold"
	if err := os.Rename(paths[1], missing); err != nil {
		t.Fatal(err)
	}

	if _, err := OpenInstalled(ctx, profile, nil); err == nil {
		t.Fatal("attached startup recovered with a missing participant")
	}

	if _, err := OpenFinancial(ctx, profile, nil); err == nil {
		t.Fatal("financial startup opened an unresolved journal")
	}

	if !bytes.Equal(before, readRestoreFixture(t, hotPath)) {
		t.Fatal("failed startup changed the journal")
	}

	if err := os.Rename(missing, paths[1]); err != nil {
		t.Fatal(err)
	}

	store, err := OpenInstalled(ctx, profile, nil)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = store.Close() }()

	current, witness := upstreamTokens(t, store)
	if current != witness || current == "interrupted" {
		t.Fatal("interrupted write survived", current, witness)
	}
}
