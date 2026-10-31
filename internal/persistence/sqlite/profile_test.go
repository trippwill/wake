// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/host"
	"go.trippwill.dev/wake/provider"
)

func profileFixture(t *testing.T) (Profile, *Store) {
	t.Helper()

	root := t.TempDir()
	p := Profile{
		WarehousePath:   filepath.Join(root, "data", "history.sqlite"),
		ConnectionsPath: filepath.Join(root, "access", "credentials.sqlite"),
	}
	for _, dir := range []string{filepath.Dir(p.WarehousePath), filepath.Dir(p.ConnectionsPath)} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	s, err := Open(t.Context(), p, nil)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = s.Close() })

	return p, s
}

func TestCompanionLossPreservesFinancialHistoryAndRestrictions(t *testing.T) {
	for _, lost := range [][]int{{1}} {
		t.Run(fmt.Sprint(lost),
			func(t *testing.T) {
				ctx := t.Context()
				s, path := testStore(t)
				p := testProfile(path)

				settings, _ := provider.NewSettings(map[string]string{"name": "Synthetic", "query_id": "123"})
				if err := s.Configure(
					ctx,
					host.Connection{ID: "connection", Provider: "ibkr-flex", Settings: settings},
					map[string][]byte{"token": []byte("synthetic-encrypted-secret")},
				); err != nil {
					t.Fatal(err)
				}

				key := host.WorkKey{Connection: "connection", Stream: "confirmations", Operation: "acquire"}
				if err := s.Schedule(ctx, key, true); err != nil {
					t.Fatal(err)
				}

				if _, err := s.Checkpoint(ctx, key); err != nil {
					t.Fatal(err)
				}

				now := time.Now().UTC()
				next := now.Add(time.Hour)
				state, _ := provider.NewState("v1", []byte("durable-continuation"))

				step, _ := provider.NewStep(state, provider.Artifact{}, next)
				progress, _ := provider.NewProgress("retained", 1, 1)
				if _, err := s.Accept(ctx, key, 0, step, nil, progress); err != nil {
					t.Fatal(err)
				}

				if _, err := s.Reserve(ctx, "ibkr-flex", "requests", "synthetic-scope", now, time.Hour); err != nil {
					t.Fatal(err)
				}

				if ok, _, err := s.ReserveReference(ctx, now, 25); err != nil || !ok {
					t.Fatal(ok, err)
				}

				if err := s.ReferenceCooldown(ctx, next); err != nil {
					t.Fatal(err)
				}
				// Retained financial rows and decisions do not depend on active access configuration.
				if _, err := s.db.Exec(
					`INSERT INTO artifacts(id,connection_id,stream,operation,format,media_type,retrieved,hash,content) VALUES('report','connection','confirmations','acquire','synthetic','application/xml','2026-10-20T00:00:00Z','synthetic',X'00');
 INSERT INTO accounts(id,singleton,name,currency,connection_id,provider,stream,authority,kind,scope,value,artifact_id) VALUES('account',1,'Retained account','','connection','ibkr-flex','confirmations','ibkr','account','','U1','report');
 INSERT INTO operator_decisions VALUES('decision','activity','retained-id','name','Retained','','set','admin/test','2026-10-20T00:00:00Z','','','submission',X'00');
 INSERT INTO normalization_receipts VALUES('report','v1',X'7b7d');`,
				); err != nil {
					t.Fatal(err)
				}

				before, err := s.Checkpoint(ctx, key)
				if err != nil {
					t.Fatal(err)
				}

				if err = s.Close(); err != nil {
					t.Fatal(err)
				}

				for _, i := range lost {
					if err = os.Remove(p.paths()[i]); err != nil {
						t.Fatal(err)
					}
				}

				var logs bytes.Buffer

				recovered, err := Open(ctx, p, slog.New(slog.NewJSONHandler(&logs, nil)))
				if err != nil {
					t.Fatal(err)
				}

				defer func() { _ = recovered.Close() }()

				view, err := recovered.Dashboard(ctx)
				if err != nil || !view.Storage.Paused || view.Account.ID() != "account" {
					t.Fatal(view, err)
				}

				for _, connection := range view.Connections {
					lostAccess := false
					for _, i := range lost {
						if i == 1 {
							lostAccess = true
						}
					}

					if connection.SettingsStored == lostAccess || connection.CredentialStored == lostAccess ||
						connection.MissingAccess != lostAccess {
						t.Fatal("incorrect recovery prerequisites", connection)
					}
				}

				after, err := recovered.Checkpoint(ctx, key)
				if err != nil || after.Revision != before.Revision || after.Next != before.Next ||
					after.State.Version() != before.State.Version() ||
					!bytes.Equal(after.State.Payload(), before.State.Payload()) ||
					after.Acknowledged != before.Acknowledged {
					t.Fatal("lost checkpoint", after, before, err)
				}

				if work, err := recovered.PendingWork(ctx, next.Add(time.Hour)); err != nil || len(work) != 0 {
					t.Fatal("unreviewed execution", work, err)
				}

				if err := recovered.UpstreamAllowed(ctx); !errors.Is(err, provider.ErrUnavailable) {
					t.Fatal(err)
				}

				if ok, _, err := recovered.ReserveReference(ctx, next.Add(time.Hour), 25); err != nil || ok {
					t.Fatal("unreviewed reference access", ok, err)
				}

				if result, err := recovered.Reserve(
					ctx,
					"ibkr-flex",
					"requests",
					"synthetic-scope",
					now,
					time.Hour,
				); err != nil ||
					result.Granted() {
					t.Fatal("lost pacing reservation", result, err)
				}

				var n int
				if err := recovered.db.QueryRow(`SELECT (SELECT count(*) FROM operator_decisions)+(SELECT count(*) FROM normalization_receipts)`).
					Scan(&n); err != nil ||
					n != 2 {
					t.Fatal("lost recovery inputs", n, err)
				}

				if view.Storage.ReferenceReview {
					if err := recovered.ResumeStorage(ctx, view.Storage.Revision, "admin/test", now); err == nil {
						t.Fatal("unrepaired access resumed")
					}

					if err := recovered.Reconfigure(
						ctx,
						host.Connection{ID: "connection", Provider: "ibkr-flex", Settings: settings},
					); err != nil {
						t.Fatal(err)
					}

					if err := recovered.ReplaceSecret(
						ctx,
						"connection",
						"token",
						[]byte("repaired-secret"),
					); err != nil {
						t.Fatal(err)
					}

					if err := recovered.SetReferenceKey(ctx, nil); err != nil {
						t.Fatal(err)
					}
				}

				if err := recovered.ResumeStorage(
					ctx,
					view.Storage.Revision+1,
					"admin/test",
					now,
				); !errors.Is(
					err,
					application.ErrStale,
				) {
					t.Fatal("stale resume", err)
				}

				if err := recovered.ResumeStorage(ctx, view.Storage.Revision, "admin/test", now); err != nil {
					t.Fatal(err)
				}

				if ok, until, err := recovered.ReserveReference(
					ctx,
					now,
					25,
				); err != nil || ok ||
					!until.Equal(next) {
					t.Fatal("lost reference cooldown", ok, until, err)
				}

				if err := recovered.Close(); err != nil {
					t.Fatal(err)
				}

				restarted, err := Open(ctx, p, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = restarted.Close() }()

				view, err = restarted.Dashboard(ctx)
				if err != nil || view.Storage.Paused {
					t.Fatal("restart lost authorization", err)
				}

				if work, err := restarted.PendingWork(ctx, next.Add(time.Second)); err != nil || len(work) != 1 {
					t.Fatal("lost synchronization obligation", work, err)
				}

				for _, event := range []string{"storage_recovery_started", "storage_recovery_completed", "storage_paused", "storage_resumed"} {
					if !strings.Contains(logs.String(), event) {
						t.Fatal("missing diagnostic", event, logs.String())
					}
				}

				for _, secret := range []string{"synthetic-encrypted-secret", "repaired-secret", "durable-continuation", "Retained account"} {
					if strings.Contains(logs.String(), secret) {
						t.Fatal("diagnostic leaked private payload")
					}
				}
			},
		)
	}
}

