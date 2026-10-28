// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"bytes"
	"strings"
	"testing"

	"go.trippwill.dev/wake/financial"
	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/transport/admin"
	"go.trippwill.dev/wake/source"
)

func TestCandidateComparisonRetainsNoneditableContractTerms(t *testing.T) {
	s := coverageStore(t)
	coverageReport(t, s, "comparison-contracts", syntheticOption+syntheticDebt)

	view, err := s.Dashboard(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	var optionTrade, debtTrade source.Trade
	for _, e := range view.Executions {
		if e.Terms().Fields().Instrument.Kind() == source.Option {
			optionTrade = e.Terms()
		} else {
			debtTrade = e.Terms()
		}
	}

	date := func(text string) financial.Date {
		value, e := financial.ParseDate(text)
		if e != nil {
			t.Fatal(e)
		}

		return value
	}
	usd, _ := financial.ParseCurrency("USD")
	gbp, _ := financial.ParseCurrency("GBP")
	reference, _ := source.NewReference("synthetic", "security", "delivery", "underlier-one")
	quantity, _ := financial.NewQuantity("12.50000000001")
	security, _ := source.NewSecurityDeliverable(reference, quantity)
	cashValue, _ := financial.NewMoney("0.75000000001", usd)
	cash, _ := source.NewCashDeliverable(cashValue)
	option := optionTrade.Fields().Instrument.Option().Fields()
	option.EffectiveFrom = date("2026-01-01")
	option.Deliverables = []source.Deliverable{security, security, cash}
	debt := source.DebtInput{
		Currency:        usd,
		IssueDate:       date("2020-01-01"),
		DatedDate:       date("2020-01-02"),
		Maturity:        date("2030-01-01"),
		PaymentsPerYear: 2,
	}
	assertComparison := func(base source.Trade, optionTerms source.OptionInput, debtTerms source.DebtInput, field, label, old, newValue string) {
		t.Helper()

		makeValues := func(changed bool) map[string]application.CorrectionValue {
			f := base.Fields()

			instrument := f.Instrument.Fields()
			if instrument.Kind == source.Option {
				terms := option
				if changed {
					terms = optionTerms
				}

				instrument.Option, err = source.NewOptionTerms(terms)
			} else {
				terms := debt
				if changed {
					terms = debtTerms
				}

				instrument.Debt, err = source.NewDebtTerms(terms)
			}

			if err != nil {
				t.Fatal(err)
			}

			f.Instrument, err = source.NewInstrument(instrument)
			if err != nil {
				t.Fatal(err)
			}

			trade, e := source.NewTrade(f)
			if e != nil {
				t.Fatal(e)
			}

			data, e := encodeTrade(trade)
			if e != nil {
				t.Fatal(e)
			}

			values, e := comparisonValues("trade", data)
			if e != nil {
				t.Fatal(e)
			}

			return values
		}

		form := application.CorrectionForm{Fields: []string{"quantity", "price"}, Values: makeValues(false)}
		candidate := application.CorrectionCandidate{Fields: makeValues(true)}

		var body bytes.Buffer
		if e := admin.CandidateComparison(form, candidate).Render(t.Context(), &body); e != nil {
			t.Fatal(e)
		}

		if form.Values[field].Value == candidate.Fields[field].Value {
			t.Error("retained difference omitted", field)
		}

		for _, want := range []string{label, old, newValue, "Different"} {
			if !strings.Contains(body.String(), want) {
				t.Errorf("contract comparison hides %s: %q", field, want)
			}
		}
	}
	changed := option
	changed.EffectiveFrom = date("2026-02-01")
	assertComparison(
		optionTrade,
		changed,
		debt,
		"option.effective_from",
		"Option terms effective from",
		"2026-01-01",
		"2026-02-01",
	)
	changed = option
	quantity, _ = financial.NewQuantity("13.50000000001")
	otherSecurity, _ := source.NewSecurityDeliverable(reference, quantity)
	changed.Deliverables = []source.Deliverable{otherSecurity, cash}
	assertComparison(
		optionTrade,
		changed,
		debt,
		"option.deliverables",
		"Reported option deliverables",
		"12.50000000001",
		"13.50000000001",
	)
	changed = option
	changed.Deliverables = []source.Deliverable{security, cash}
	assertComparison(
		optionTrade,
		changed,
		debt,
		"option.deliverables",
		"Reported option deliverables",
		"12.50000000001",
		"12.50000000001",
	)
	changed = option
	otherRef, _ := source.NewReference("synthetic", "security", "other-delivery", "underlier-two")
	otherSecurity, _ = source.NewSecurityDeliverable(otherRef, quantity)
	changed.Deliverables = []source.Deliverable{otherSecurity, cash}
	assertComparison(
		optionTrade,
		changed,
		debt,
		"option.deliverables",
		"Reported option deliverables",
		"underlier-one",
		"underlier-two",
	)
	changed = option
	cashValue, _ = financial.NewMoney("0.75000000001", gbp)
	otherCash, _ := source.NewCashDeliverable(cashValue)
	changed.Deliverables = []source.Deliverable{security, security, otherCash}
	assertComparison(
		optionTrade,
		changed,
		debt,
		"option.deliverables",
		"Reported option deliverables",
		"0.75000000001 USD",
		"0.75000000001 GBP",
	)

	for _, test := range []struct {
		field, label, old, value string
		change                   func(*source.DebtInput)
	}{
		{"debt.issue_date", "Debt issue date", "2020-01-01", "2021-01-01", func(d *source.DebtInput) { d.IssueDate = date("2021-01-01") }},
		{"debt.dated_date", "Debt dated date", "2020-01-02", "2021-01-02", func(d *source.DebtInput) { d.DatedDate = date("2021-01-02") }},
		{"debt.payments_per_year", "Debt payments per year", "2", "4", func(d *source.DebtInput) { d.PaymentsPerYear = 4 }},
		{"debt.type", "Debt type", "Unknown", "treasury-note", func(d *source.DebtInput) { d.Type = source.TreasuryNote }},
		{"debt.currency", "Debt currency", "USD", "GBP", func(d *source.DebtInput) { d.Currency = gbp }},
	} {
		t.Run(test.field, func(t *testing.T) {
			changed := debt
			test.change(&changed)
			assertComparison(debtTrade, option, changed, test.field, test.label, test.old, test.value)
		})
	}
}
