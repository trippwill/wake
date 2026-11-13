// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"archive/tar"
	"bytes"
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

//go:embed testdata/warehouse-v1-to-v2.sql
var warehouseV2Migration string

//go:embed testdata/access-v1-to-v2.sql
var accessV2Migration string

const upgradeFixtureNote = "Retained selection changes. Activity and positions will be temporarily unavailable."

func fixtureUpgrade(warehouse, access, rebuild bool) upgradeBaseline {
	b := releaseBaseline()
	if warehouse {
		b.formats[0] = "wake-warehouse/2"
		b.migrations[0] = []migration{{schemaVersion, b.formats[0], warehouseV2Migration}}
		b.schemas[0] = strings.Replace(
			b.schemas[0],
			"resumed_at TEXT NOT NULL DEFAULT ''",
			"resumed_at TEXT NOT NULL DEFAULT '', fixture_label TEXT NOT NULL DEFAULT 'v2'",
			1,
		) +
			"\nCREATE TABLE main.fixture_warehouse(value TEXT NOT NULL) STRICT;"
	}

	if access {
		b.formats[1] = "wake-access/2"
		b.migrations[1] = []migration{{accessSchemaVersion, b.formats[1], accessV2Migration}}
		b.schemas[1] = strings.Replace(
			b.schemas[1],
			"settings BLOB NOT NULL)",
			"settings BLOB NOT NULL, fixture_label TEXT NOT NULL DEFAULT 'v2')",
			1,
		) +
			"\nCREATE TABLE access.fixture_access(value TEXT NOT NULL) STRICT;"
	}

	if rebuild {
		b.rule = "retained-claims-and-decisions/2"
		b.notes = map[int64]string{2: upgradeFixtureNote}
	}

	return b
}

func seedUpgradeStores(t *testing.T) Profile {
	t.Helper()
	s := newStoreWithSyntheticAccount(t)
	p := testProfile(s.owners[0].Name())
	processConfirmationReport(t, s, "upgrade", syntheticOption)

	view, err := s.Dashboard(t.Context())
	if err != nil || len(view.Executions) != 1 {
		t.Fatal(view, err)
	}

	retainDecision(t, s, "activity", view.Executions[0].ID(), "price", "2.340000000001", "USD", "set")

	if _, err = s.db.Exec(
		`INSERT INTO access.configurations(id,provider,settings) VALUES('synthetic','ibkr-flex',X'7b7d');
 INSERT INTO access.credentials VALUES('synthetic','token',X'010203');
 INSERT INTO work(connection_id,stream,operation,state_version,state) VALUES('synthetic','confirmations','acquire','fixture',X'0102');`,
	); err != nil {
		t.Fatal(err)
	}

	if err = s.Close(); err != nil {
		t.Fatal(err)
	}

	return p
}

func previewFixture(t *testing.T, p Profile, baseline upgradeBaseline) UpgradePlan {
	t.Helper()

	var plan UpgradePlan
	if s, err := openStore(
		t.Context(),
		p,
		nil,
		baseline,
		upgradeOptions{preview: true, result: &plan},
	); s != nil ||
		err != nil {
		t.Fatal("preview", s, err)
	}

	return plan
}

func storeBytes(t *testing.T, p Profile) [2][]byte {
	t.Helper()

	var result [2][]byte
	for i, path := range p.paths() {
		data, err := os.ReadFile(filepath.Clean(path))
		if err != nil {
			t.Fatal(err)
		}

		result[i] = data
	}

	return result
}

