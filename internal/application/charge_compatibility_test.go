// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package application

import (
	"testing"

	"go.trippwill.dev/wake/financial"
	"go.trippwill.dev/wake/source"
)

func TestChargeRolesAllowEnrichmentAndReordering(t *testing.T) {
	usd, _ := financial.ParseCurrency("USD")
	gbp, _ := financial.ParseCurrency("GBP")
	account, _ := source.NewReference("synthetic", "account", "", "one")
	instrumentRef, _ := source.NewReference("synthetic", "instrument", "", "one")
	instrument, _ := source.NewInstrument(source.InstrumentInput{Kind: source.Equity, Reference: instrumentRef})
	quantity, _ := financial.NewQuantity("1")
	price, _ := financial.NewPrice("100", usd)
	date, _ := financial.ParseDate("2026-10-20")
	charge := func(field, scope, amount string, currency financial.Currency) source.Charge {
		money, _ := financial.NewMoney(amount, currency)

		value, err := source.NewCharge(field, scope, money)
		if err != nil {
			t.Fatal(err)
		}

		return value
	}

	total := charge("commission", "total", "-1", usd)
	component := charge("brokerFee", "component", "-0.25", usd)
	trade := func(t *testing.T, charges []source.Charge) source.Trade {
		value, err := source.NewTrade(source.TradeInput{
			Location: "synthetic", Account: account,
			Instrument: instrument, Side: source.Buy, Quantity: quantity, Price: price,
			TradeDate: date, Charges: charges,
		})
		if err != nil {
			t.Fatal(err)
		}

		return value
	}
	for _, tc := range []struct {
		name       string
		a, b       []source.Charge
		compatible bool
	}{
		{"enrichment", []source.Charge{total}, []source.Charge{total, component}, true},
		{"reordered", []source.Charge{total, component}, []source.Charge{component, total}, true},
		{"unknown", nil, []source.Charge{total}, true},
		{"changed amount", []source.Charge{total}, []source.Charge{charge("commission", "total", "-2", usd)}, false},
		{"changed currency", []source.Charge{total}, []source.Charge{charge("commission", "total", "-1", gbp)}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if Compatible(trade(t, tc.a), trade(t, tc.b)) != tc.compatible ||
				Compatible(trade(t, tc.b), trade(t, tc.a)) != tc.compatible {
				t.Fatal("charge role compatibility", tc.name)
			}
		})
	}
}

func TestConversionChargeRolesAllowEnrichment(t *testing.T) {
	usd, _ := financial.ParseCurrency("USD")
	gbp, _ := financial.ParseCurrency("GBP")
	account, _ := source.NewReference("synthetic", "account", "", "one")
	sent, _ := source.NewCurrencyAmount(usd, "1")
	received, _ := source.NewCurrencyAmount(gbp, "")
	date, _ := financial.ParseDate("2026-10-20")
	totalMoney, _ := financial.NewMoney("-1", usd)
	componentMoney, _ := financial.NewMoney("-0.25", gbp)
	changedMoney, _ := financial.NewMoney("-2", usd)
	total, _ := source.NewCharge("commission", "total", totalMoney)
	component, _ := source.NewCharge("brokerFee", "component", componentMoney)
	changed, _ := source.NewCharge("commission", "total", changedMoney)
	makeConversion := func(charges []source.Charge) source.Conversion {
		value, err := source.NewConversion(
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

		return value
	}

	first := makeConversion([]source.Charge{total})
	enriched := makeConversion([]source.Charge{component, total})
	reordered := makeConversion([]source.Charge{total, component})
	if !CompatibleConversion(first, enriched) || !CompatibleConversion(enriched, first) ||
		!CompatibleConversion(enriched, reordered) {
		t.Fatal("compatible reported FX charges became conflicts")
	}

	if CompatibleConversion(first, makeConversion([]source.Charge{changed})) {
		t.Fatal("conflicting FX charge accepted")
	}
}
