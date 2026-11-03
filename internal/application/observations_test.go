// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package application

import (
	"errors"
	"slices"
	"strconv"
	"testing"

	"go.trippwill.dev/wake/financial"
	"go.trippwill.dev/wake/source"
)

func TestConflictingEnrichmentsRetainEverySelectableSource(t *testing.T) {
	base := selectionTrade(t, source.Equity, nil, source.DebtTerms{}).Observation()
	usd, _ := financial.ParseCurrency("USD")
	claims := make([]ActivityClaim, 2048)
	for i := range claims {
		key := strconv.Itoa(i)

		money, err := financial.NewMoney(key, usd)
		if err != nil {
			t.Fatal(err)
		}

		claims[i] = ActivityClaim{Key: key, Value: observationWith(t, base.Fields(), asserted(t,
			source.ActivityFieldInput{Name: source.FieldRealizedPnL, Money: money}))}
	}

	result, err := SelectActivityFields(base.Fields().Fields, claims, nil)
	if err != nil || selectedField(t, result.Fields, source.FieldRealizedPnL).Known() ||
		!slices.Contains(result.Conflicts, "realized_pnl") {
		t.Fatal("conflicting enrichments changed the baseline", result, err)
	}

	last := claims[len(claims)-1].Key
	chosen, err := SelectActivityFields(base.Fields().Fields, claims, map[string]string{"realized_pnl": last})
	if err != nil || selectedField(t, chosen.Fields, source.FieldRealizedPnL).Fields().Money.Amount() != last {
		t.Fatal("later conflicting evidence could not be selected", chosen, err)
	}
}

func asserted(t testing.TB, input source.ActivityFieldInput) source.ActivityField {
	t.Helper()

	v, err := source.NewActivityField(input)
	if err != nil {
		t.Fatal(err)
	}

	return v
}

func observationWith(
	t testing.TB,
	subject source.ActivityObservationInput,
	fields ...source.ActivityField,
) source.ActivityObservation {
	t.Helper()

	subject.Fields = fields

	v, err := source.NewActivityObservation(subject)
	if err != nil {
		t.Fatal(err)
	}

	return v
}

func selectedField(
	t testing.TB,
	fields []source.ActivityField,
	name source.ActivityFieldName,
) source.ActivityField {
	t.Helper()

	for _, f := range fields {
		if f.Name() == name {
			return f
		}
	}

	t.Fatalf("missing %s", name)

	return source.ActivityField{}
}

func TestPartialObservationsCollectivelyEstablishCompleteTerms(t *testing.T) {
	full := selectionTrade(t, source.Equity, nil, source.DebtTerms{}).Observation()

	subject := full.Fields()
	a := observationWith(t, subject, subject.Fields[:3]...)
	b := observationWith(t, subject, subject.Fields[3:]...)
	if _, err := CompleteActivityTerms(a.Fields(), a.Fields().Fields); !errors.Is(err, ErrIncomplete) {
		t.Fatal(err)
	}

	if _, err := CompleteActivityTerms(b.Fields(), b.Fields().Fields); !errors.Is(err, ErrIncomplete) {
		t.Fatal(err)
	}

	result, err := SelectActivityFields(nil, []ActivityClaim{{Key: "a", Value: a}, {Key: "b", Value: b}}, nil)
	if err != nil {
		t.Fatal(err)
	}

	complete, err := CompleteActivityTerms(subject, result.Fields)
	if err != nil || complete.Trade().Fields().Price.Amount() != "100" || len(result.Conflicts) != 0 {
		t.Fatal(complete, result, err)
	}
}

func TestPartialFieldsUseOneBaselineAndIndependentEvidenceSelection(t *testing.T) {
	base := selectionTrade(t, source.Equity, nil, source.DebtTerms{}).Observation()
	usd, _ := financial.ParseCurrency("USD")
	eur, _ := financial.ParseCurrency("EUR")
	a, _ := financial.NewMoney("12.34000000000000001", eur)
	b, _ := financial.NewMoney("13.3400", eur)
	cost, _ := financial.NewMoney("-100.00000000000000001", usd)
	amountDate, _ := financial.ParseDate("2026-10-24")
	costDate, _ := financial.ParseDate("2026-10-23")
	left := observationWith(
		t,
		base.Fields(),
		asserted(t, source.ActivityFieldInput{Name: source.FieldRealizedPnL, Money: a, ReportedOn: amountDate}),
	)
	right := observationWith(
		t,
		base.Fields(),
		asserted(t, source.ActivityFieldInput{Name: source.FieldRealizedPnL, Money: b, ReportedOn: amountDate}),
	)
	allocation := observationWith(
		t,
		base.Fields(),
		asserted(
			t,
			source.ActivityFieldInput{Name: source.FieldReportedCostBasis, Money: cost, ReportedOn: costDate},
		),
	)
	claims := []ActivityClaim{
		{Key: "left", Value: left},
		{Key: "right", Value: right},
		{Key: "cost", Value: allocation},
	}

	result, err := SelectActivityFields(base.Fields().Fields, claims, nil)
	if err != nil {
		t.Fatal(err)
	}

	if selectedField(t, result.Fields, source.FieldRealizedPnL).Known() ||
		!slices.Contains(result.Conflicts, "realized_pnl") {
		t.Fatal("conflicting enrichment chose arrival order", result)
	}

	choices := map[string]string{"realized_pnl": "left", "reported_cost_basis": "cost"}

	selected, err := SelectActivityFields(base.Fields().Fields, claims, choices)
	if err != nil {
		t.Fatal(err)
	}

	amount := selectedField(t, selected.Fields, source.FieldRealizedPnL).Fields()
	basis := selectedField(t, selected.Fields, source.FieldReportedCostBasis).Fields()
	if amount.Money.Amount() != a.Amount() || amount.Money.Currency() != eur ||
		amount.ReportedOn != amountDate ||
		basis.Money.Currency() != usd ||
		basis.ReportedOn != costDate {
		t.Fatal("financial roles were coupled", selected)
	}

	complete, err := CompleteActivityTerms(base.Fields(), selected.Fields)
	if err != nil || complete.Trade().Fields().Price.Amount() != "100" {
		t.Fatal("selecting sparse evidence changed omitted trade fields", complete, err)
	}

	reversed := slices.Clone(claims)
	slices.Reverse(reversed)

	again, err := SelectActivityFields(base.Fields().Fields, reversed, nil)
	if err != nil || selectedField(t, again.Fields, source.FieldRealizedPnL).Known() {
		t.Fatal("later observation order changed unknown baseline", again, err)
	}
}
