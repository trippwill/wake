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
	"net/url"
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

			restored := testProfile(filepath.Join(t.TempDir(), "restored.db"))
			//nolint:gosec // A private test directory needs owner traversal permission.
			if err = os.Chmod(filepath.Dir(restored.WarehousePath), 0o700); err != nil {
				t.Fatal(err)
			}

			restored.Restored = true
			extractBackupPair(t, plan.BackupPath, restored)

			copy, err := Open(t.Context(), restored, nil)
			if err != nil {
				t.Fatal("restored pair could not start on the earlier format", err)
			}

			recovery, err := copy.queries.Profile(t.Context())
			if err != nil || recovery.Paused != 1 || recovery.AccessID != plan.Stores[1].ID {
				t.Fatal(recovery, err)
			}

			assertMigratedFixture(t, copy, releaseBaseline(), plan)

			if err = copy.Close(); err != nil {
				t.Fatal(err)
			}

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

	p := testProfile(filepath.Join(t.TempDir(), "snapshot.db"))
	extractBackupPair(t, path, p)

	for i, data := range storeBytes(t, p) {
		if got, want := backupContents(t, data), backupContents(t, expected[i]); !reflect.DeepEqual(got, want) {
			t.Fatal("snapshot contents or row order differ from original", backupEntries[i])
		}
	}
}

func extractBackupPair(t *testing.T, path string, p Profile) {
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
		if e != nil {
			t.Fatal(name, e)
		}

		if e = os.WriteFile(p.paths()[i], data, 0o600); e != nil {
			t.Fatal(e)
		}
	}

	if _, err = archive.Next(); err != io.EOF {
		t.Fatal("unexpected additional backup entry", err)
	}
}

func backupContents(t *testing.T, data []byte) map[string][][]any {
	t.Helper()

	path := filepath.Join(t.TempDir(), "snapshot.db")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	ctx := t.Context()

	db := sql.OpenDB(&connector{paths: [2]string{path}, probe: true})
	defer func() { _ = db.Close() }()

	tables, err := db.QueryContext(
		ctx,
		"SELECT name FROM sqlite_schema WHERE type='table' AND name NOT LIKE 'sqlite_%'",
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tables.Close() }()

	out := map[string][][]any{}
	for tables.Next() {
		var name string
		if err = tables.Scan(&name); err != nil {
			t.Fatal(err)
		}

		out[name] = backupRows(t, db, name)
	}

	if err = tables.Err(); err != nil {
		t.Fatal(err)
	}

	return out
}

func backupRows(t *testing.T, db *sql.DB, name string) [][]any {
	t.Helper()

	//nolint:gosec // Quote a schema identifier from the disposable test database.
	rows, err := db.QueryContext(
		t.Context(),
		`SELECT * FROM "`+strings.ReplaceAll(name, `"`, `""`)+`" ORDER BY rowid`,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()

	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}

	var values [][]any
	for rows.Next() {
		row := make([]any, len(columns))
		dest := make([]any, len(columns))
		for i := range row {
			dest[i] = &row[i]
		}

		if err = rows.Scan(dest...); err != nil {
			t.Fatal(err)
		}

		values = append(values, row)
	}

	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}

	return values
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

func TestUpgradeRefusesIncompleteOrMismatchedPairs(t *testing.T) {
	for _, fixture := range []struct {
		name, change string
	}{
		{"missing access", ""},
		{"unrelated access", "UPDATE access.store_metadata SET id='unrelated'"},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			p := seedUpgradeStores(t)
			ctx := t.Context()
			if fixture.change == "" {
				if err := os.Remove(p.ConnectionsPath); err != nil {
					t.Fatal(err)
				}
			} else {
				s, err := Open(ctx, p, nil)
				if err != nil {
					t.Fatal(err)
				}

				if _, err = s.db.ExecContext(ctx, fixture.change); err != nil {
					t.Fatal(err)
				}

				if err = s.Close(); err != nil {
					t.Fatal(err)
				}
			}

			p.UnattendedUpgrade = true
			if s, err := openStore(ctx, p, nil, fixtureUpgrade(true, true, true), upgradeOptions{}); s != nil ||
				!errors.Is(err, ErrUpgradePair) {
				t.Fatal(s, err)
			}

			if _, err := os.Stat(p.WarehousePath + UpgradeIntentSuffix); !os.IsNotExist(err) {
				t.Fatal("incomplete pair wrote an intent", err)
			}

			if _, err := os.Stat(p.WarehousePath + upgradeBackupSuffix); !os.IsNotExist(err) {
				t.Fatal("incomplete pair wrote a backup", err)
			}
		})
	}
}

