// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package application

import (
	"testing"

	"go.trippwill.dev/wake/financial"
	"go.trippwill.dev/wake/source"
)

func TestChargeHierarchyContributionsAndIndependentSelection(t *testing.T) {
	usd, _ := financial.ParseCurrency("USD")
	makeCharge := func(category source.ChargeCategory, field, amount string, children ...source.Charge) source.Charge {
		t.Helper()

		var money financial.Money
		if amount != "" {
			money, _ = financial.NewMoney(amount, usd)
		}

		c, err := source.NewCharge(
			source.ChargeInput{Category: category, Field: field, Total: money, Components: children},
		)
		if err != nil {
			t.Fatal(err)
		}

		return c
	}
	total := makeCharge(source.Commission, "commission", "-1.0000")
	fee := makeCharge(source.RegulatoryFee, "taf", "-0.00100")
	other := makeCharge(source.RegulatoryFee, "other", "0.0000")
	reg := makeCharge(source.RegulatoryFee, "regulatory", "", fee)
	enriched := makeCharge(source.Commission, "commission", "-1.00", reg)
	reordered := makeCharge(
		source.Commission,
		"commission",
		"-1",
		makeCharge(source.RegulatoryFee, "regulatory", "", other, fee),
	)
	trade := func(c source.Charge) source.Trade {
		return selectionTrade(t, source.Equity, []source.Charge{c}, source.DebtTerms{})
	}
	base := trade(total).Observation()
	withFee := trade(enriched).Observation()
	withOther := trade(reordered).Observation()

	prior := []source.ActivityObservation{base}
	if got := ObservationContribution(prior, withFee); got != ObservationEnriched {
		t.Fatal("new nested fee discarded", got)
	}

	prior = append(prior, withFee, withOther)
	for _, observation := range []source.ActivityObservation{base, withFee, withOther} {
		if got := ObservationContribution(prior, observation); got != "" {
			t.Fatal("known breakdown added support", got)
		}
	}

	claims := []ActivityClaim{
		{Key: "base", Value: base},
		{Key: "fee", Value: withFee},
		{Key: "other", Value: withOther},
	}
	changed := makeCharge(
		source.Commission,
		"commission",
		"-1",
		makeCharge(source.RegulatoryFee, "regulatory", "", makeCharge(source.RegulatoryFee, "taf", "-0.00200")),
	)
	claims = append(claims, ActivityClaim{Key: "conflict", Value: trade(changed).Observation()})

	selected, err := SelectActivityFields(withFee.Fields().Fields, claims, nil)
	if err != nil || len(selected.Conflicts) != 1 ||
		selected.Conflicts[0] != "charge/commission/regulatory/taf" {
		t.Fatal(selected, err)
	}

	terms, err := CompleteActivityTerms(withFee.Fields(), selected.Fields)
	if err != nil {
		t.Fatal(err)
	}

	root := terms.Trade().Fields().Charges[0]
	if root.Total().Amount() != "-1.00" || len(root.Components()[0].Components()) != 2 {
		t.Fatal("component conflict erased total or independent enrichment", root)
	}

	if root.Components()[0].Components()[1].Total().Amount() != "-0.00100" {
		t.Fatal("baseline component changed", root)
	}

	merged, conflict, err := SelectTrades(trade(total), []source.Trade{trade(enriched)})
	if err != nil || conflict || len(merged.Fields().Charges[0].Components()) != 1 {
		t.Fatal("whole-trade enrichment lost hierarchy", merged, conflict, err)
	}
}

func TestChargeCompatibilityIncludesSharedAncestorCategories(t *testing.T) {
	usd, _ := financial.ParseCurrency("USD")
	money, _ := financial.NewMoney("-0.00100", usd)

	charge := func(category source.ChargeCategory, field string, child ...source.Charge) source.Charge {
		t.Helper()

		input := source.ChargeInput{Category: category, Field: field, Components: child}
		if len(child) == 0 {
			input.Total = money
		}

		value, err := source.NewCharge(input)
		if err != nil {
			t.Fatal(err)
		}

		return value
	}
	left := charge(source.RegulatoryFee, "subtotal", charge(source.RegulatoryFee, "a"))
	base := selectionTrade(t, source.Equity, []source.Charge{left}, source.DebtTerms{})
	for _, category := range []source.ChargeCategory{source.RegulatoryFee, source.OtherFee} {
		t.Run(string(category), func(t *testing.T) {
			right := charge(category, "subtotal", charge(source.RegulatoryFee, "b"))

			incoming := selectionTrade(t, source.Equity, []source.Charge{right}, source.DebtTerms{})
			want := category == source.RegulatoryFee
			if Compatible(base, incoming) != want || Compatible(incoming, base) != want {
				t.Error("trade compatibility ignored shared ancestor categories")
			}

			if CompatibleObservations(base.Observation(), incoming.Observation()) != want ||
				CompatibleObservations(incoming.Observation(), base.Observation()) != want {
				t.Error("observation compatibility ignored shared ancestor categories")
			}
		})
	}
}
