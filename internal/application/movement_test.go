// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package application

import (
	"errors"
	"testing"

	"go.trippwill.dev/wake/financial"
	"go.trippwill.dev/wake/source"
)

func TestMovementCorrectionsPreserveExactTermsAndRelease(t *testing.T) {
	trade := selectionTrade(t, source.Equity, nil, source.DebtTerms{}).Fields()

	transfer, err := source.NewSecurityTransfer(source.SecurityTransferInput{
		Location: "synthetic", Account: trade.Account, Instrument: trade.Instrument,
		Direction: source.TransferIn, Quantity: trade.Quantity, Date: trade.TradeDate,
		TransferPrice: trade.Price,
	})
	if err != nil {
		t.Fatal(err)
	}

	usd, _ := financial.ParseCurrency("USD")
	amount, _ := financial.NewMoney("-1.2300", usd)

	cash, err := source.NewCashActivity(source.CashActivityInput{
		Location: "synthetic", Account: trade.Account, Category: source.CashFee,
		Amount: amount, Date: trade.TradeDate, Description: "Original fee",
	})
	if err != nil {
		t.Fatal(err)
	}

	transferTerms, _ := SecurityTransferTerms(transfer)
	cashTerms, _ := CashTerms(cash)
	for _, tc := range []struct {
		name string
		base ActivityTerms
		edit FieldSelection
		want string
		read func(ActivityTerms) string
	}{
		{"transfer quantity", transferTerms, FieldSelection{Scope: "quantity", Text: "12.00000000000001"}, "12.00000000000001", func(v ActivityTerms) string { return v.SecurityTransfer().Fields().Quantity.Value() }},
		{"transfer valuation", transferTerms, FieldSelection{Scope: "transfer_price", Text: "100.000000000001", Currency: "USD"}, "100.000000000001", func(v ActivityTerms) string { return v.SecurityTransfer().Fields().TransferPrice.Amount() }},
		{"unknown transfer valuation", transferTerms, FieldSelection{Scope: "transfer_price"}, "", func(v ActivityTerms) string { return v.SecurityTransfer().Fields().TransferPrice.Amount() }},
		{"transfer cost", transferTerms, FieldSelection{Scope: "transfer_cost", Text: "-0.00100", Currency: "USD"}, "-0.00100", func(v ActivityTerms) string { return v.SecurityTransfer().Fields().ReportedCost.Amount() }},
		{"transfer date", transferTerms, FieldSelection{Scope: "trade_date", Text: "2026-11-01"}, "2026-11-01", func(v ActivityTerms) string { return v.SecurityTransfer().Fields().Date.String() }},
		{"reported time", transferTerms, FieldSelection{Scope: "executed_at", Text: "2026-10-20T13:30:00.120000000Z"}, "2026-10-20T13:30:00.120000000Z", func(v ActivityTerms) string { return v.SecurityTransfer().Fields().At.String() }},
		{"cash refund", cashTerms, FieldSelection{Scope: "cash_amount", Text: "0.000000000000001", Currency: "EUR"}, "0.000000000000001 EUR", func(v ActivityTerms) string {
			f := v.Cash().Fields()
			return f.Amount.Amount() + " " + f.Amount.Currency().String()
		}},
		{"cash zero", cashTerms, FieldSelection{Scope: "cash_amount", Text: "0.0000", Currency: "USD"}, "0.0000 USD", func(v ActivityTerms) string {
			f := v.Cash().Fields()
			return f.Amount.Amount() + " " + f.Amount.Currency().String()
		}},
		{"cash description", cashTerms, FieldSelection{Scope: "description", Text: "Corrected fee"}, "Corrected fee", func(v ActivityTerms) string { return v.Cash().Fields().Description }},
		{"unknown description", cashTerms, FieldSelection{Scope: "description"}, "", func(v ActivityTerms) string { return v.Cash().Fields().Description }},
		{"cash availability", cashTerms, FieldSelection{Scope: "available_for_trading_date", Text: "2026-11-02"}, "2026-11-02", func(v ActivityTerms) string { return v.Cash().Fields().AvailableForTradingDate.String() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := ActivityCalculationInput{
				Rule:     RetainedClaimsRule,
				Baseline: tc.base,
				Claims: []ActivityClaim{
					{Key: "retained", Value: tc.base.Observation()},
				},
				Decisions: []FieldSelection{tc.edit},
			}

			got, e := CalculateActivity(in)
			if e != nil {
				t.Fatal(e)
			}

			if tc.read(got.Terms) != tc.want || got.State.Authority() != OperatorSelection {
				t.Fatal("correction lost exact terms or authority", got)
			}

			in.Decisions = nil

			released, e := CalculateActivity(in)
			if e != nil || tc.read(released.Terms) != tc.read(tc.base) ||
				released.State.Authority() != AutomaticSelection {
				t.Fatal("release lost retained terms", released, e)
			}
		})
	}

	for _, edit := range []FieldSelection{
		{Scope: "quantity", Text: "0"},
		{Scope: "quantity", Text: "invalid"},
		{Scope: "trade_date", Text: "2026-02-30"},
		{Scope: "executed_at", Text: "invalid"},
		{Scope: "transfer_price", Text: "1", Currency: "invalid"},
	} {
		_, e := CalculateActivity(ActivityCalculationInput{
			Rule:     RetainedClaimsRule,
			Baseline: transferTerms,
			Claims: []ActivityClaim{
				{Key: "retained", Value: transfer.Observation()},
			},
			Decisions: []FieldSelection{edit},
		})
		if !errors.Is(e, ErrInvalid) {
			t.Errorf("invalid correction %v: %v", edit, e)
		}
	}
}
