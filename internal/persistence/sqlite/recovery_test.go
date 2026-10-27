// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0
package persistence

import (
	"context"
	"crypto/rand"
	"errors"
	"strings"
	"testing"
	"time"

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/reference"
	"go.trippwill.dev/wake/providers/ibkrflex"
)

func TestInterruptedRebuildPreservesIDsDecisionsAndReferenceSchedules(t *testing.T) {
	ctx := context.Background()
	s := coverageStore(t)
	coverageReport(t, s, "families", syntheticOption+syntheticDebt+syntheticFX)
	lifecycle := strings.ReplaceAll(
		strings.ReplaceAll(
			strings.ReplaceAll(syntheticOption, "option-fill", "exercise-fill"),
			"option-trade",
			"exercise-trade",
		),
		`transactionType="ExchTrade"`,
		`transactionType="BookTrade" code="Ex"`,
	)
	coverageReport(t, s, "lifecycle", lifecycle)
	coverageReport(t, s, "original", equityRow("U1", "original", "100"))
	coverageReport(t, s, "replacement", correctionRow("replacement", "Co", "Toriginal"))

	v, err := s.Dashboard(ctx)
	if err != nil {
		t.Fatal(err)
	}

	id := v.Executions[2].ID()
	retainDecision(t, s, "activity", id, "quantity", "42", "", "set")

	before, err := s.Dashboard(ctx)
	if err != nil {
		t.Fatal(err)
	}

	next := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	jobs := []reference.Job{{IDType: "ID_ISIN", IDValue: "US0000000001"}}
	if _, err = s.SaveReference(
		ctx,
		reference.Evidence{
			At:       time.Now(),
			Request:  []byte(`[{"idType":"ID_ISIN","idValue":"US0000000001"}]`),
			Response: []byte(`[{"warning":"No identifier found."}]`),
			Status:   200,
		},
		jobs,
		[]reference.Result{{Status: "no-match", Next: next}},
	); err != nil {
		t.Fatal(err)
	}

	if _, _, err = s.ReserveReference(ctx, time.Now(), 25); err != nil {
		t.Fatal(err)
	}

	for _, i := range before.Instruments {
		if err = s.ApplyReference(ctx, i.ID(), reference.Result{Status: "no-match", Next: next}); err != nil {
			t.Fatal(err)
		}
	}

	refs, err := s.queries.Instruments(ctx)
	if err != nil {
		t.Fatal(err)
	}

	budget, err := s.queries.ReferenceBudget(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Corrupt only disposable accepted fields, retaining authoritative inputs.
	if _, err = s.db.Exec(
		`UPDATE executions SET status='damaged'; UPDATE instruments SET name='damaged'`,
	); err != nil {
		t.Fatal(err)
	}

	submission := rand.Text()

	job, err := s.StartRebuild(ctx, submission, "admin/synthetic-session", time.Now())
	if err != nil {
		t.Fatal(err)
	}

	repeated, err := s.StartRebuild(ctx, submission, "admin/synthetic-session", time.Now())
	if err != nil || repeated != job {
		t.Fatal("repeated rebuild submission", err)
	}

	changed, err := s.RunRecovery(ctx)
	if err != nil || !changed {
		t.Fatal(err)
	}

	progress, err := s.queries.RecoveryJob(ctx, job)
	if err != nil || progress.Completed == 0 || progress.Completed >= progress.Total {
		t.Fatal("rebuild was not bounded", progress, err)
	}
	// A cancelled step leaves committed progress and other groups' last views intact.
	cancelled, cancel := context.WithCancel(ctx)
	cancel()

	if _, err = s.RunRecovery(cancelled); err == nil {
		t.Fatal("cancelled rebuild acknowledged")
	}

	unchanged, err := s.queries.RecoveryJob(ctx, job)
	if err != nil || unchanged.Completed != progress.Completed {
		t.Fatal("cancelled progress advanced", err)
	}

	path := s.owners[0].Name()

	if err = s.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(
		ctx,
		testProfile(path),
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()

	for n := 0; n < 100; n++ {
		changed, e := reopened.RunRecovery(ctx)
		if e != nil {
			t.Fatal(e)
		}

		if !changed {
			break
		}

		if n == 99 {
			t.Fatal("rebuild failed to finish")
		}
	}

	after, err := reopened.Dashboard(ctx)
	if err != nil || len(after.Executions) != len(before.Executions) || len(after.Conversions) != 1 ||
		len(after.Lifecycles) != 1 ||
		after.Lifecycles[0].ID() != before.Lifecycles[0].ID() {
		t.Fatal("rebuild lost activity", after, err)
	}

	for n, e := range before.Executions {
		if after.Executions[n].ID() != e.ID() || after.Executions[n].Instrument() != e.Instrument() {
			t.Fatal("reallocated identity")
		}
	}

	d, err := reopened.ExecutionDetail(ctx, id)
	if err != nil || d.Execution.Terms().Fields().Quantity.Value() != "42" {
		t.Fatal("decision lost", d, err)
	}

	for _, old := range refs {
		now, e := reopened.queries.Instrument(ctx, old.ID)
		if e != nil || now.ReferenceNext != old.ReferenceNext || now.Name == "damaged" {
			t.Fatal("reference schedule or projection lost", now, e)
		}
	}

	current, e := reopened.queries.ReferenceBudget(ctx)
	if e != nil || current != budget {
		t.Fatal("rebuild reset reference budget", e)
	}

	if _, ok, e := reopened.ReferenceCached(ctx, jobs[0].Key(), next.Add(-time.Second)); e != nil || !ok {
		t.Fatal("negative cache lost", e)
	}

	final, err := reopened.queries.RecoveryJob(ctx, job)
	if err != nil || final.Status != "complete" || final.Completed != final.Total {
		t.Fatal(final, err)
	}
}

func TestRecoveryGroupRollbackRetryAndProcessingRetry(t *testing.T) {
	ctx := context.Background()
	s := coverageStore(t)
	coverageReport(t, s, "original", equityRow("U1", "original", "100"))
	coverageReport(t, s, "replacement", correctionRow("replacement", "Co", "Toriginal"))

	if _, err := s.db.Exec(
		`UPDATE executions SET status='damaged'; CREATE TRIGGER recovery_failure BEFORE UPDATE ON executions WHEN NEW.status='superseded' BEGIN SELECT RAISE(ABORT,'injected'); END`,
	); err != nil {
		t.Fatal(err)
	}

	job, err := s.StartRebuild(ctx, rand.Text(), "admin/synthetic-session", time.Now())
	if err != nil {
		t.Fatal(err)
	}

	if _, err = s.RunRecovery(ctx); err == nil {
		t.Fatal("storage failure acknowledged")
	}

	rows, err := s.queries.Executions(ctx)
	if err != nil {
		t.Fatal(err)
	}

	for _, r := range rows {
		if r.Status != "damaged" {
			t.Fatal("relationship group partially committed", rows)
		}
	}

	progress, err := s.queries.RecoveryJob(ctx, job)
	if err != nil || progress.Status != "failed" || progress.Completed != 0 {
		t.Fatal(progress, err)
	}

	if _, err = s.db.Exec(`DROP TRIGGER recovery_failure`); err != nil {
		t.Fatal(err)
	}

	if err = s.RetryRecovery(ctx, job); err != nil {
		t.Fatal(err)
	}

	for n := 0; n < 20; n++ {
		changed, e := s.RunRecovery(ctx)
		if e != nil {
			t.Fatal(e)
		}

		if !changed {
			break
		}
	}

	if err = s.RetryProcessing(ctx, "original"); err != nil {
		t.Fatal(err)
	}

	if err = s.Process(ctx, "original", ibkrflex.Version); err != nil {
		t.Fatal(err)
	}

	view, err := s.Dashboard(ctx)
	if err != nil || len(view.Executions) != 2 || view.Executions[1].Status() != "superseded" {
		t.Fatal("retry duplicated or erased correction", view, err)
	}

	var count int
	if err = s.db.QueryRow(`SELECT count(*) FROM execution_observations WHERE artifact_id='original'`).
		Scan(&count); err != nil ||
		count != 2 {
		t.Fatal("retry duplicated provenance", count, err)
	}

	if err = s.RetryProcessing(ctx, "discovery"); err == nil {
		t.Fatal("discovery processing retry accepted")
	}
}

func TestRebuildUsesCurrentDecisionAtEachCommittedGroup(t *testing.T) {
	ctx := context.Background()
	s := coverageStore(t)
	coverageReport(t, s, "families", syntheticOption+syntheticDebt)

	view, err := s.Dashboard(ctx)
	if err != nil {
		t.Fatal(err)
	}

	_, err = s.StartRebuild(ctx, rand.Text(), "admin/synthetic-session", time.Now())
	if err != nil {
		t.Fatal(err)
	}

	retainDecision(t, s, "activity", view.Executions[0].ID(), "quantity", "37", "", "set")

	for n := 0; n < 20; n++ {
		changed, e := s.RunRecovery(ctx)
		if e != nil {
			t.Fatal(e)
		}

		if !changed {
			break
		}
	}

	d, err := s.ExecutionDetail(ctx, view.Executions[0].ID())
	if err != nil || d.Execution.Terms().Fields().Quantity.Value() != "37" {
		t.Fatal("rebuild used stale decision snapshot", d, err)
	}

	before := d.Execution.Revision()

	if _, err = s.StartRebuild(ctx, rand.Text(), "admin/synthetic-session", time.Now()); err != nil {
		t.Fatal(err)
	}

	for n := 0; n < 20; n++ {
		changed, e := s.RunRecovery(ctx)
		if e != nil {
			t.Fatal(e)
		}

		if !changed {
			break
		}
	}

	d, err = s.ExecutionDetail(ctx, view.Executions[0].ID())
	if err != nil || d.Execution.Revision() != before {
		t.Fatal("unchanged rebuild advanced revision", err)
	}
}

var _ application.Store = (*Store)(nil)

func TestSupersededRebuildCannotRetry(t *testing.T) {
	ctx := t.Context()
	s := coverageStore(t)
	coverageReport(t, s, "initial", syntheticOption)

	first, err := s.StartRebuild(ctx, rand.Text(), "test", time.Now())
	if err != nil {
		t.Fatal(err)
	}

	if err = s.queries.FailRecovery(ctx, first); err != nil {
		t.Fatal(err)
	}

	second, err := s.StartRebuild(ctx, rand.Text(), "test", time.Now())
	if err != nil {
		t.Fatal(err)
	}

	if err = s.queries.FailRecovery(ctx, second); err != nil {
		t.Fatal(err)
	}

	if err = s.RetryRecovery(ctx, first); !errors.Is(err, application.ErrInvalid) {
		t.Fatal("superseded retry accepted", err)
	}

	if err = s.RetryRecovery(ctx, second); err != nil {
		t.Fatal(err)
	}

	for range 100 {
		changed, e := s.RunRecovery(ctx)
		if e != nil {
			t.Fatal(e)
		}

		if !changed {
			break
		}
	}

	if err = s.RetryRecovery(ctx, first); !errors.Is(err, application.ErrInvalid) {
		t.Fatal("completed newer rebuild permits superseded retry", err)
	}
}
