// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"context"
	"crypto/rand"
	"errors"
	"testing"

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/source"
)

func TestCorrectionChecksUseFinancialRulesWithoutWriting(t *testing.T) {
	s := coverageStore(t)
	coverageReport(t, s, "check", syntheticOption+syntheticDebt+syntheticFX)

	ctx := context.Background()

	view, err := s.Dashboard(ctx)
	if err != nil {
		t.Fatal(err)
	}

	var id, other string
	for _, e := range view.Executions {
		if e.Terms().Fields().Instrument.Kind() == source.Option {
			id = e.ID()
		} else {
			other = e.Instrument()
		}
	}

	form, err := s.CorrectionForm(ctx, "activity", id)
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		scope, value, currency string
		valid                  bool
	}{
		{"price", "9007199254740993.0000000000001", "USD", true},
		{"price", "not a price", "USD", false},
		{"price", "1.2", "usd", false},
		{"quantity", "0", "", false},
		{"quantity", "-1", "", false},
		{"quantity", "0.0000000000000001", "", true},
		{"trade_date", "2026-02-30", "", false},
		{"trade_date", "2028-02-29", "", true},
		{"executed_at", "2026-10-20T13:30:00.120000000Z", "", true},
		{"executed_at", "2026-10-20T13:30:00.1200000000Z", "", false},
		{"option.right", "sell", "", false},
		{"option.right", "put", "", true},
		{"instrument", other, "", false},
		{"instrument", form.Values["instrument"].Value, "", true},
		{"listing", "", "", true},
	} {
		command, e := application.NewDecision(
			application.DecisionInput{
				TargetKind:       "activity",
				TargetID:         id,
				Scope:            test.scope,
				Text:             test.value,
				Currency:         test.currency,
				Action:           "set",
				Submission:       rand.Text(),
				ExpectedRevision: form.Revision,
			},
		)
		if e != nil {
			t.Fatal(e)
		}

		e = s.CheckCorrection(ctx, command)
		if (e == nil) != test.valid {
			t.Errorf("%s=%s: %v", test.scope, test.value, e)
		}
	}

	for _, link := range form.Links {
		if link.Scope == "instrument" && link.Value != form.Values["instrument"].Value {
			t.Fatal("incompatible family offered", link)
		}
	}

	current, err := s.CorrectionForm(ctx, "activity", id)
	if err != nil || current.Revision != form.Revision || len(current.Decisions) != 0 {
		t.Fatal("validation wrote state", current, err)
	}

	stale, _ := application.NewDecision(
		application.DecisionInput{
			TargetKind:       "activity",
			TargetID:         id,
			Scope:            "price",
			Text:             "1",
			Currency:         "USD",
			Action:           "set",
			Submission:       rand.Text(),
			ExpectedRevision: form.Revision + 1,
		},
	)
	if err = s.CheckCorrection(ctx, stale); !errors.Is(err, application.ErrStale) {
		t.Fatal("stale check accepted", err)
	}

	underlier, err := s.CorrectionForm(ctx, "instrument", form.Values["instrument"].Value)
	if err != nil {
		t.Fatal(err)
	}

	roots := map[string]string{}
	for _, i := range view.Instruments {
		roots[i.ID()] = i.Kind()
	}

	for _, link := range underlier.Links {
		if link.Scope != "underlier" || (roots[link.Value] != "equity" && roots[link.Value] != "fund") {
			t.Fatal("incompatible underlier offered", link)
		}
	}
}