func TestUpgradePreviewApprovalAndResume(t *testing.T) {
	for _, unattended := range []bool{false, true} {
		name := "explicit"
		if unattended {
			name = "unattended"
		}

		t.Run(name, func(t *testing.T) {
			p := seedUpgradeStores(t)
			b := fixtureUpgrade(true, true, true)

			before := storeBytes(t, p)
			plan := previewFixture(t, p, b)
			if plan.ID == "" || plan.BackupPath != p.WarehousePath+upgradeBackupSuffix || len(plan.Steps) != 2 ||
				plan.Rebuild != "required" ||
				!reflect.DeepEqual(plan.ReleaseNotes, []RuleReleaseNote{{2, upgradeFixtureNote}}) {
				t.Fatal(plan)
			}

			if _, err := openStore(
				t.Context(),
				p,
				nil,
				b,
				upgradeOptions{},
			); !errors.Is(
				err,
				ErrUpgradeConfirmation,
			) {
				t.Fatal(err)
			}

			b.notes[2] += " Revised note."
			if _, err := openStore(
				t.Context(),
				p,
				nil,
				b,
				upgradeOptions{confirm: plan.ID},
			); !errors.Is(
				err,
				ErrUpgradePlanChanged,
			) {
				t.Fatal("old release notes approved", err)
			}

			delete(b.notes, 2)

			p.UnattendedUpgrade = true
			if _, err := openStore(t.Context(), p, nil, b, upgradeOptions{}); !errors.Is(err, ErrUpgradeNotes) {
				t.Fatal("missing release notes accepted", err)
			}

			if got := storeBytes(t, p); !reflect.DeepEqual(got, before) {
				t.Fatal("preflight modified stores")
			}

			if _, err := os.Stat(p.WarehousePath + UpgradeIntentSuffix); !os.IsNotExist(err) {
				t.Fatal("preflight wrote approval", err)
			}

			if _, err := os.Stat(plan.BackupPath); !os.IsNotExist(err) {
				t.Fatal("preflight wrote a backup", err)
			}

			b.notes[2] = upgradeFixtureNote

			p.UnattendedUpgrade = unattended
			opts := upgradeOptions{}
			if !unattended {
				opts.confirm = plan.ID
			}

			var logs bytes.Buffer

			logger := slog.New(slog.NewJSONHandler(&logs, nil))

			s, err := openStore(t.Context(), p, logger, b, opts)
			if err != nil {
				t.Fatal(err)
			}

			if err = s.Close(); err != nil {
				t.Fatal(err)
			}

			assertBackupPair(t, plan.BackupPath, before)

			var loggedPlan UpgradePlan
			for line := range strings.SplitSeq(logs.String(), "\n") {
				var entry struct {
					Event string      `json:"event"`
					Plan  UpgradePlan `json:"plan"`
				}
				if json.Unmarshal([]byte(line), &entry) == nil && entry.Event == "daemon_upgrade_plan" {
					loggedPlan = entry.Plan
				}
			}

			if !reflect.DeepEqual(loggedPlan, plan) {
				t.Fatal("logged plan differs from approved preview", loggedPlan, plan)
			}

			p.UnattendedUpgrade = false

			s, err = openStore(t.Context(), p, nil, b, upgradeOptions{})
			if err != nil {
				t.Fatal(err)
			}

			defer func() { _ = s.Close() }()

			if !s.HistoryUpdating() {
				t.Fatal("approved update was not resumed")
			}

			for range 8 {
				if !s.HistoryUpdating() {
					break
				}

				if _, err = s.RunHistoryUpdate(t.Context(), time.Now()); err != nil {
					t.Fatal(err)
				}
			}

			if s.HistoryUpdating() {
				t.Fatal("rebuild did not finish")
			}

			state, err := s.queries.HistoryUpdate(t.Context())
			if err != nil || state.Applied != 2 || state.Target != 0 {
				t.Fatal(state, err)
			}

			assertMigratedFixture(t, s, b, plan)
		})
	}
}

func assertMigratedFixture(t *testing.T, s *Store, b upgradeBaseline, plan UpgradePlan) {
	t.Helper()

	ctx := t.Context()
	metadata, _, accessID, err := readUpgradeState(ctx, s.db)
	if err != nil || accessID != metadata[1].id {
		t.Fatal(metadata, accessID, err)
	}

	for i, m := range metadata {
		if m.version != b.formats[i] || m.id != plan.Stores[i].ID {
			t.Fatal("changed store identity or wrong format", m)
		}
	}

	for _, check := range []struct{ query, want string }{
		{`SELECT hex(encrypted) FROM access.credentials WHERE connection_id='synthetic'`, "010203"},
		{`SELECT hex(state) FROM work WHERE connection_id='synthetic'`, "0102"},
		{`SELECT text FROM operator_decisions`, "2.340000000001"},
		{`SELECT count(*) FROM artifacts WHERE id='upgrade'`, "1"},
	} {
		var got string
		if err = s.db.QueryRowContext(ctx, check.query).Scan(&got); err != nil || got != check.want {
			t.Fatal(check.query, got, err)
		}
	}
}

