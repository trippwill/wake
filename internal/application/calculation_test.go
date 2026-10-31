// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package application

import (
	"reflect"
	"testing"

	"go.trippwill.dev/wake/source"
)

func TestFinancialCalculationIsDeterministicAndKeepsSourceDisagreement(t *testing.T) {
	base := selectionTrade(t, source.Equity, nil, source.DebtTerms{})

	baseline, err := TradeTerms(base)
	if err != nil {
		t.Fatal(err)
	}

	disputed, err := ApplyTradeField(base, "quantity", "10.00000000000000001", "")
	if err != nil {
		t.Fatal(err)
	}

	variant, err := TradeTerms(disputed)
	if err != nil {
		t.Fatal(err)
	}

	state, _ := NewActivityState(Cancelled, 0, AutomaticSelection)
	in := ActivityCalculationInput{
		Rule:        RetainedClaimsRule,
		Baseline:    baseline,
		Claims:      []ActivityClaim{{Key: "original", Terms: baseline}, {Key: "disputed", Terms: variant}},
		Binding:     ActivityBinding{Instrument: "root"},
		SourceState: state,
		Decisions: []FieldSelection{
			{Scope: "quantity", Text: "12.00000000000000001"},
			{Scope: "executed_at", Text: "2026-10-20T13:30:00.120000000Z"},
		},
	}
	original := append([]FieldSelection(nil), in.Decisions...)

	result, err := CalculateActivity(in)
	if err != nil {
		t.Fatal(err)
	}

	again, err := CalculateActivity(in)
	if err != nil || !reflect.DeepEqual(result, again) || !reflect.DeepEqual(original, in.Decisions) {
		t.Fatal("calculation changed input or depends on prior calls", err)
	}

	if result.Terms.Trade().Fields().Quantity.Value() != "12.00000000000000001" ||
		result.Terms.Trade().Fields().ExecutedAt.String() != "2026-10-20T13:30:00.120000000Z" ||
		result.State.Lifecycle() != Cancelled ||
		result.State.ReviewReasons()&ConflictingClaims == 0 ||
		result.State.Authority() != OperatorSelection {
		t.Fatal("independent financial meaning lost", result)
	}

	in.Decisions = nil

	automatic, err := CalculateActivity(in)
	if err != nil || automatic.Terms.Trade().Fields().Quantity.Value() != base.Fields().Quantity.Value() ||
		automatic.State.Authority() != AutomaticSelection {
		t.Fatal("release did not recompute retained automatic baseline", automatic, err)
	}

	in.Rule = "unimplemented-rules"
	if _, err = CalculateActivity(in); err == nil {
		t.Fatal("unknown rule selection accepted")
	}
}
