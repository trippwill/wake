// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package application

import (
	"errors"
	"reflect"
	"slices"
	"strconv"
	"testing"

	"go.trippwill.dev/wake/financial"
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
		Rule:     RetainedClaimsRule,
		Baseline: baseline,
		Claims: []ActivityClaim{
			{Key: "original", Value: baseline.Observation()},
			{Key: "disputed", Value: variant.Observation()},
		},
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

func TestIncompleteAutomaticRateDoesNotBlockIndependentEvidenceChoice(t *testing.T) {
	usd, _ := financial.ParseCurrency("USD")
	eur, _ := financial.ParseCurrency("EUR")
	account, _ := source.NewReference("synthetic", "account", "", "one")
	date, _ := financial.ParseDate("2026-10-20")
	sent, _ := source.NewCurrencyAmount(usd, "10")
	received, _ := source.NewCurrencyAmount(eur, "")

	conversion, err := source.NewConversion(
		source.ConversionInput{
			Location: "synthetic",
			Account:  account,
			Date:     date,
			Sent:     sent,
			Received: received,
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	baseline, _ := ConversionTerms(conversion)
	subject := baseline.Observation().Fields()
	rate, _ := financial.NewPrice("0.9", eur)

	money, _ := financial.NewMoney("1", usd)
	rateClaim := ActivityClaim{
		Key: "rate",
		Value: observationWith(
			t,
			subject,
			asserted(t, source.ActivityFieldInput{Name: source.FieldRate, Price: rate}),
		),
	}
	for _, field := range []source.ActivityFieldName{source.FieldRealizedPnL, source.FieldNet} {
		t.Run(string(field), func(t *testing.T) {
			choice := asserted(t, source.ActivityFieldInput{Name: field, Money: money})
			state, _ := NewActivityState(Accepted, 0, AutomaticSelection)
			in := ActivityCalculationInput{
				Rule: RetainedClaimsRule, Baseline: baseline, SourceState: state,
				Claims:    []ActivityClaim{rateClaim, {Key: "chosen", Value: observationWith(t, subject, choice)}},
				Decisions: []FieldSelection{{Scope: "evidence/" + choice.Key(), Text: "chosen"}},
			}

			got, err := CalculateActivity(in)
			if err != nil {
				t.Fatal("independent choice blocked by incomplete rate", err)
			}

			if got.Terms.Conversion().Fields().Rate.Amount() != "" ||
				!selectedField(t, got.Fields.Fields, field).Fields().Money.Equal(money) ||
				!slices.Contains(got.Fields.Conflicts, "invariants") ||
				len(got.Fields.Sources["rate"]) != 0 {
				t.Fatal("invalid automatic rate or lost choice", got)
			}

			in.Claims = append(
				in.Claims,
				ActivityClaim{
					Key: "base",
					Value: observationWith(
						t,
						subject,
						asserted(t, source.ActivityFieldInput{Name: source.FieldRateBase, Currency: usd}),
					),
				},
			)
			got, err = CalculateActivity(in)
			if err != nil || got.Terms.Conversion().Fields().Rate.Amount() != "0.9" {
				t.Fatal("completed rate was not adopted", got, err)
			}

			in.Claims = in.Claims[:2]
			in.Decisions = append(in.Decisions, FieldSelection{Scope: "evidence/rate", Text: "rate"})
			if _, err = CalculateActivity(in); !errors.Is(err, ErrInvalid) {
				t.Fatal("invalid explicit rate choice accepted", err)
			}
		})
	}
}

func TestFallbackRetainsCoupledRateWhenIndependentChargeExceedsLimit(t *testing.T) {
	usd, _ := financial.ParseCurrency("USD")
	eur, _ := financial.ParseCurrency("EUR")
	account, _ := source.NewReference("synthetic", "account", "", "one")
	date, _ := financial.ParseDate("2026-10-20")
	sent, _ := source.NewCurrencyAmount(usd, "10")
	received, _ := source.NewCurrencyAmount(eur, "")

	money, _ := financial.NewMoney("-1", usd)
	charges := make([]source.Charge, 32)
	for i := range charges {
		var err error

		charges[i], err = source.NewCharge("fee"+strconv.Itoa(i), "component", money)
		if err != nil {
			t.Fatal(err)
		}
	}

	conversion, err := source.NewConversion(
		source.ConversionInput{
			Location: "synthetic",
			Account:  account,
			Date:     date,
			Sent:     sent,
			Received: received,
			Charges:  charges,
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	baseline, _ := ConversionTerms(conversion)
	subject := baseline.Observation().Fields()
	rate, _ := financial.NewPrice("0.9", eur)
	extra, _ := source.NewCharge("overflow", "component", money)

	overflow := asserted(t, source.ActivityFieldInput{Name: source.FieldCharge, Charge: extra})
	claims := []ActivityClaim{
		{
			Key: "rates",
			Value: observationWith(
				t,
				subject,
				asserted(t, source.ActivityFieldInput{Name: source.FieldRate, Price: rate}),
				asserted(t, source.ActivityFieldInput{Name: source.FieldRateBase, Currency: usd}),
			),
		},
		{Key: "overflow", Value: observationWith(t, subject, overflow)},
	}
	state, _ := NewActivityState(Accepted, 0, AutomaticSelection)
	for _, choice := range []string{"", "rate", "rate_base"} {
		t.Run(choice, func(t *testing.T) {
			in := ActivityCalculationInput{
				Rule:        RetainedClaimsRule,
				Baseline:    baseline,
				SourceState: state,
				Claims:      claims,
			}
			if choice != "" {
				in.Decisions = []FieldSelection{{Scope: "evidence/" + choice, Text: "rates"}}
			}

			got, err := CalculateActivity(in)
			if err != nil {
				t.Fatal("independent overflow invalidated a valid rate unit", err)
			}

			terms := got.Terms.Conversion().Fields()
			if terms.Rate.Amount() != "0.9" || terms.RateBase != usd ||
				got.Automatic.Conversion().Fields().Rate.Amount() != "0.9" ||
				len(terms.Charges) != 32 ||
				!slices.Contains(got.Fields.Conflicts, "invariants") ||
				len(got.Fields.Sources[overflow.Key()]) != 0 ||
				slices.ContainsFunc(
					got.Fields.Fields,
					func(f source.ActivityField) bool { return f.Key() == overflow.Key() },
				) {
				t.Fatalf(
					"rate %q, base %q, charges %d, conflicts %v",
					terms.Rate.Amount(),
					terms.RateBase.String(),
					len(terms.Charges),
					got.Fields.Conflicts,
				)
			}
		})
	}
}
