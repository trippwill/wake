// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0
package persistence

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/reference"
)

func retainDecision(
	t *testing.T,
	s *Store,
	kind, id, scope, text, currency, action string,
) application.Decision {
	t.Helper()

	form, err := s.CorrectionForm(context.Background(), kind, id)
	if err != nil {
		t.Fatal(err)
	}

	d, err := application.NewDecision(
		application.DecisionInput{
			TargetKind:       kind,
			TargetID:         id,
			Scope:            scope,
			Text:             text,
			Currency:         currency,
			Action:           action,
			Submission:       rand.Text(),
			ExpectedRevision: form.Revision,
			Rationale:        "Synthetic reviewed decision",
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	if err = s.SaveDecision(context.Background(), d, "admin/synthetic-session", time.Now()); err != nil {
		t.Fatal(err)
	}

	return d
}

func TestRetainedTypedDecisionReplayStalenessSupersessionAndRelease(t *testing.T) {
	ctx := context.Background()
	s := coverageStore(t)
	coverageReport(t, s, "original", syntheticOption)

	v, err := s.Dashboard(ctx)
	if err != nil {
		t.Fatal(err)
	}

	id := v.Executions[0].ID()

	d := retainDecision(t, s, "activity", id, "price", "2.12345678900000001", "USD", "set")
	if err = s.SaveDecision(ctx, d, "admin/synthetic-session", time.Now()); err != nil {
		t.Fatal("duplicate submission", err)
	}

	form, err := s.CorrectionForm(ctx, "activity", id)
	if err != nil || len(form.Decisions) != 1 {
		t.Fatal("duplicate decision", form, err)
	}

	stale := d.Fields()
	stale.Submission = rand.Text()
	stale.Text = "3"

	command, err := application.NewDecision(stale)
	if err != nil {
		t.Fatal(err)
	}

	if err = s.SaveDecision(
		ctx,
		command,
		"admin/synthetic-session",
		time.Now(),
	); !errors.Is(
		err,
		application.ErrStale,
	) {
		t.Fatal("stale form accepted", err)
	}

	coverageReport(t, s, "source-repeat", syntheticOption)

	detail, err := s.ExecutionDetail(ctx, id)
	if err != nil || detail.Execution.Terms().Fields().Price.Amount() != "2.12345678900000001" {
		t.Fatal("source replay overwrote correction", detail, err)
	}

	retainDecision(t, s, "activity", id, "price", "3.5", "USD", "set")

	form, err = s.CorrectionForm(ctx, "activity", id)
	if err != nil || len(form.Decisions) != 2 || form.Decisions[1].Supersedes != form.Decisions[0].ID ||
		form.Decisions[0].Active ||
		!form.Decisions[1].Active {
		t.Fatal("supersession", form, err)
	}

	retainDecision(t, s, "activity", id, "price", "", "", "release")

	detail, err = s.ExecutionDetail(ctx, id)
	if err != nil || detail.Execution.Terms().Fields().Price.Amount() != "1.234567890123456789" {
		t.Fatal("release did not restore selection", err)
	}
}

func TestObservationSelectionAndInstrumentNamingSurviveReplayAndReference(t *testing.T) {
	ctx := context.Background()
	s := coverageStore(t)
	coverageReport(t, s, "original", syntheticOption)
	competing := strings.Replace(syntheticOption, `price="1.234567890123456789"`, `price="9.25"`, 1)
	coverageReport(t, s, "competing", competing)

	v, err := s.Dashboard(ctx)
	if err != nil {
		t.Fatal(err)
	}

	id := v.Executions[0].ID()
	instrument := v.Executions[0].Instrument()

	form, err := s.CorrectionForm(ctx, "activity", id)
	if err != nil || len(form.Candidates) != 2 {
		t.Fatal(form, err)
	}

	retainDecision(t, s, "activity", id, "observation", form.Candidates[1].Value, "", "set")
	retainDecision(t, s, "instrument", instrument, "name", "Operator option name", "", "set")

	if err = s.ApplyReference(
		ctx,
		instrument,
		reference.Result{
			Status: "resolved",
			Records: []reference.Record{
				{
					FIGI:          "option-venue",
					Composite:     "option-reference",
					Name:          "Reference name",
					Market:        "Equity",
					SecurityType2: "Option",
				},
			},
			Next: time.Now().Add(time.Hour),
		},
	); err != nil {
		t.Fatal(err)
	}

	coverageReport(t, s, "repeat", syntheticOption)

	d, err := s.ExecutionDetail(ctx, id)
	i, e := s.InstrumentDetail(ctx, instrument)
	if err != nil || e != nil || d.Execution.Terms().Fields().Price.Amount() != "9.25" ||
		i.Instrument.Name() != "Operator option name" {
		t.Fatal("retained decisions overwritten", d, i, err, e)
	}

	retainDecision(t, s, "instrument", instrument, "name", "", "", "release")

	i, err = s.InstrumentDetail(ctx, instrument)
	if err != nil || i.Instrument.Name() != "Reference name" {
		t.Fatal("release lost latest automatic reference", i, err)
	}
}

func TestLinkDecisionPreservesIdentitiesAndRejectsWrongListing(t *testing.T) {
	ctx := context.Background()
	s := coverageStore(t)
	coverageReport(t, s, "original", syntheticOption)
	other := strings.ReplaceAll(
		strings.ReplaceAll(syntheticOption, "option-fill", "other-fill"),
		`conid="200"`,
		`conid="201"`,
	)
	other = strings.ReplaceAll(other, "option-trade", "other-trade")
	coverageReport(t, s, "other", other)

	v, err := s.Dashboard(ctx)
	if err != nil {
		t.Fatal(err)
	}

	original := v.Executions[1]
	target := v.Executions[0]

	retainDecision(t, s, "activity", original.ID(), "instrument", original.Instrument(), "", "set")

	same, e := s.ExecutionDetail(ctx, original.ID())
	if e != nil || same.Execution.Listing() != original.Listing() {
		t.Fatal("same-root decision changed listing", same, e)
	}

	retainDecision(t, s, "activity", original.ID(), "instrument", target.Instrument(), "", "set")
	retainDecision(t, s, "activity", original.ID(), "listing", target.Listing(), "", "set")
	coverageReport(t, s, "repeat", syntheticOption)

	d, err := s.ExecutionDetail(ctx, original.ID())
	if err != nil || d.Execution.ID() != original.ID() || d.Execution.Instrument() != target.Instrument() ||
		d.Execution.Listing() != target.Listing() {
		t.Fatal("link decision repurposed identity", d, err)
	}

	form, err := s.CorrectionForm(ctx, "activity", original.ID())
	if err != nil {
		t.Fatal(err)
	}

	invalid, _ := application.NewDecision(
		application.DecisionInput{
			TargetKind:       "activity",
			TargetID:         original.ID(),
			Scope:            "listing",
			Text:             original.Listing(),
			Action:           "set",
			Submission:       rand.Text(),
			ExpectedRevision: form.Revision,
		},
	)
	if err = s.SaveDecision(ctx, invalid, "admin/synthetic-session", time.Now()); err == nil {
		t.Fatal("listing from wrong root accepted")
	}

	retainDecision(t, s, "activity", original.ID(), "listing", "", "", "release")
	retainDecision(t, s, "activity", original.ID(), "instrument", "", "", "release")

	d, err = s.ExecutionDetail(ctx, original.ID())
	if err != nil || d.Execution.Instrument() != original.Instrument() ||
		d.Execution.Listing() != original.Listing() {
		t.Fatal("link release", d, err)
	}
}

func TestReferenceCandidateSelectionStaysWithinItsMappingJob(t *testing.T) {
	ctx := context.Background()
	s := coverageStore(t)
	coverageReport(t, s, "source", equityRow("U1", "fill", "100"))

	v, err := s.Dashboard(ctx)
	if err != nil {
		t.Fatal(err)
	}

	id := v.Instruments[0].ID()
	body := []byte(
		`[{"data":[{"figi":"candidate-a","shareClassFIGI":"root-a","name":"Candidate A","marketSector":"Equity"},{"figi":"candidate-b","shareClassFIGI":"root-b","name":"Candidate B","marketSector":"Equity"}]},{"data":[{"figi":"unrelated","shareClassFIGI":"unrelated-root","name":"Unrelated","marketSector":"Equity"}]}]`,
	)
	jobs := []reference.Job{
		{IDType: "ID_ISIN", IDValue: "US0000000001"},
		{IDType: "ID_ISIN", IDValue: "UNRELATED"},
	}
	request, _ := json.Marshal(jobs)

	results, err := s.SaveReference(
		ctx,
		reference.Evidence{At: time.Now(), Request: request, Response: body, Status: 200},
		jobs,
		[]reference.Result{
			{Status: "ambiguous", Next: time.Now().Add(time.Hour)},
			{Status: "resolved", Next: time.Now().Add(time.Hour)},
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	if err = s.ApplyReference(ctx, id, results[0]); err != nil {
		t.Fatal(err)
	}

	form, err := s.CorrectionForm(ctx, "instrument", id)
	if err != nil || len(form.Candidates) != 2 {
		t.Fatal("foreign mapping candidates exposed", form, err)
	}

	retainDecision(t, s, "instrument", id, "reference", form.Candidates[1].Value, "", "set")

	i, err := s.InstrumentDetail(ctx, id)
	if err != nil || i.Instrument.Name() != "Candidate B" || i.Instrument.FIGI() != "root-b" {
		t.Fatal("candidate selection", i, err)
	}

	if err = s.ApplyReference(ctx, id, results[0]); err != nil {
		t.Fatal(err)
	}

	i, err = s.InstrumentDetail(ctx, id)
	if err != nil || i.Instrument.Name() != "Candidate B" {
		t.Fatal("refresh overwrote retained candidate", i, err)
	}

	form, err = s.CorrectionForm(ctx, "instrument", id)
	if err != nil {
		t.Fatal(err)
	}

	bad, _ := application.NewDecision(
		application.DecisionInput{
			TargetKind:       "instrument",
			TargetID:         id,
			Scope:            "reference",
			Text:             referenceCandidateKey(results[0].Evidence, 1, 0),
			Action:           "set",
			Submission:       rand.Text(),
			ExpectedRevision: form.Revision,
		},
	)
	if err = s.SaveDecision(ctx, bad, "admin/synthetic-session", time.Now()); err == nil {
		t.Fatal("foreign mapping job selected")
	}
}

func TestUnderlierDecisionAndDecisionRollback(t *testing.T) {
	ctx := context.Background()
	s := coverageStore(t)
	coverageReport(t, s, "option", syntheticOption)

	other := strings.Replace(equityRow("U1", "other-equity", "100"), `conid="100"`, `conid="101"`, 1)
	coverageReport(t, s, "other-underlier", other)

	v, err := s.Dashboard(ctx)
	if err != nil {
		t.Fatal(err)
	}

	var option, underlier string
	for _, i := range v.Instruments {
		if i.Kind() == "option" {
			option = i.ID()
		} else if i.Reported().Reference().Value() == "101" {
			underlier = i.ID()
		}
	}

	original, err := s.InstrumentDetail(ctx, option)
	if err != nil {
		t.Fatal(err)
	}

	retainDecision(t, s, "instrument", option, "underlier", underlier, "", "set")
	coverageReport(t, s, "repeat", syntheticOption)

	d, err := s.InstrumentDetail(ctx, option)
	if err != nil || d.Underlier != underlier {
		t.Fatal("source overwrote underlier decision", d, err)
	}

	retainDecision(t, s, "instrument", option, "underlier", "", "", "release")

	d, err = s.InstrumentDetail(ctx, option)
	if err != nil || d.Underlier != original.Underlier {
		t.Fatal("underlier release", d, err)
	}

	activity := v.Executions[0].ID()

	form, err := s.CorrectionForm(ctx, "activity", activity)
	if err != nil {
		t.Fatal(err)
	}

	if _, err = s.db.Exec(
		`CREATE TRIGGER decision_failure BEFORE UPDATE ON executions BEGIN SELECT RAISE(ABORT,'injected'); END`,
	); err != nil {
		t.Fatal(err)
	}

	command, _ := application.NewDecision(
		application.DecisionInput{
			TargetKind:       "activity",
			TargetID:         activity,
			Scope:            "quantity",
			Text:             "42",
			Action:           "set",
			Submission:       rand.Text(),
			ExpectedRevision: form.Revision,
		},
	)
	if err = s.SaveDecision(ctx, command, "admin/synthetic-session", time.Now()); err == nil {
		t.Fatal("failed projection acknowledged decision")
	}

	form, err = s.CorrectionForm(ctx, "activity", activity)
	if err != nil || len(form.Decisions) != 0 {
		t.Fatal("partial retained decision", form, err)
	}
}

func TestCorrectionComparisonRetainsExactTypedValues(t *testing.T) {
	s := coverageStore(t)
	coverageReport(t, s, "comparison", syntheticOption+syntheticDebt+syntheticFX)

	view, err := s.Dashboard(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	evidence, err := s.EvidenceContext(context.Background(), "comparison")
	if err != nil || evidence.Operation != "acquire" || len(evidence.Observations) < 3 {
		t.Fatal("retained evidence lost context without active access", evidence, err)
	}

	for _, o := range evidence.Observations {
		if o.Location == "" || o.Rule == "" {
			t.Fatal("observation lost source trace", o)
		}
	}

	for _, execution := range view.Executions {
		form, err := s.CorrectionForm(context.Background(), "activity", execution.ID())
		if err != nil {
			t.Fatal(err)
		}

		if execution.Terms().Fields().SettlementDate.IsZero() && form.Values["settlement_date"].Value != "" {
			t.Fatal("absent settlement date became reported text", form.Values["settlement_date"])
		}

		for _, value := range form.Values {
			if strings.Contains(value.Value+value.Currency+value.Unit, "\x00") {
				t.Fatal("invalid zero-value currency leaked into display")
			}
		}

		if len(form.Candidates) == 0 || len(form.Candidates[0].Fields) == 0 {
			t.Fatal("missing typed comparison")
		}

		if value := form.Values["price"]; value.Value != execution.Terms().Fields().Price.Amount() ||
			value.Currency != execution.Terms().Fields().Price.Currency().String() {
			t.Fatal("changed exact price", value)
		}

		if form.Values["quantity"].Unit != execution.Terms().QuantityUnit() {
			t.Fatal("lost units")
		}

		if form.Values["instrument"].Label == "" {
			t.Fatal("missing readable link")
		}
	}

	for _, conversion := range view.Conversions {
		form, err := s.CorrectionForm(context.Background(), "activity", conversion.ID())
		if err != nil {
			t.Fatal(err)
		}

		if form.Values["sent_amount"].Value != conversion.Terms().Fields().Sent.Amount() {
			t.Fatal("conversion amount changed")
		}
	}
}