func TestTwoStorePartitionAndRollback(t *testing.T) {
	_, s := profileFixture(t)
	ctx := t.Context()
	settings, _ := provider.NewSettings(map[string]string{"name": "Synthetic"})

	if _, err := s.db.Exec(
		`CREATE TRIGGER access.reject_secret BEFORE INSERT ON credentials BEGIN SELECT RAISE(ABORT,'injected'); END`,
	); err != nil {
		t.Fatal(err)
	}

	if err := s.Configure(
		ctx,
		host.Connection{ID: "new", Provider: "synthetic", Settings: settings},
		map[string][]byte{"token": []byte("encrypted")},
	); err == nil {
		t.Fatal("fault ignored")
	}

	for _, table := range []string{"main.connections", "access.configurations", "access.credentials"} {
		var n int
		if err := s.db.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil || n != 0 {
			t.Fatal("partial transaction", table, n, err)
		}
	}

	if _, err := s.db.Exec(`DROP TRIGGER access.reject_secret`); err != nil {
		t.Fatal(err)
	}

	if err := s.Configure(
		ctx,
		host.Connection{ID: "new", Provider: "synthetic", Settings: settings},
		nil,
	); err != nil {
		t.Fatal(err)
	}

	if err := s.Schedule(
		ctx,
		host.WorkKey{Connection: "new", Stream: "stream", Operation: "acquire"},
		true,
	); err != nil {
		t.Fatal(err)
	}

	for schema, tables := range map[string][]string{"main": {"connections", "work", "scheduled", "normalization_receipts", "operator_decisions", "attempts", "reference_cache"}, "access": {"configurations", "credentials", "reference_settings"}} {
		for _, table := range tables {
			var n int
			if err := s.db.QueryRow("SELECT count(*) FROM "+schema+".sqlite_schema WHERE type='table' AND name=?", table).
				Scan(&n); err != nil ||
				n != 1 {
				t.Fatal(schema, table, n, err)
			}
		}
	}
}

