// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package application

import (
	"fmt"
	"reflect"
	"testing"

	"go.trippwill.dev/wake/financial"
	"go.trippwill.dev/wake/source"
)

func selectionTrade(
	t *testing.T,
	kind source.InstrumentKind,
	charges []source.Charge,
	debt source.DebtTerms,
) source.Trade {
	t.Helper()

	usd, _ := financial.ParseCurrency("USD")
	account, _ := source.NewReference("synthetic", "account", "", "one")
	ref, _ := source.NewReference("synthetic", "instrument", "", "one")

	instrument, err := source.NewInstrument(source.InstrumentInput{Reference: ref, Kind: kind, Debt: debt})
	if err != nil {
		t.Fatal(err)
	}

	quantity, _ := financial.NewQuantity("1")
	price, _ := financial.NewPrice("100", usd)
	date, _ := financial.ParseDate("2026-10-20")

	v, err := source.NewTrade(
		source.TradeInput{
			Location:   "synthetic",
			Account:    account,
			Instrument: instrument,
			Quantity:   quantity,
			Price:      price,
			Side:       source.Buy,
			TradeDate:  date,
			Charges:    charges,
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	return v
}

func TestSelectionMergesChargeRolesAndRetainsOverflow(t *testing.T) {
	usd, _ := financial.ParseCurrency("USD")
	gbp, _ := financial.ParseCurrency("GBP")
	money, _ := financial.NewMoney("-1", usd)
	charge := func(field string) source.Charge {
		c, e := source.NewCharge(field, "component", money)
		if e != nil {
			t.Fatal(e)
		}

		return c
	}
	a, b := charge("a"), charge("b")

	base := selectionTrade(t, source.Equity, []source.Charge{b}, source.DebtTerms{})
	extra := selectionTrade(t, source.Equity, []source.Charge{a, b}, source.DebtTerms{})
	selected, conflict, err := SelectTrades(base, []source.Trade{extra})
	if err != nil || conflict || !reflect.DeepEqual(selected.Fields().Charges, []source.Charge{a, b}) {
		t.Fatal("trade charge enrichment", selected.Fields().Charges, conflict, err)
	}

	sent, _ := source.NewCurrencyAmount(usd, "1")
	received, _ := source.NewCurrencyAmount(gbp, "")
	conversion := func(charges []source.Charge) source.Conversion {
		v, e := source.NewConversion(
			source.ConversionInput{
				Location: "synthetic",
				Account:  base.Fields().Account,
				Date:     base.Fields().TradeDate,
				Sent:     sent,
				Received: received,
				Charges:  charges,
			},
		)
		if e != nil {
			t.Fatal(e)
		}

		return v
	}

	fx, disagreement, e := SelectConversions(
		conversion([]source.Charge{b}),
		[]source.Conversion{conversion([]source.Charge{a, b})},
	)
	if e != nil || disagreement || !reflect.DeepEqual(fx.Fields().Charges, []source.Charge{a, b}) {
		t.Fatal("FX charge enrichment", fx.Fields().Charges, disagreement, e)
	}

	full := make([]source.Charge, 32)
	for i := range full {
		full[i] = charge(fmt.Sprintf("role-%02d", i))
	}

	original := selectionTrade(t, source.Equity, full, source.DebtTerms{})
	incoming := selectionTrade(t, source.Equity, []source.Charge{a}, source.DebtTerms{})
	selected, conflict, err = SelectTrades(original, []source.Trade{incoming})
	if err != nil || !conflict || !reflect.DeepEqual(selected.Fields(), original.Fields()) {
		t.Fatal("bounded trade union did not preserve baseline with disagreement", conflict, err)
	}

	fx, disagreement, e = SelectConversions(
		conversion(full),
		[]source.Conversion{conversion([]source.Charge{a})},
	)
	if e != nil || !disagreement || !reflect.DeepEqual(fx.Fields().Charges, full) {
		t.Fatal("bounded FX union did not preserve baseline", disagreement, e)
	}
}

func TestDebtCombinedInvariantsBecomeDisagreement(t *testing.T) {
	issued, _ := financial.ParseDate("2026-10-20")
	matured, _ := financial.ParseDate("2026-10-19")

	a, err := source.NewDebtTerms(source.DebtInput{IssueDate: issued})
	if err != nil {
		t.Fatal(err)
	}

	b, err := source.NewDebtTerms(source.DebtInput{Maturity: matured})
	if err != nil {
		t.Fatal(err)
	}

	if CompatibleDebt(a, b) || CompatibleDebt(b, a) {
		t.Fatal("invalid combined debt chronology compatible")
	}

	baseline := selectionTrade(t, source.Debt, nil, a)
	incoming := selectionTrade(t, source.Debt, nil, b)
	selected, conflict, err := SelectTrades(baseline, []source.Trade{incoming})
	if err != nil || !conflict || !reflect.DeepEqual(selected.Fields(), baseline.Fields()) {
		t.Fatal("invalid combination blocked processing or changed baseline", conflict, err)
	}
}
