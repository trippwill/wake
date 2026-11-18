// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"testing"
	"time"

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/application/calculation"
	"go.trippwill.dev/wake/internal/persistence/sqlite/sqlc"
)

func correctionFixture(t *testing.T) (*Store, calculation.Decision) {
	t.Helper()
	s := newStoreWithSyntheticAccount(t)
	processConfirmationReport(t, s, "correction-capability", syntheticDebt)

	view, err := s.Dashboard(t.Context())
	if err != nil || len(view.Executions) != 1 {
		t.Fatal(view, err)
	}

	form, err := s.CorrectionForm(t.Context(), "activity", view.Executions[0].ID())
	if err != nil {
		t.Fatal(err)
	}

	d, err := calculation.NewDecision(
		calculation.DecisionInput{
			TargetKind:       "activity",
			TargetID:         form.ID,
			Scope:            "quantity",
			Text:             "12.00000000000000001",
			Action:           "set",
			Submission:       rand.Text(),
			ExpectedRevision: form.Revision,
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	return s, d
}

func TestActivityCorrectionCapabilitiesEndWithTheirTransaction(t *testing.T) {
	s, d := correctionFixture(t)
	ctx := t.Context()

	var reader application.ActivityCorrectionReader

	err := s.ViewActivityCorrection(ctx, func(r application.ActivityCorrectionReader) error {
		reader = r
		if _, ok := r.(application.ActivityCorrectionWriter); ok {
			t.Fatal("preview exposes writes")
		}

		_, err := r.ActivityRevision(ctx, d.Fields().TargetID)

		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err = reader.ActivityRevision(ctx, d.Fields().TargetID); !errors.Is(err, sql.ErrTxDone) {
		t.Fatal("escaped reader remained usable", err)
	}

	var writer application.ActivityCorrectionWriter

	err = s.UpdateActivityCorrection(
		ctx,
		func(w application.ActivityCorrectionWriter) error { writer = w; return nil },
	)
	if err != nil {
		t.Fatal(err)
	}

	if err = writer.InsertDecision(
		ctx,
		d.Fields(),
		d.Fields(),
		"synthetic",
		time.Now(),
		"",
	); !errors.Is(
		err,
		sql.ErrTxDone,
	) {
		t.Fatal("escaped writer remained usable", err)
	}
}

func TestActivityCorrectionPreviewAndIdempotentRetryDoNotWrite(t *testing.T) {
	s, d := correctionFixture(t)
	ctx := t.Context()

	changes, remove := s.SubscribePublications()
	defer remove()

	before, err := s.Snapshot(ctx, application.HistoryPublication{})
	if err != nil {
		t.Fatal(err)
	}

	if err = application.CheckCorrection(ctx, s, d); err != nil {
		t.Fatal(err)
	}

	if tableCount(t, s, "operator_decisions") != 0 || len(changes) != 0 {
		t.Fatal("preview retained a decision or notified")
	}

	unchanged, err := s.Snapshot(
		ctx,
		application.HistoryPublication{BaselineID: before.Snapshot.BaselineID, Version: before.Snapshot.Version},
	)
	if err != nil || unchanged.Delta == nil || unchanged.Delta.Version != unchanged.Delta.From {
		t.Fatal("preview published", err)
	}

	if err = application.SaveDecision(ctx, s, d, "synthetic", time.Now()); err != nil {
		t.Fatal(err)
	}

	if len(changes) != 1 {
		t.Fatal("committed decision did not notify")
	}

	<-changes

	after, err := s.Snapshot(ctx, application.HistoryPublication{})
	if err != nil || after.Snapshot.Version == before.Snapshot.Version {
		t.Fatal("notification preceded publication", err)
	}
	// The original revision is now stale, but the identical submission still succeeds.
	if err = application.SaveDecision(ctx, s, d, "synthetic", time.Now()); err != nil {
		t.Fatal(err)
	}

	if tableCount(t, s, "operator_decisions") != 1 || len(changes) != 0 {
		t.Fatal("identical retry wrote or notified")
	}

	unchanged, err = s.Snapshot(
		ctx,
		application.HistoryPublication{BaselineID: after.Snapshot.BaselineID, Version: after.Snapshot.Version},
	)
	if err != nil || unchanged.Delta == nil || unchanged.Delta.Version != unchanged.Delta.From {
		t.Fatal("identical retry published", err)
	}

	if err = application.SaveDecision(
		ctx,
		s,
		d,
		"different actor",
		time.Now(),
	); !errors.Is(
		err,
		application.ErrStale,
	) {
		t.Fatal(err)
	}

	f := d.Fields()
	f.Submission = rand.Text()

	stale, err := calculation.NewDecision(f)
	if err != nil {
		t.Fatal(err)
	}

	if err = application.SaveDecision(
		ctx,
		s,
		stale,
		"synthetic",
		time.Now(),
	); !errors.Is(
		err,
		application.ErrStale,
	) {
		t.Fatal("save trusted earlier preview", err)
	}
}

func TestActivityCorrectionFailuresRollBackWithoutNotification(t *testing.T) {
	for _, failure := range []string{"callback", "finalization", "journal-base", "journal-version", "journal-changes", "commit"} {
		t.Run(failure, func(t *testing.T) {
			s, d := correctionFixture(t)
			ctx := t.Context()

			before, err := s.Snapshot(ctx, application.HistoryPublication{})
			if err != nil {
				t.Fatal(err)
			}

			beforeForm, err := s.CorrectionForm(ctx, "activity", d.Fields().TargetID)
			if err != nil {
				t.Fatal(err)
			}

			versions := tableCount(t, s, "publication_versions")
			journalChanges := tableCount(t, s, "publication_changes")

			changes, remove := s.SubscribePublications()
			defer remove()

			switch failure {
			case "callback":
				err = s.UpdateActivityCorrection(ctx, func(w application.ActivityCorrectionWriter) error {
					if e := w.InsertDecision(ctx, d.Fields(), d.Fields(), "synthetic", time.Now(), ""); e != nil {
						return e
					}

					return context.Canceled
				})
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			case "finalization":
				if _, err = s.db.Exec(
					`CREATE TRIGGER reject_correction_publication BEFORE UPDATE ON publishing BEGIN SELECT RAISE(ABORT,'synthetic finalization failure'); END`,
				); err != nil {
					t.Fatal(err)
				}

				err = application.SaveDecision(ctx, s, d, "synthetic", time.Now())
			case "journal-base", "journal-version", "journal-changes":
				triggers := map[string]string{
					"journal-base":    "CREATE TRIGGER reject_journal BEFORE INSERT ON publication_versions BEGIN SELECT RAISE(ABORT,'journal base failure'); END",
					"journal-version": "CREATE TRIGGER reject_journal BEFORE INSERT ON publication_versions WHEN NEW.change_count>0 BEGIN SELECT RAISE(ABORT,'journal version failure'); END",
					"journal-changes": "CREATE TRIGGER reject_journal BEFORE INSERT ON publication_changes BEGIN SELECT RAISE(ABORT,'journal changes failure'); END",
				}
				if _, err = s.db.Exec(triggers[failure]); err != nil {
					t.Fatal(err)
				}

				err = application.SaveDecision(ctx, s, d, "synthetic", time.Now())
			case "commit":
				if _, err = s.db.Exec(
					`CREATE TABLE correction_commit_guard(account_id TEXT REFERENCES accounts(id) DEFERRABLE INITIALLY DEFERRED); CREATE TRIGGER reject_correction_commit AFTER INSERT ON operator_decisions BEGIN INSERT INTO correction_commit_guard VALUES('missing-account'); END`,
				); err != nil {
					t.Fatal(err)
				}

				err = application.SaveDecision(ctx, s, d, "synthetic", time.Now())
			}

			if err == nil {
				t.Fatal("failed transaction acknowledged")
			}

			if tableCount(t, s, "operator_decisions") != 0 || len(changes) != 0 {
				t.Fatal("failure retained decisions or notified")
			}

			if tableCount(t, s, "publication_versions") != versions ||
				tableCount(t, s, "publication_changes") != journalChanges {
				t.Fatal("failure changed the publication journal")
			}

			after, err := s.Snapshot(
				ctx,
				application.HistoryPublication{
					BaselineID: before.Snapshot.BaselineID,
					Version:    before.Snapshot.Version,
				},
			)
			if err != nil || after.Delta == nil || after.Delta.Version != after.Delta.From {
				t.Fatal("failure published", err)
			}

			form, err := s.CorrectionForm(ctx, "activity", d.Fields().TargetID)
			if err != nil || form.Revision != beforeForm.Revision ||
				form.Values["quantity"] != beforeForm.Values["quantity"] {
				t.Fatal("failure retained derived state", form, err)
			}
		})
	}
}

func assertCorrectionInputsMatchGraph(t *testing.T, s *Store) {
	t.Helper()

	ctx := t.Context()

	states, err := readCorrectionStates(ctx, s.queries)
	if err != nil {
		t.Fatal(err)
	}

	rows, err := s.queries.Executions(ctx)
	if err != nil {
		t.Fatal(err)
	}

	counter := &endpointCountingDB{DBTX: s.db}

	reader := correctionReader{q: sqlc.New(counter)}
	for _, row := range rows {
		in, err := reader.ActivityInputs(ctx, row.ID, nil)
		if err != nil {
			t.Fatal(err)
		}

		want, ok := states[row.ID]
		if !ok {
			want, err = calculation.NewActivityState(calculation.Accepted, 0, calculation.AutomaticSelection)
			if err != nil {
				t.Fatal(err)
			}
		}

		if in.SourceState != want {
			t.Fatalf("correction input for %s = %+v, graph = %+v", row.ID, in.SourceState, want)
		}
	}

	if counter.fullScans != 0 || counter.endpoints != 0 {
		t.Fatalf(
			"correction input loaded the graph: %d scans, %d endpoint lookups",
			counter.fullScans,
			counter.endpoints,
		)
	}
}