func TestUpgradeBackupRefusesLinkedDestination(t *testing.T) {
	p := seedUpgradeStores(t)
	baseline := fixtureUpgrade(true, true, false)

	plan := previewFixture(t, p, baseline)
	before := storeBytes(t, p)
	if err := os.Symlink(p.ConnectionsPath, plan.BackupPath); err != nil {
		t.Fatal(err)
	}

	if s, err := openStore(
		t.Context(),
		p,
		nil,
		baseline,
		upgradeOptions{},
	); s != nil ||
		!errors.Is(err, ErrUpgradeBackup) {
		t.Fatal(s, err)
	}

	if after := storeBytes(t, p); !reflect.DeepEqual(before, after) {
		t.Fatal("unsafe backup destination changed the stores")
	}

	if link, err := os.Readlink(plan.BackupPath); err != nil || link != p.ConnectionsPath {
		t.Fatal(link, err)
	}
}

func TestUpgradeRecoveryRefusesChangedIntentOrStore(t *testing.T) {
	p := seedUpgradeStores(t)
	b := fixtureUpgrade(true, true, false)
	b.migrations[1][0].sql += "\nINSERT INTO access.missing_fixture VALUES ('fail');"
	plan := previewFixture(t, p, b)
	before := storeBytes(t, p)

	ctx := t.Context()
	if s, err := openStore(ctx, p, nil, b, upgradeOptions{confirm: plan.ID}); s != nil ||
		!errors.Is(err, ErrUpgradeRecovery) {
		t.Fatal(s, err)
	}

	path := p.WarehousePath + UpgradeIntentSuffix

	original, err := readUpgradeIntent(path)
	if err != nil || original.Plan.ID != plan.ID {
		t.Fatal(original, err)
	}

	for _, tamper := range []func(*upgradeIntent){
		func(intent *upgradeIntent) { intent.Plan.Stores[1].Path += ".wrong" },
		func(intent *upgradeIntent) { intent.Plan.BackupPath += ".wrong" },
	} {
		changed := original
		tamper(&changed)

		if err = writeUpgradeIntent(path, changed); err != nil {
			t.Fatal(err)
		}

		if s, e := openStore(ctx, p, nil, b, upgradeOptions{}); s != nil ||
			!errors.Is(e, ErrUpgradeRecovery) {
			t.Fatal(s, e)
		}
	}

	if err = os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}

	if s, e := openStore(ctx, p, nil, b, upgradeOptions{}); s != nil || !errors.Is(e, ErrUpgradeRecovery) {
		t.Fatal(s, e)
	}

	if err = writeUpgradeIntent(path, original); err != nil {
		t.Fatal(err)
	}

	if err = os.Rename(p.ConnectionsPath, p.ConnectionsPath+".original"); err != nil {
		t.Fatal(err)
	}

	if err = os.WriteFile(p.ConnectionsPath, before[1], 0o600); err != nil {
		t.Fatal(err)
	}

	if s, e := openStore(ctx, p, nil, b, upgradeOptions{}); s != nil || !errors.Is(e, ErrUpgradeRecovery) {
		t.Fatal("accepted a replaced store", s, e)
	}

	if got := storeBytes(t, p); !reflect.DeepEqual(got, before) {
		t.Fatal("recovery refusal changed the stores")
	}

	assertBackupPair(t, plan.BackupPath, before)
}

func TestUpgradeRejectsMigrationsThatFailTargetVerification(t *testing.T) {
	for _, fixture := range []struct{ name, sql string }{
		{"unexpected table", "CREATE TABLE main.unexpected (value TEXT);"},
		{"changed identity", "UPDATE main.store_metadata SET id='new'"},
		{"lost pairing", "UPDATE main.profile SET access_id='unrelated'"},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			p := seedUpgradeStores(t)
			b := fixtureUpgrade(true, true, false)
			b.migrations[0][0].sql += "\n" + fixture.sql
			plan := previewFixture(t, p, b)
			before := storeBytes(t, p)

			if s, err := openStore(t.Context(), p, nil, b, upgradeOptions{confirm: plan.ID}); s != nil ||
				!errors.Is(err, ErrUpgradeRecovery) {
				t.Fatal(s, err)
			}

			if got := storeBytes(t, p); !reflect.DeepEqual(got, before) {
				t.Fatal("failed target verification did not roll back both stores")
			}

			assertBackupPair(t, plan.BackupPath, before)
		})
	}
}

