// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"bytes"
	"database/sql"
	"encoding/binary"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOpenInstalledInitializesVacantProfile(t *testing.T) {
	ctx := t.Context()
	dir := filepath.Join(t.TempDir(), "stores")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	store, err := OpenInstalled(ctx, testProfile(filepath.Join(dir, "wake.db")), nil)
	if err != nil {
		t.Fatal(err)
	}

	revision, err := store.UpstreamRevision(ctx)
	if err != nil || revision == "" {
		t.Error("fresh synchronization profile is unavailable", err)
	}

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}

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
		"PRAGMA main.cache_size=1; PRAGMA access.cache_size=1; PRAGMA sync.cache_size=1;" +
			"PRAGMA main.cache_spill=1; PRAGMA access.cache_spill=1; PRAGMA sync.cache_spill=1;",
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

	var journals [storeCount]string
	for i, path := range paths {
		journals[i] = path + "-journal"
	}

	super := paths[0] + "-mjABCDEF912"
	members := []byte(strings.Join(journals[:], "\x00") + "\x00")
	if err := os.WriteFile(super, members, 0o600); err != nil {
		t.Fatal(err)
	}

	for _, journal := range journals {
		bindTestJournal(t, journal, super)
	}
	// SQLite has completed rollback in the access participant when this
	// stand-alone open closes. The other participants still need rollback.
	access, err := sql.Open("sqlite", paths[1])
	if err != nil {
		t.Fatal(err)
	}

	var count int

	err = access.QueryRow("SELECT count(*) FROM credentials").Scan(&count)
	if err := errors.Join(err, access.Close()); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(journals[1]); !os.IsNotExist(err) {
		t.Fatal("SQLite did not clear the recovered access journal", err)
	}

	if _, err := os.Stat(super); err != nil {
		t.Fatal("SQLite cleared the super-journal before the other participants recovered", err)
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

	other := seedUpgradeStores(t)
	if err := os.WriteFile(paths[1], readRestoreFixture(t, other.ConnectionsPath), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := OpenInstalled(ctx, profile, nil); !errors.Is(err, ErrAssociation) {
		t.Fatal("a substitute access store was allowed to recover the original journal", err)
	}

	if !bytes.Equal(before, readRestoreFixture(t, hotPath)) {
		t.Fatal("substituted participant modified original recovery state")
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

	if _, err := os.Stat(super); !os.IsNotExist(err) {
		t.Fatal("completed rollback left the super-journal behind", err)
	}

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(super, members, 0o600); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenInstalled(ctx, profile, nil)
	if err != nil {
		t.Fatal("failed to reopen after the final child rollback", err)
	}

	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(super); !os.IsNotExist(err) {
		t.Fatal("verified orphan super-journal was not removed", err)
	}
}

// Append SQLite's super-journal name and trailer to a real hot rollback
// journal emitted by the interrupted child.
func bindTestJournal(t *testing.T, journal, super string) {
	t.Helper()

	data := readRestoreFixture(t, journal)
	magic := [8]byte{0xd9, 0xd5, 0x05, 0xf9, 0x20, 0xa1, 0x63, 0xd7}
	if !bytes.HasPrefix(data, magic[:]) || len(data) < 28 {
		t.Fatalf("expected a real hot child journal %s (%d bytes)", journal, len(data))
	}

	pageSize := binary.BigEndian.Uint32(data[24:28])
	data = binary.BigEndian.AppendUint32(data, 0x40000000/pageSize+1)
	data = append(data, super...)
	//nolint:gosec // The temporary path is bounded by the filesystem.
	data = binary.BigEndian.AppendUint32(data, uint32(len(super)))

	var checksum int32
	for _, b := range []byte(super) {
		//nolint:gosec // SQLite's checksum treats every filename byte as signed.
		checksum += int32(int8(b))
	}

	data = binary.BigEndian.AppendUint32(data, uint32(checksum))
	data = append(data, magic[:]...)
	if err := os.WriteFile(journal, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSuperJournalRequiresKnownPrivateMembers(t *testing.T) {
	profile := seedUpgradeStores(t)

	paths := profile.paths()

	var journals [storeCount]string
	for i, path := range paths {
		journals[i] = path + "-journal"
	}

	super := paths[0] + "-mjABCDEF912"
	complete := []byte(journals[0] + "\x00" + journals[1] + "\x00")
	if err := os.WriteFile(super, complete, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := checkSuperJournal(super, journals, len(complete)); err != nil {
		t.Fatal("refused already recovered child journals", err)
	}

	for _, path := range journals[:2] {
		if err := os.WriteFile(path, []byte("rollback"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if err := checkSuperJournal(super, journals, len(complete)); err != nil {
		t.Fatal("original participant names were refused", err)
	}

	//nolint:gosec // Confirm that recovery refuses a journal with unsafe permissions.
	if err := os.Chmod(journals[0], 0o644); err != nil {
		t.Fatal(err)
	}

	if err := checkSuperJournal(super, journals, len(complete)); !errors.Is(err, ErrUpgradeRecovery) {
		t.Fatal("accepted a non-private child journal", err)
	}

	if err := os.Chmod(journals[0], 0o600); err != nil {
		t.Fatal(err)
	}

	if err := checkSuperJournal(super, journals, len(complete)-1); !errors.Is(err, ErrUpgradeRecovery) {
		t.Fatal("accepted an oversized participant list", err)
	}

	for _, invalid := range [][]byte{
		complete[:len(complete)-1],
		[]byte(journals[0] + "\x00" + journals[0] + "\x00"),
		[]byte(paths[0] + ".unrelated\x00"),
	} {
		if err := os.WriteFile(super, invalid, 0o600); err != nil {
			t.Fatal(err)
		}

		if err := checkSuperJournal(super, journals, len(invalid)); !errors.Is(err, ErrUpgradeRecovery) {
			t.Fatal("accepted an invalid participant list", err)
		}
	}
}

func TestDiscardColdJournalPreservesUnknownHeaders(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wake.db-journal")
	for _, record := range []struct {
		data []byte
		want error
	}{
		{nil, nil},
		{[]byte("short"), ErrUpgradeRecovery},
		{[]byte("unrecognized header"), ErrUpgradeRecovery},
	} {
		if err := os.WriteFile(path, record.data, 0o600); err != nil {
			t.Fatal(err)
		}

		if err := discardColdJournal(path); !errors.Is(err, record.want) {
			t.Fatal("unexpected journal handling", err, record.want)
		}

		if record.want == nil {
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatal("empty journal was not removed", err)
			}
		} else if !bytes.Equal(readRestoreFixture(t, path), record.data) {
			t.Fatal("unrecognized journal was modified")
		}
	}
}