//nolint:gosec // Replace only synthetic credential data in private fixtures.
func TestCredentialReplacementPreservesWarehouseAndRequiresReview(t *testing.T) {
	p, s := profileFixture(t)
	other, replacement := profileFixture(t)
	ctx := t.Context()
	insertReferenceInstrument(t, s, "retained", "US0000000001")

	settings, _ := provider.NewSettings(map[string]string{"name": "Independent access", "query_id": "123"})
	if err := replacement.Configure(
		ctx,
		host.Connection{ID: "replacement", Provider: "ibkr-flex", Settings: settings},
		map[string][]byte{"token": []byte("encrypted")},
	); err != nil {
		t.Fatal(err)
	}

	var warehouseID string
	if err := s.db.QueryRow(`SELECT id FROM main.store_metadata`).Scan(&warehouseID); err != nil {
		t.Fatal(err)
	}

	_ = s.Close()
	_ = replacement.Close()

	data, err := os.ReadFile(other.ConnectionsPath)
	if err != nil {
		t.Fatal(err)
	}

	if err = os.WriteFile(p.ConnectionsPath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(ctx, p, nil)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = reopened.Close() }()

	var sameWarehouse string
	if err := reopened.db.QueryRow(`SELECT id FROM main.store_metadata`).Scan(&sameWarehouse); err != nil {
		t.Fatal(err)
	}

	if sameWarehouse != warehouseID {
		t.Fatal("credential replacement changed warehouse identity")
	}

	if _, err := reopened.queries.Instrument(ctx, "retained"); err != nil {
		t.Fatal("credential replacement lost financial data", err)
	}

	view, err := reopened.Dashboard(ctx)
	if err != nil || !view.Storage.Paused || !view.Storage.ReferenceReview || len(view.Connections) != 1 ||
		view.Connections[0].MissingAccess {
		t.Fatal("credential replacement did not require review", view, err)
	}
}