func TestRestoredStartupRefusesMixedMissingOrDamagedPair(t *testing.T) {
	for _, test := range []struct {
		name string
		want error
	}{
		{"mixed", ErrUpgradePair},
		{"missing", ErrUpgradePair},
		{"damaged", ErrUnsupportedSchema},
	} {
		t.Run(test.name, func(t *testing.T) {
			p := seedUpgradeStores(t)
			switch test.name {
			case "mixed":
				other := seedUpgradeStores(t)
				if err := os.WriteFile(p.ConnectionsPath, storeBytes(t, other)[1], 0o600); err != nil {
					t.Fatal(err)
				}
			case "missing":
				if err := os.Remove(p.ConnectionsPath); err != nil {
					t.Fatal(err)
				}
			case "damaged":
				if err := os.WriteFile(p.ConnectionsPath, []byte("truncated snapshot"), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			//nolint:gosec // Read only this test's disposable warehouse.
			before, err := os.ReadFile(p.WarehousePath)
			if err != nil {
				t.Fatal(err)
			}

			p.Restored = true

			s, err := Open(t.Context(), p, nil)
			if s != nil || !errors.Is(err, test.want) {
				t.Fatal(s, err)
			}

			//nolint:gosec // Read only this test's disposable warehouse.
			after, err := os.ReadFile(p.WarehousePath)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("refused restore changed warehouse", err)
			}
		})
	}
}

func TestBackupRefusesRecoveryFilesAndKeepsPreviousArchive(t *testing.T) {
	p := seedUpgradeStores(t)
	ctx := t.Context()

	archive, err := Backup(ctx, p, nil)
	if err != nil {
		t.Fatal(err)
	}

	original := storeBytes(t, p)

	info, err := os.Stat(archive)
	if err != nil {
		t.Fatal(err)
	}

	journal := p.WarehousePath + "-journal"
	if err = os.WriteFile(journal, []byte("pending rollback"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err = Backup(ctx, p, nil); !errors.Is(err, ErrUpgradeRecovery) {
		t.Fatal("backup accepted an unresolved rollback journal", err)
	}

	p.Restored = true
	if s, e := Open(ctx, p, nil); s != nil || !errors.Is(e, ErrUpgradeRecovery) {
		t.Fatal("restore accepted an unresolved rollback journal", s, e)
	}

	if after, e := os.Stat(archive); e != nil || !os.SameFile(info, after) {
		t.Fatal("backup replaced the verified archive", e)
	}

	if !reflect.DeepEqual(storeBytes(t, p), original) {
		t.Fatal("refused recovery modified the stores")
	}
}

func TestBackupRefusesForeignKeyViolations(t *testing.T) {
	for _, test := range []struct {
		store int
		query string
	}{
		{0, "INSERT INTO normalization_receipts VALUES('missing', 'fixture', X'00')"},
		{1, "INSERT INTO credentials VALUES('missing', 'token', X'00')"},
	} {
		t.Run(storeKinds[test.store], func(t *testing.T) {
			ctx := t.Context()
			p := seedUpgradeStores(t)

			archive, err := Backup(ctx, p, nil)
			if err != nil {
				t.Fatal(err)
			}

			good := storeBytes(t, p)

			info, err := os.Stat(archive)
			if err != nil {
				t.Fatal(err)
			}

			uri := (&url.URL{Scheme: "file", Path: p.paths()[test.store], RawQuery: "mode=rw"}).String()

			db, err := sql.Open("sqlite", uri)
			if err != nil {
				t.Fatal(err)
			}

			_, err = db.ExecContext(ctx, "PRAGMA foreign_keys=OFF; "+test.query)
			if err = errors.Join(err, db.Close()); err != nil {
				t.Fatal(err)
			}

			if _, err = Backup(ctx, p, nil); !errors.Is(err, ErrCorrupt) {
				t.Fatal("backup accepted a foreign-key violation", err)
			}

			if after, e := os.Stat(archive); e != nil || !os.SameFile(info, after) {
				t.Fatal("invalid backup replaced the last verified archive", e)
			}

			assertBackupPair(t, archive, good)
		})
	}
}

func TestBackupRefusesProviderMismatchAndKeepsPreviousArchive(t *testing.T) {
	p := seedUpgradeStores(t)
	ctx := t.Context()
	original := storeBytes(t, p)

	archive, err := Backup(ctx, p, nil)
	if err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(archive)
	if err != nil {
		t.Fatal(err)
	}

	s, err := Open(ctx, p, nil)
	if err != nil {
		t.Fatal(err)
	}

	_, err = s.db.ExecContext(ctx, "UPDATE access.configurations SET provider='snaptrade' WHERE id='synthetic'")
	if err = errors.Join(err, s.Close()); err != nil {
		t.Fatal(err)
	}

	changed := storeBytes(t, p)
	if _, err = Backup(ctx, p, nil); !errors.Is(err, ErrAssociation) {
		t.Fatal("backup accepted mismatched providers", err)
	}

	if after, e := os.Stat(archive); e != nil || !os.SameFile(info, after) {
		t.Fatal("backup replaced the verified archive", e)
	}

	if !reflect.DeepEqual(storeBytes(t, p), changed) {
		t.Fatal("backup modified the original pair")
	}

	assertBackupPair(t, archive, original)
}
