// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package application

import (
	"reflect"
	"testing"

	"go.trippwill.dev/wake/financial"
	"go.trippwill.dev/wake/source"
)

func TestTradeCorrectionsPreserveExactValuesAndOtherFacts(t *testing.T) {
	usd, _ := financial.ParseCurrency("USD")
	account, _ := source.NewReference("synthetic", "account", "", "one")
	ref, _ := source.NewReference("synthetic", "instrument", "", "one")
	debt, _ := source.NewDebtTerms(source.DebtInput{Type: source.TreasuryNote, Currency: usd})
	instrument, _ := source.NewInstrument(source.InstrumentInput{Reference: ref, Kind: source.Debt, Debt: debt})
	quantity, _ := financial.NewQuantity("1")
	price, _ := financial.NewPrice("100", usd)
	date, _ := financial.ParseDate("2026-10-20")

	base, err := source.NewTrade(
		source.TradeInput{
			Location:   "synthetic",
			Account:    account,
			Instrument: instrument,
			Quantity:   quantity,
			Price:      price,
			Side:       source.Buy,
			TradeDate:  date,
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	q, _ := financial.NewQuantity("2.00000000001")
	p, _ := financial.NewPrice("99.00000000001", usd)
	money, _ := financial.NewMoney("-123.00000000001", usd)
	next, _ := financial.ParseDate("2026-10-21")
	at, _ := financial.ParseTimestamp("2026-10-20T09:30:00.120Z")

	cases := []struct {
		scope, text, currency string
		change                func(*source.TradeInput)
	}{
		{"quantity", q.Value(), "", func(f *source.TradeInput) { f.Quantity = q }},
		{"price", p.Amount(), "USD", func(f *source.TradeInput) { f.Price = p }},
		{"side", "sell", "", func(f *source.TradeInput) { f.Side = source.Sell }},
		{"trade_date", next.String(), "", func(f *source.TradeInput) { f.TradeDate = next }},
		{"executed_at", at.String(), "", func(f *source.TradeInput) { f.ExecutedAt = at }},
		{"settlement_date", next.String(), "", func(f *source.TradeInput) { f.SettlementDate = next }},
		{"gross", money.Amount(), "USD", func(f *source.TradeInput) { f.Gross = money }},
		{"proceeds", money.Amount(), "USD", func(f *source.TradeInput) { f.Proceeds = money }},
		{"net", money.Amount(), "USD", func(f *source.TradeInput) { f.Net = money }},
		{"net_billable", money.Amount(), "USD", func(f *source.TradeInput) { f.NetWithBillable = money }},
		{"accrued_interest", money.Amount(), "USD", func(f *source.TradeInput) { f.AccruedInterest = money }},
		{"position_effect", "closing", "", func(f *source.TradeInput) { f.PositionEffect = source.Closing }},
	}
	original := base.Fields()
	for _, tc := range cases {
		t.Run(tc.scope, func(t *testing.T) {
			got, e := ApplyTradeField(base, tc.scope, tc.text, tc.currency)
			want := base.Fields()
			tc.change(&want)

			if e != nil || !reflect.DeepEqual(got.Fields(), want) || !reflect.DeepEqual(base.Fields(), original) {
				t.Fatal("correction changed another fact or lost exact text", tc.scope, e)
			}
		})
	}

	for _, tc := range []struct{ scope, text, currency string }{{"quantity", "0", ""}, {"price", "1", "invalid"}, {"trade_date", "not-a-date", ""}, {"executed_at", "yesterday", ""}, {"net", "1", "invalid"}, {"side", "unknown", ""}, {"unknown", "x", ""}} {
		if _, e := ApplyTradeField(base, tc.scope, tc.text, tc.currency); e == nil {
			t.Fatal("invalid correction accepted", tc.scope)
		}
	}

	for _, scope := range []string{"executed_at", "settlement_date", "net"} {
		got, e := ApplyTradeField(base, scope, "", "")
		if e != nil || !reflect.DeepEqual(got.Fields(), original) {
			t.Fatal("explicit optional clearing changed unrelated facts", scope, e)
		}
	}
}

func TestOptionDebtAndFXCorrectionRoles(t *testing.T) {
	usd, _ := financial.ParseCurrency("USD")
	gbp, _ := financial.ParseCurrency("GBP")
	account, _ := source.NewReference("synthetic", "account", "", "one")
	ref, _ := source.NewReference("synthetic", "instrument", "", "one")
	date, _ := financial.ParseDate("2026-10-20")
	q, _ := financial.NewQuantity("2")
	terms, _ := source.NewOptionTerms(source.OptionInput{})
	option, _ := source.NewInstrument(
		source.InstrumentInput{Reference: ref, Kind: source.Option, Option: terms},
	)

	lifecycle, err := source.NewLifecycle(
		source.LifecycleInput{
			Location:   "synthetic",
			Account:    account,
			Instrument: option,
			Kind:       source.Exercise,
			Quantity:   q,
			Date:       date,
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		scope, text, currency string
		check                 func(source.LifecycleInput) bool
	}{
		{"quantity", "3.0001", "", func(f source.LifecycleInput) bool { return f.Quantity.Value() == "3.0001" }},
		{"kind", "expiration", "", func(f source.LifecycleInput) bool { return f.Kind == source.Expiration }},
		{"trade_date", "2026-10-21", "", func(f source.LifecycleInput) bool { return f.Date.String() == "2026-10-21" }},
		{"executed_at", "2026-10-20T09:30:00.120Z", "", func(f source.LifecycleInput) bool { return f.At.String() == "2026-10-20T09:30:00.120Z" }},
		{"option.strike", "100.00001", "USD", func(f source.LifecycleInput) bool {
			return f.Instrument.Option().Fields().Strike.Amount() == "100.00001"
		}},
		{"option.multiplier", "100", "", func(f source.LifecycleInput) bool { return f.Instrument.Option().Fields().Multiplier.Value() == "100" }},
		{"option.right", "put", "", func(f source.LifecycleInput) bool { return f.Instrument.Option().Fields().Right == source.Put }},
		{"option.expiry", "2026-11-20", "", func(f source.LifecycleInput) bool {
			return f.Instrument.Option().Fields().Expiry.String() == "2026-11-20"
		}},
	} {
		got, e := ApplyLifecycleField(lifecycle, tc.scope, tc.text, tc.currency)
		if e != nil || !tc.check(got.Fields()) || got.Fields().Account != account ||
			got.Fields().Location != "synthetic" {
			t.Fatal("invalid lifecycle correction", tc.scope, e)
		}
	}

	for _, tc := range []struct{ scope, text, currency string }{{"quantity", "invalid", ""}, {"kind", "invalid", ""}, {"option.right", "invalid", ""}, {"option.strike", "1", "invalid"}, {"option.multiplier", "-1", ""}, {"option.unknown", "1", ""}} {
		if _, e := ApplyLifecycleField(lifecycle, tc.scope, tc.text, tc.currency); e == nil {
			t.Fatal("invalid lifecycle field accepted", tc.scope)
		}
	}

	debt, _ := source.NewDebtTerms(source.DebtInput{Type: source.TreasuryNote, Currency: usd})

	instrument, _ := source.NewInstrument(source.InstrumentInput{Reference: ref, Kind: source.Debt, Debt: debt})
	for _, tc := range []struct {
		scope, text string
		check       func(source.DebtInput) bool
	}{
		{"debt.coupon", "4.125000001", func(f source.DebtInput) bool { return f.Coupon.Value() == "4.125000001" }},
		{"debt.maturity", "2030-10-20", func(f source.DebtInput) bool { return f.Maturity.String() == "2030-10-20" }},
		{"debt.payments", "2", func(f source.DebtInput) bool { return f.PaymentsPerYear == 2 }},
	} {
		got, e := applyInstrumentTerm(instrument, tc.scope, tc.text, "")
		if e != nil || !tc.check(got.Debt().Fields()) || got.Reference() != ref {
			t.Fatal("invalid debt correction", tc.scope, e)
		}
	}

	sent, _ := source.NewCurrencyAmount(usd, "1")
	received, _ := source.NewCurrencyAmount(gbp, "")
	rate, _ := financial.NewPrice("0.75", gbp)

	fx, err := source.NewConversion(
		source.ConversionInput{
			Location: "synthetic",
			Account:  account,
			Date:     date,
			Sent:     sent,
			Received: received,
			RateBase: usd,
			Rate:     rate,
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		scope, text, currency string
		check                 func(source.ConversionInput) bool
	}{
		{"sent_amount", "2.0001", "", func(f source.ConversionInput) bool { return f.Sent.Amount() == "2.0001" && f.Sent.Currency() == usd }},
		{"received_amount", "1.5001", "", func(f source.ConversionInput) bool {
			return f.Received.Amount() == "1.5001" && f.Received.Currency() == gbp
		}},
		{"rate", "0.750001", "", func(f source.ConversionInput) bool {
			return f.Rate.Amount() == "0.750001" && f.Rate.Currency() == gbp && f.RateBase == usd
		}},
		{"trade_date", "2026-10-21", "", func(f source.ConversionInput) bool { return f.Date.String() == "2026-10-21" }},
		{"executed_at", "2026-10-20T09:30:00Z", "", func(f source.ConversionInput) bool { return f.At.String() == "2026-10-20T09:30:00Z" }},
		{"gross", "-2.0001", "USD", func(f source.ConversionInput) bool { return f.Gross.Amount() == "-2.0001" }},
		{"proceeds", "-2.0001", "USD", func(f source.ConversionInput) bool { return f.Proceeds.Amount() == "-2.0001" }},
		{"net", "-2.0001", "USD", func(f source.ConversionInput) bool { return f.Net.Amount() == "-2.0001" }},
		{"net_billable", "-2.0001", "USD", func(f source.ConversionInput) bool { return f.NetWithBillable.Amount() == "-2.0001" }},
	} {
		got, e := ApplyConversionField(fx, tc.scope, tc.text, tc.currency)
		if e != nil || !tc.check(got.Fields()) || got.Fields().Account != account {
			t.Fatal("invalid conversion correction", tc.scope, e)
		}
	}

	if _, e := ApplyConversionField(fx, "sent_amount", "-1", ""); e == nil {
		t.Fatal("negative FX magnitude accepted")
	}

	if _, e := ApplyConversionField(fx, "unknown", "1", ""); e == nil {
		t.Fatal("unknown FX field accepted")
	}
}