func TestEveryCompanionOwnershipAndLinks(t *testing.T) {
	for _, i := range []int{1} {
		for _, mode := range []string{"lock", "symlink", "hardlink", "sidecar", "superjournal"} {
			t.Run(storeKinds[i]+"/"+mode, func(t *testing.T) {
				p, s := profileFixture(t)
				_ = s.Close()
				path := p.paths()[i]
				switch mode {
				case "lock":
					owner, e := claimFile(path)
					if e != nil {
						t.Fatal(e)
					}
					defer func() { _ = owner.Close() }()
				case "symlink":
					backup := path + ".saved"
					if e := os.Rename(path, backup); e != nil {
						t.Fatal(e)
					}

					if e := os.Symlink(backup, path); e != nil {
						t.Fatal(e)
					}
				case "hardlink":
					if e := os.Link(path, path+".alias"); e != nil {
						t.Fatal(e)
					}
				case "superjournal":
					if e := os.Symlink(path, path+"-mjABCDEF912"); e != nil {
						t.Fatal(e)
					}
				case "sidecar":
					if e := os.Symlink(path, path+"-journal"); e != nil {
						t.Fatal(e)
					}
				}

				if opened, e := Open(t.Context(), p, nil); !errors.Is(e, ErrUnsafeStore) {
					if opened != nil {
						_ = opened.Close()
					}

					t.Fatal("unsafe companion accepted", e)
				}
			})
		}
	}
}

func TestAttachedCrashHelper(t *testing.T) {
	phase := os.Getenv("WAKE_M4_CRASH_PHASE")
	if phase == "" {
		return
	}

	p := Profile{
		WarehousePath:   os.Getenv("WAKE_M4_CRASH_WAREHOUSE"),
		ConnectionsPath: os.Getenv("WAKE_M4_CRASH_CONNECTIONS"),
	}

	s, err := Open(context.Background(), p, nil)
	if err != nil {
		t.Fatal(err)
	}

	for _, schema := range storeSchemas {
		if _, err = s.db.Exec("PRAGMA " + schema + ".cache_size=1"); err != nil {
			t.Fatal(err)
		}
	}

	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}

	payload := strings.Repeat("synthetic committed transaction;", 32768)
	for _, statement := range []string{`UPDATE reference_evidence SET response=CAST(? AS BLOB) WHERE id='e'`, `UPDATE access.configurations SET settings=CAST(? AS BLOB) WHERE id='crash'`, `UPDATE main.reference_cache SET status=? WHERE key='crash'`} {
		if _, err = tx.Exec(statement, payload); err != nil {
			t.Fatal(err)
		}
	}

	if phase == "committed" {
		if err = tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}

	os.Exit(0)
}

func TestAttachedCrashRecoveryIsAtomic(t *testing.T) {
	for _, phase := range []string{"uncommitted", "committed"} {
		t.Run(phase, func(t *testing.T) {
			p, s := profileFixture(t)
			if _, err := s.db.Exec(
				`INSERT INTO connections(id,provider) VALUES('crash','before'); INSERT INTO access.configurations(id,provider,settings) VALUES('crash','before',X'6265666f7265'); INSERT INTO reference_evidence VALUES('e','now',X'',X'6265666f7265',200,X''); INSERT INTO main.reference_cache VALUES('crash','before',X'','later','e');`,
			); err != nil {
				t.Fatal(err)
			}

			_ = s.Close()

			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}

			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()

			//nolint:gosec // Trusted test executable operating only on disposable stores.
			child := exec.CommandContext(ctx, executable, "-test.run=^TestAttachedCrashHelper$")

			child.Env = append(
				os.Environ(),
				"WAKE_M4_CRASH_PHASE="+phase,
				"WAKE_M4_CRASH_WAREHOUSE="+p.WarehousePath,
				"WAKE_M4_CRASH_CONNECTIONS="+p.ConnectionsPath,
			)
			if output, err := child.CombinedOutput(); err != nil {
				t.Fatal(string(output), err)
			}

			if phase == "uncommitted" {
				for _, path := range p.paths() {
					if info, err := os.Stat(path + "-journal"); err != nil || info.Size() <= 512 {
						t.Fatal("crash did not spill journal", path, err)
					}
				}
			}

			reopened, err := Open(t.Context(), p, nil)
			if err != nil {
				t.Fatal(err)
			}

			defer func() { _ = reopened.Close() }()

			want := "before"
			if phase == "committed" {
				want = strings.Repeat("synthetic committed transaction;", 32768)
			}

			for _, query := range []string{`SELECT CAST(response AS TEXT) FROM reference_evidence WHERE id='e'`, `SELECT CAST(settings AS TEXT) FROM access.configurations WHERE id='crash'`, `SELECT status FROM main.reference_cache WHERE key='crash'`} {
				var got string
				if err := reopened.db.QueryRow(query).Scan(&got); err != nil || got != want {
					t.Fatal("mixed crash outcome", len(got), err)
				}
			}
		})
	}
}

