// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"bytes"
	"crypto/rand"
	"errors"
	"strings"
	"testing"

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/transport/admin"
)

func TestInstrumentPreflightRespectsActiveListingDecision(t *testing.T) {
	s := coverageStore(t)
	coverageReport(t, s, "original", syntheticOption)
	other := strings.NewReplacer("option-fill", "other-fill", "option-trade", "other-trade", `conid="200"`, `conid="201"`).
		Replace(syntheticOption)
	coverageReport(t, s, "other", other)

	view, err := s.Dashboard(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	a, b := view.Executions[0], view.Executions[1]
	retainDecision(t, s, "activity", a.ID(), "listing", a.Listing(), "", "set")

	check := func(wantValid bool) {
		t.Helper()

		form, e := s.CorrectionForm(t.Context(), "activity", a.ID())
		if e != nil {
			t.Fatal(e)
		}

		decision, e := application.NewDecision(
			application.DecisionInput{
				TargetKind:       "activity",
				TargetID:         a.ID(),
				Scope:            "instrument",
				Text:             b.Instrument(),
				Action:           "set",
				Submission:       rand.Text(),
				ExpectedRevision: form.Revision,
			},
		)
		if e != nil {
			t.Fatal(e)
		}

		e = s.CheckCorrection(t.Context(), decision)
		if (e == nil) != wantValid || (!wantValid && !errors.Is(e, application.ErrInvalid)) {
			t.Fatal("preflight disagrees with dependent listing constraint", e)
		}
	}
	check(false)
	retainDecision(t, s, "activity", a.ID(), "listing", "", "", "release")
	check(true)
	retainDecision(t, s, "activity", a.ID(), "instrument", b.Instrument(), "", "set")

	detail, err := s.ExecutionDetail(t.Context(), a.ID())
	if err != nil || detail.Execution.Instrument() != b.Instrument() || detail.Execution.Listing() != "" {
		t.Fatal("released listing did not permit instrument correction", detail, err)
	}
}

func TestCandidateComparisonIncludesReadOnlyChargeRoles(t *testing.T) {
	for _, test := range []struct{ name, row string }{{"trade", syntheticOption}, {"conversion", syntheticFX}} {
		t.Run(test.name, func(t *testing.T) {
			s := coverageStore(t)

			row := strings.ReplaceAll(test.row, `commissionCurrency="GBP"`, `commissionCurrency="USD"`)
			if !strings.Contains(row, "commissionCurrency=") {
				row = strings.ReplaceAll(row, `/>`, ` commissionCurrency="USD"/>`)
			}

			first := strings.ReplaceAll(row, `/>`, ` brokerExecutionCommission="-0.25000000001"/>`)
			second := strings.ReplaceAll(
				first,
				`brokerExecutionCommission="-0.25000000001"`,
				`brokerExecutionCommission="-0.75000000001"`,
			)
			coverageReport(t, s, "charge-first", first)
			coverageReport(t, s, "charge-second", second)

			view, err := s.Dashboard(t.Context())
			if err != nil {
				t.Fatal(err)
			}

			id := ""
			if test.name == "trade" {
				id = view.Executions[0].ID()
			} else {
				id = view.Conversions[0].ID()
			}

			form, err := s.CorrectionForm(t.Context(), "activity", id)
			if err != nil || len(form.Candidates) != 2 {
				t.Fatal(form, err)
			}

			var rendered bytes.Buffer
			for _, candidate := range form.Candidates {
				if err = admin.CandidateComparison(form, candidate).Render(t.Context(), &rendered); err != nil {
					t.Fatal(err)
				}
			}

			for _, want := range []string{"Attached charges", "brokerExecutionCommission", "component", "-0.25000000001 USD", "-0.75000000001 USD", "Different"} {
				if !strings.Contains(rendered.String(), want) {
					t.Errorf("charge-only comparison hides %q", want)
				}
			}

			for _, field := range form.Fields {
				if field == "charges" {
					t.Fatal("read-only charges offered as editable field")
				}
			}
		})
	}
}
