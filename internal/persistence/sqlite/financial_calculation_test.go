// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"bytes"
	"crypto/rand"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"go.trippwill.dev/wake/internal/application"
)

func TestCorrectionCheckSaveAndReleaseShareRetainedCalculation(t *testing.T) {
	s := coverageStore(t)
	ctx := t.Context()
	lifecycle := strings.NewReplacer(`execID="option-fill"`, `execID="exercise-fill"`, `tradeID="option-trade"`, `tradeID="exercise-trade"`, `transactionType="ExchTrade"`, `transactionType="BookTrade" code="Ex"`).
		Replace(syntheticOption)
	coverageReport(t, s, "shared-calculation", syntheticOption+syntheticFX+lifecycle)

	rows, err := s.queries.Executions(ctx)
	if err != nil || len(rows) != 3 {
		t.Fatal(rows, err)
	}

	for _, row := range rows {
		t.Run(row.Kind, func(t *testing.T) {
			scope := "quantity"
			if row.Kind == "conversion" {
				scope = "sent_amount"
			}

			form, e := s.CorrectionForm(ctx, "activity", row.ID)
			if e != nil {
				t.Fatal(e)
			}

			decision, e := application.NewDecision(
				application.DecisionInput{
					TargetKind:       "activity",
					TargetID:         row.ID,
					Scope:            scope,
					Text:             "12.00000000000000001",
					Action:           "set",
					Submission:       rand.Text(),
					ExpectedRevision: form.Revision,
				},
			)
			if e != nil {
				t.Fatal(e)
			}

			checkpoint, e := s.PublishCheckpoint(ctx)
			if e != nil {
				t.Fatal(e)
			}

			var before, after int64
			if e = s.db.QueryRow(`SELECT total_changes()`).Scan(&before); e != nil {
				t.Fatal(e)
			}

			if e = s.CheckCorrection(ctx, decision); e != nil {
				t.Fatal("preflight rejected valid retained calculation", e)
			}

			if e = s.db.QueryRow(`SELECT total_changes()`).Scan(&after); e != nil || before != after {
				t.Fatal("preflight performed writes", before, after, e)
			}

			if changes := publishedChanges(t, s, checkpoint); len(changes.Items) != 0 {
				t.Fatal("preflight published", changes)
			}

			if e = s.SaveDecision(ctx, decision, "synthetic", time.Now()); e != nil {
				t.Fatal("save disagreed with preflight", e)
			}

			current, e := s.CorrectionForm(ctx, "activity", row.ID)
			if e != nil || current.Values[scope].Value != "12.00000000000000001" {
				t.Fatal("exact correction lost", current, e)
			}

			release, e := application.NewDecision(
				application.DecisionInput{
					TargetKind:       "activity",
					TargetID:         row.ID,
					Scope:            scope,
					Action:           "release",
					Submission:       rand.Text(),
					ExpectedRevision: current.Revision,
				},
			)
			if e != nil {
				t.Fatal(e)
			}

			if e = s.CheckCorrection(ctx, release); e != nil {
				t.Fatal("release preflight", e)
			}

			if e = s.SaveDecision(ctx, release, "synthetic", time.Now()); e != nil {
				t.Fatal("release save", e)
			}

			restored, e := s.queries.Execution(ctx, row.ID)
			if e != nil || !bytes.Equal(restored.Accepted, row.Accepted) ||
				restored.SelectionAuthority != "automatic" {
				t.Fatal("release changed retained automatic terms", restored, e)
			}
		})
	}
}