func TestInterruptedCompanionReconstructionRestartsPaused(t *testing.T) {
	p, s := profileFixture(t)
	if _, err := s.db.Exec(
		`CREATE TRIGGER reject_recovery BEFORE UPDATE ON profile BEGIN SELECT RAISE(ABORT,'injected'); END`,
	); err != nil {
		t.Fatal(err)
	}

	_ = s.Close()

	if err := os.Remove(p.paths()[1]); err != nil {
		t.Fatal(err)
	}

	if opened, err := Open(t.Context(), p, nil); err == nil {
		_ = opened.Close()

		t.Fatal("reconstruction fault ignored")
	}

	db, err := sql.Open("sqlite", p.paths()[0])
	if err != nil {
		t.Fatal(err)
	}

	_, err = db.Exec(`DROP TRIGGER reject_recovery`)
	_ = db.Close()

	if err != nil {
		t.Fatal(err)
	}

	recovered, err := Open(t.Context(), p, nil)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = recovered.Close() }()

	view, err := recovered.Dashboard(t.Context())
	if err != nil || !view.Storage.Paused {
		t.Fatal("interrupted reconstruction restored authority", view, err)
	}
}

func TestRecoveryReferenceReviewPreservesRetryEligibility(t *testing.T) {
	p, s := profileFixture(t)
	ctx := t.Context()
	insertReferenceInstrument(t, s, "instrument", "US0000000001")

	next := time.Now().UTC().Add(6 * time.Hour).Format(time.RFC3339Nano)
	if _, err := s.db.Exec(
		`UPDATE instruments SET reference_status='no-match',reference_next=? WHERE id='instrument'`,
		next,
	); err != nil {
		t.Fatal(err)
	}

	_ = s.Close()

	if err := os.Remove(p.paths()[1]); err != nil {
		t.Fatal(err)
	}

	recovered, err := Open(ctx, p, nil)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = recovered.Close() }()

	if err = recovered.SetReferenceKey(ctx, nil); err != nil {
		t.Fatal(err)
	}

	instrument, err := recovered.queries.Instrument(ctx, "instrument")
	if err != nil || instrument.ReferenceNext != next {
		t.Fatal("reference review shortened retry eligibility", instrument.ReferenceNext, err)
	}

	view, err := recovered.Dashboard(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if err = recovered.ResumeStorage(ctx, view.Storage.Revision, "admin/test", time.Now()); err != nil {
		t.Fatal(err)
	}

	if err = recovered.ResumeStorage(ctx, view.Storage.Revision, "admin/test", time.Now()); err != nil {
		t.Fatal("resume retry failed", err)
	}

	if pending, err := recovered.PendingReference(ctx, time.Now()); err != nil || len(pending) != 0 {
		t.Fatal("recovery advanced negative reference retry", pending, err)
	}
}