func TestUpgradeIndependentChainsMatchFreshSchemas(t *testing.T) {
	for _, store := range storeKinds {
		t.Run(store, func(t *testing.T) {
			p := seedUpgradeStores(t)
			b := fixtureUpgrade(store == "warehouse", store == "connections", false)
			plan := previewFixture(t, p, b)
			if plan.ID == "" || len(plan.Steps) != 1 || plan.Steps[0].Store != store || plan.Rebuild != "" {
				t.Fatal(plan)
			}

			ctx := t.Context()

			s, err := openStore(ctx, p, nil, b, upgradeOptions{})
			if err != nil {
				t.Fatal(err)
			}

			defer func() { _ = s.Close() }()

			assertMigratedFixture(t, s, b, plan)

			dir := filepath.Join(t.TempDir(), "fresh")
			if err = os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}

			fresh := testProfile(filepath.Join(dir, "wake.db"))

			target, err := openStore(ctx, fresh, nil, b, upgradeOptions{})
			if err != nil {
				t.Fatal(err)
			}

			defer func() { _ = target.Close() }()

			for _, query := range []string{
				`SELECT group_concat(name||':'||type||':'||"notnull"||':'||coalesce(dflt_value,'')) FROM pragma_table_info('profile')`,
				`SELECT group_concat(name||':'||type||':'||"notnull"||':'||coalesce(dflt_value,'')) FROM pragma_table_info('configurations','access')`,
			} {
				var upgraded, expected string
				if err = s.db.QueryRowContext(ctx, query).Scan(&upgraded); err != nil {
					t.Fatal(err)
				}

				if err = target.db.QueryRowContext(ctx, query).Scan(&expected); err != nil || upgraded != expected {
					t.Fatal(query, upgraded, expected, err)
				}
			}
		})
	}
}

func TestUpgradeSQLFailureRollsBackBothStores(t *testing.T) {
	p := seedUpgradeStores(t)
	b := fixtureUpgrade(true, true, true)

	b.migrations[1][0].sql += "\nINSERT INTO access.missing_fixture VALUES ('fail');"
	plan := previewFixture(t, p, b)
	before := storeBytes(t, p)
	if s, err := openStore(t.Context(), p, nil, b, upgradeOptions{confirm: plan.ID}); s != nil || err == nil {
		t.Fatal("invalid migration committed", s, err)
	}

	if got := storeBytes(t, p); !reflect.DeepEqual(got, before) {
		t.Fatal("failed migration did not roll back both stores")
	}

	var pending UpgradePlan
	if _, err := openStore(
		t.Context(),
		p,
		nil,
		b,
		upgradeOptions{preview: true, result: &pending},
	); err != nil ||
		pending.ID != plan.ID {
		t.Fatal("approved intent was not retained", pending, err)
	}

	if _, err := Open(t.Context(), p, nil); !errors.Is(err, ErrUpgradeRecovery) {
		t.Fatal("wrong build opened interrupted upgrade", err)
	}
}

func TestMigrationCrashHelper(t *testing.T) {
	mode := os.Getenv("WAKE_TEST_MIGRATION_CRASH")
	if mode == "" {
		return
	}

	ctx := context.Background()
	p := Profile{
		WarehousePath:   os.Getenv("WAKE_TEST_MIGRATION_WAREHOUSE"),
		ConnectionsPath: os.Getenv("WAKE_TEST_MIGRATION_ACCESS"),
	}
	b := fixtureUpgrade(true, true, true)

	plan := previewFixture(t, p, b)

	var owners [2]*os.File
	for i, path := range p.paths() {
		file, err := lockStoreFile(path, false)
		if err != nil {
			t.Fatal(err)
		}

		owners[i] = file
	}

	files, err := upgradeFiles(owners)
	if err != nil {
		t.Fatal(err)
	}

	backupDigest, err := createUpgradeBackup(ctx, plan, owners)
	if err != nil {
		t.Fatal(err)
	}

	if err = writeUpgradeIntent(
		p.WarehousePath+UpgradeIntentSuffix,
		upgradeIntent{Plan: plan, Files: files, BackupDigest: backupDigest},
	); err != nil {
		t.Fatal(err)
	}

	db := sql.OpenDB(&connector{paths: p.paths(), owners: owners})
	if _, err = db.ExecContext(ctx, "PRAGMA main.cache_size=1; PRAGMA access.cache_size=1;"); err != nil {
		t.Fatal(err)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}

	if err = applyMigrations(ctx, tx, b.migrations); err != nil {
		t.Fatal(err)
	}

	if _, err = tx.ExecContext(ctx, "UPDATE main.history_update SET target=2, phase='activity'"); err != nil {
		t.Fatal(err)
	}

	if mode == "committed" {
		if err = tx.Commit(); err != nil {
			t.Fatal(err)
		}
	} else {
		// Spill modified pages to both files before dying. SQLite must roll them
		// back before Wake reads either store's format again.
		for _, query := range []string{"UPDATE main.artifacts SET content=zeroblob(2097152)", "UPDATE access.credentials SET encrypted=zeroblob(2097152)"} {
			if _, err = tx.ExecContext(ctx, query); err != nil {
				t.Fatal(err)
			}
		}
	}

	os.Exit(0)
}