func TestSharedCalculationWriteFailureRollsBackDecisionAndPublication(t *testing.T) {
	s := coverageStore(t)
	ctx := t.Context()
	coverageReport(t, s, "atomic-calculation", syntheticOption)

	view, err := s.Dashboard(ctx)
	if err != nil {
		t.Fatal(err)
	}

	form, err := s.CorrectionForm(ctx, "activity", view.Executions[0].ID())
	if err != nil {
		t.Fatal(err)
	}

	decision, err := application.NewDecision(
		application.DecisionInput{
			TargetKind:       "activity",
			TargetID:         form.ID,
			Scope:            "quantity",
			Text:             "12",
			Action:           "set",
			Submission:       rand.Text(),
			ExpectedRevision: form.Revision,
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	if err = s.CheckCorrection(ctx, decision); err != nil {
		t.Fatal(err)
	}

	checkpoint, err := s.PublishCheckpoint(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if _, err = s.db.Exec(
		`CREATE TRIGGER fail_calculation BEFORE UPDATE ON executions BEGIN SELECT RAISE(ABORT,'synthetic write failure'); END;`,
	); err != nil {
		t.Fatal(err)
	}

	if err = s.SaveDecision(ctx, decision, "synthetic", time.Now()); err == nil {
		t.Fatal("failed derived write was acknowledged")
	}

	if tableCount(t, s, "operator_decisions") != 0 {
		t.Fatal("failed calculation retained decision")
	}

	if changes := publishedChanges(t, s, checkpoint); len(changes.Items) != 0 {
		t.Fatal("failed calculation published", changes)
	}
}

func TestCalculationUsesDistinctClaimsAndCanSelectAnyRetainedAppearance(t *testing.T) {
	s := coverageStore(t)
	ctx := t.Context()
	row := equityRow("U1", "same", "100")
	coverageReport(t, s, "original-claim", row)

	for i := range 12 {
		coverageReport(t, s, fmt.Sprint("appearance-", i), strings.Repeat("\n", i+1)+row)
	}

	view, err := s.Dashboard(ctx)
	if err != nil || len(view.Executions) != 1 {
		t.Fatal(view, err)
	}

	id := view.Executions[0].ID()

	inputs, err := s.queries.CalculationInputs(ctx, sql.NullString{String: id, Valid: true})
	if err != nil || len(inputs) != 1 {
		t.Fatal("repeated appearances inflated financial input", len(inputs), err)
	}

	form, err := s.CorrectionForm(ctx, "activity", id)
	if err != nil || len(form.Candidates) != 13 {
		t.Fatal("provenance candidates were discarded", len(form.Candidates), err)
	}

	choice := form.Candidates[len(form.Candidates)-1].Value

	decision, err := application.NewDecision(
		application.DecisionInput{
			TargetKind:       "activity",
			TargetID:         id,
			Scope:            "observation",
			Text:             choice,
			Action:           "set",
			Submission:       rand.Text(),
			ExpectedRevision: form.Revision,
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	if err = s.CheckCorrection(ctx, decision); err != nil {
		t.Fatal("later appearance cannot be checked", err)
	}

	if err = s.SaveDecision(ctx, decision, "synthetic", time.Now()); err != nil {
		t.Fatal("later appearance cannot be selected", err)
	}

	current, err := s.CorrectionForm(ctx, "activity", id)
	if err != nil || len(current.Decisions) != 1 || current.Decisions[0].Text != choice {
		t.Fatal("selected appearance provenance lost", current, err)
	}

	coverageReport(t, s, "new-disputed-claim", strings.Replace(row, `price="100"`, `price="101"`, 1))

	inputs, err = s.queries.CalculationInputs(ctx, sql.NullString{String: id, Valid: true})
	if err != nil || len(inputs) != 2 {
		t.Fatal("genuinely new claim missing", len(inputs), err)
	}

	detail, err := s.ExecutionDetail(ctx, id)
	if err != nil || !detail.Execution.State().NeedsReview() ||
		detail.Execution.State().Authority() != application.OperatorSelection {
		t.Fatal("selection erased dispute or authority", detail, err)
	}
}

func TestSelectingBoundInstrumentPreservesEnrichedListing(t *testing.T) {
	s := coverageStore(t)
	ctx := t.Context()
	reported := equityRow("U1", "enriched-listing", "100")
	coverageReport(t, s, "without-venue", strings.Replace(reported, ` listingExchange="NASDAQ"`, "", 1))

	view, err := s.Dashboard(ctx)
	if err != nil || len(view.Executions) != 1 || view.Executions[0].Listing() != "" {
		t.Fatal(view, err)
	}

	id := view.Executions[0].ID()

	coverageReport(t, s, "with-venue", reported)

	detail, err := s.ExecutionDetail(ctx, id)
	if err != nil || detail.Execution.Listing() == "" {
		t.Fatal("compatible venue did not enrich listing", detail, err)
	}

	listing := detail.Execution.Listing()
	retainDecision(t, s, "activity", id, "instrument", detail.Execution.Instrument(), "", "set")

	detail, err = s.ExecutionDetail(ctx, id)
	if err != nil || detail.Execution.Listing() != listing {
		t.Fatal("reselecting bound instrument erased enriched listing", detail, err)
	}

	retainDecision(t, s, "activity", id, "listing", "", "", "set")

	detail, err = s.ExecutionDetail(ctx, id)
	if err != nil || detail.Execution.Listing() != "" {
		t.Fatal("explicit listing clear was ignored", detail, err)
	}

	retainDecision(t, s, "activity", id, "listing", "", "", "release")

	detail, err = s.ExecutionDetail(ctx, id)
	if err != nil || detail.Execution.Listing() != listing {
		t.Fatal("releasing listing choice did not restore automatic enrichment", detail, err)
	}
}