//nolint:gosec // Read and remove only synthetic SQLite stores in private test directories.
func TestFreshWarehouseRetainsAccessAndDiscardsOldExecution(t *testing.T) {
	t.Run("reset", func(t *testing.T) {
		p, s := profileFixture(t)
		ctx := t.Context()
		settings, _ := provider.NewSettings(map[string]string{"query_id": "123"})
		token := []byte("opaque encrypted provider credential")
		referenceKey := []byte("opaque encrypted reference credential")

		if err := s.Configure(
			ctx,
			host.Connection{
				ID:       "retained",
				Provider: "ibkr-flex",
				Name:     "Retained connection",
				Settings: settings,
			},
			map[string][]byte{"token": token},
		); err != nil {
			t.Fatal(err)
		}

		if err := s.SetReferenceKey(ctx, referenceKey); err != nil {
			t.Fatal(err)
		}

		if err := s.Schedule(
			ctx,
			host.WorkKey{Connection: "retained", Stream: "confirmations", Operation: "acquire"},
			true,
		); err != nil {
			t.Fatal(err)
		}

		if _, err := s.db.Exec(
			`INSERT INTO artifacts(id,connection_id,stream,operation,format,media_type,retrieved,hash,content) VALUES('old-report','retained','confirmations','acquire','synthetic','application/xml','2026-10-20T00:00:00Z','synthetic',X'00');
 INSERT INTO reference_evidence VALUES('old-evidence','now',X'',X'',200,X'');
 INSERT INTO main.attempts VALUES('old-attempt','old-report','v1','succeeded',X'7b7d');
 INSERT INTO main.reference_cache VALUES('old-job','no-match',X'5b5d','later','old-evidence');`,
		); err != nil {
			t.Fatal(err)
		}

		var oldWarehouse, accessID string
		if err := s.db.QueryRow(`SELECT id FROM main.store_metadata`).Scan(&oldWarehouse); err != nil {
			t.Fatal(err)
		}

		if err := s.db.QueryRow(`SELECT id FROM access.store_metadata`).Scan(&accessID); err != nil {
			t.Fatal(err)
		}

		_ = s.Close()

		if err := os.Remove(p.WarehousePath); err != nil {
			t.Fatal(err)
		}

		s, err := Open(ctx, p, nil)
		if err != nil {
			t.Fatal(err)
		}

		defer func() { _ = s.Close() }()

		view, err := s.Dashboard(ctx)
		if err != nil {
			t.Fatal(err)
		}

		if !view.Storage.Paused || !view.Storage.WarehouseReset || view.Storage.ReferenceReview ||
			view.Account.ID() != "" ||
			len(view.Connections) != 1 ||
			view.Connections[0].MissingAccess ||
			view.Connections[0].Name != "Retained connection" {
			t.Fatal("fresh warehouse did not retain reviewed access", view)
		}

		got, err := s.Secret(ctx, "retained", "token")
		if err != nil || !bytes.Equal(got, token) {
			t.Fatal("provider credential changed", err)
		}

		key, failure, err := s.ReferenceKey(ctx)
		if err != nil || failure != "" || !bytes.Equal(key, referenceKey) {
			t.Fatal("reference credential changed", err)
		}

		var newWarehouse, newAccessID string
		if err := s.db.QueryRow(`SELECT id FROM main.store_metadata`).Scan(&newWarehouse); err != nil {
			t.Fatal(err)
		}

		if err := s.db.QueryRow(`SELECT id FROM access.store_metadata`).
			Scan(&newAccessID); err != nil {
			t.Fatal(err)
		}

		if oldWarehouse == newWarehouse || accessID != newAccessID {
			t.Fatal("wrong fresh warehouse association")
		}

		for _, table := range []string{"main.accounts", "main.artifacts", "main.scheduled", "main.attempts", "main.reference_cache"} {
			var n int
			if err := s.db.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil || n != 0 {
				t.Fatal("old warehouse execution state survived", table, n, err)
			}
		}

		if err := s.UpstreamAllowed(ctx); !errors.Is(err, provider.ErrUnavailable) {
			t.Fatal("fresh warehouse granted upstream authority", err)
		}

		_ = s.Close()

		s, err = Open(ctx, p, nil)
		if err != nil {
			t.Fatal(err)
		}

		view, err = s.Dashboard(ctx)
		if err != nil || !view.Storage.Paused || !view.Storage.WarehouseReset || len(view.Connections) != 1 {
			t.Fatal("intact restart lost retained access or pause", view, err)
		}
	})
}