func TestMigrationProcessCrashRecovery(t *testing.T) {
	for _, mode := range []string{"uncommitted", "committed"} {
		t.Run(mode, func(t *testing.T) {
			p := seedUpgradeStores(t)
			baseline := fixtureUpgrade(true, true, true)
			plan := previewFixture(t, p, baseline)

			original := storeBytes(t, p)

			binary, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}

			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			//nolint:gosec // Run only this test executable against disposable stores.
			child := exec.CommandContext(ctx, binary, "-test.run=^TestMigrationCrashHelper$")

			child.Env = append(os.Environ(), "WAKE_TEST_MIGRATION_CRASH="+mode,
				"WAKE_TEST_MIGRATION_WAREHOUSE="+p.WarehousePath, "WAKE_TEST_MIGRATION_ACCESS="+p.ConnectionsPath)
			if output, err := child.CombinedOutput(); err != nil {
				t.Fatal(string(output), err)
			}

			if mode == "uncommitted" {
				for _, path := range p.paths() {
					info, err := os.Stat(path + "-journal")
					if err != nil || info.Size() <= 512 {
						t.Fatal("missing spilled rollback journal", path, err)
					}
				}
			}

			before := storeBytes(t, p)
			pending := previewFixture(t, p, baseline)
			if pending.ID != plan.ID || !reflect.DeepEqual(before, storeBytes(t, p)) {
				t.Fatal("preview recovered or changed an interrupted migration")
			}

			assertBackupPair(t, plan.BackupPath, original)

			info, err := os.Stat(plan.BackupPath)
			if err != nil {
				t.Fatal(err)
			}

			// A failed verification must stop before SQLite can recover the journals.
			saved := plan.BackupPath + ".saved"
			if err = os.Rename(plan.BackupPath, saved); err != nil {
				t.Fatal(err)
			}

			if s, e := openStore(
				ctx,
				p,
				nil,
				baseline,
				upgradeOptions{},
			); s != nil ||
				!errors.Is(e, ErrUpgradeBackup) {
				t.Fatal("recovered without the approved backup", s, e)
			}

			if !reflect.DeepEqual(before, storeBytes(t, p)) {
				t.Fatal("backup failure modified interrupted stores")
			}

			data, err := os.ReadFile(filepath.Clean(saved))
			if err != nil {
				t.Fatal(err)
			}
			// Change a byte in the first database without changing either tar header.
			data[512] ^= 1
			//nolint:gosec // The backup is inside this test's disposable profile.
			if err = os.WriteFile(filepath.Clean(plan.BackupPath), data, 0o600); err != nil {
				t.Fatal(err)
			}

			if s, e := openStore(
				ctx,
				p,
				nil,
				baseline,
				upgradeOptions{},
			); s != nil ||
				!errors.Is(e, ErrUpgradeBackup) {
				t.Fatal("recovered with a corrupt backup", s, e)
			}

			if !reflect.DeepEqual(before, storeBytes(t, p)) {
				t.Fatal("corrupt backup modified interrupted stores")
			}

			if err = os.Rename(saved, plan.BackupPath); err != nil {
				t.Fatal(err)
			}

			s, err := openStore(ctx, p, nil, baseline, upgradeOptions{})
			if err != nil {
				t.Fatal(err)
			}

			defer func() { _ = s.Close() }()

			if !s.HistoryUpdating() {
				t.Fatal("durable approval lost across process interruption")
			}

			if _, err = os.Stat(p.WarehousePath + UpgradeIntentSuffix); !os.IsNotExist(err) {
				t.Fatal("verified migration left an intent", err)
			}

			after, err := os.Stat(plan.BackupPath)
			if err != nil || !os.SameFile(info, after) {
				t.Fatal("retry replaced the original backup", err)
			}

			assertBackupPair(t, plan.BackupPath, original)

			assertMigratedFixture(t, s, baseline, plan)
		})
	}
}

func TestUpgradePlansInterveningStepsAndNotes(t *testing.T) {
	p := seedUpgradeStores(t)
	b := fixtureUpgrade(true, true, true)
	b.formats[0] = "wake-warehouse/3"
	b.migrations[0] = append(b.migrations[0], migration{"wake-warehouse/2", "wake-warehouse/3", "SELECT 1;"})

	b.rule = "retained-claims-and-decisions/3"
	b.notes[3] = "Second retained selection change."
	plan := previewFixture(t, p, b)
	if len(plan.Steps) != 3 || plan.Steps[0].To != "wake-warehouse/2" ||
		plan.Steps[1].To != "wake-warehouse/3" || plan.Steps[2].To != "wake-access/2" ||
		!reflect.DeepEqual(plan.ReleaseNotes, []RuleReleaseNote{{2, upgradeFixtureNote}, {3, b.notes[3]}}) {
		t.Fatal(plan)
	}

	for _, steps := range [][]migration{
		b.migrations[0][:1],
		{{schemaVersion, "wake-warehouse/3", "SELECT 1;"}},
		{{schemaVersion, "wake-access/2", "SELECT 1;"}},
	} {
		before := storeBytes(t, p)
		b.migrations[0] = steps
		if _, err := openStore(
			t.Context(),
			p,
			nil,
			b,
			upgradeOptions{confirm: plan.ID},
		); !errors.Is(
			err,
			ErrUnsupportedSchema,
		) {
			t.Fatal("accepted broken migration chain", err)
		}

		if got := storeBytes(t, p); !reflect.DeepEqual(got, before) {
			t.Fatal("invalid migration path modified stores")
		}
	}
}

func assertBackupPair(t *testing.T, path string, expected [2][]byte) {
	t.Helper()

	file, err := openBackup(path)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = file.Close() }()

	archive := tar.NewReader(file)
	for i, name := range backupEntries {
		entry, e := archive.Next()
		if e != nil || entry.Name != name || entry.Mode != 0o600 {
			t.Fatal(entry, e)
		}

		data, e := io.ReadAll(archive)
		if e != nil || !bytes.Equal(data, expected[i]) {
			t.Fatal("backup entry differs from original", name, e)
		}
	}

	if _, err = archive.Next(); err != io.EOF {
		t.Fatal("unexpected additional backup entry", err)
	}
}

func TestUpgradeBackupFailureLeavesStoresUntouched(t *testing.T) {
	p := seedUpgradeStores(t)
	baseline := fixtureUpgrade(true, true, false)

	plan := previewFixture(t, p, baseline)
	original := storeBytes(t, p)
	if err := os.WriteFile(plan.BackupPath, []byte("unrelated file"), 0o600); err != nil {
		t.Fatal(err)
	}

	s, err := openStore(t.Context(), p, nil, baseline, upgradeOptions{})
	if s != nil || !errors.Is(err, ErrUpgradeBackup) {
		t.Fatal(s, err)
	}

	if got := storeBytes(t, p); !reflect.DeepEqual(got, original) {
		t.Fatal("upgrade modified a store without a backup")
	}

	if data, err := os.ReadFile(
		filepath.Clean(plan.BackupPath),
	); err != nil ||
		string(data) != "unrelated file" {
		t.Fatal("upgrade changed an unrelated file", err)
	}

	if _, err = os.Stat(p.WarehousePath + UpgradeIntentSuffix); !os.IsNotExist(err) {
		t.Fatal("backup failure authorized a migration", err)
	}
}

func TestUpgradeReplacesLatestBackupWithNextVerifiedPair(t *testing.T) {
	p := seedUpgradeStores(t)
	baseline := fixtureUpgrade(true, true, false)
	first := storeBytes(t, p)
	plan := previewFixture(t, p, baseline)

	s, err := openStore(t.Context(), p, nil, baseline, upgradeOptions{})
	if err != nil {
		t.Fatal(err)
	}

	if err = s.Close(); err != nil {
		t.Fatal(err)
	}

	assertBackupPair(t, plan.BackupPath, first)

	second := storeBytes(t, p)
	baseline.rule = "retained-claims-and-decisions/2"
	baseline.notes = map[int64]string{2: upgradeFixtureNote}

	next := previewFixture(t, p, baseline)
	if next.BackupPath != plan.BackupPath || next.ID == plan.ID {
		t.Fatal(next)
	}

	s, err = openStore(t.Context(), p, nil, baseline, upgradeOptions{confirm: next.ID})
	if err != nil {
		t.Fatal(err)
	}

	if err = s.Close(); err != nil {
		t.Fatal(err)
	}

	assertBackupPair(t, next.BackupPath, second)
}
