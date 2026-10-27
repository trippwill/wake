// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0
package source_test

import (
	"testing"

	"go.trippwill.dev/wake/financial"
	"go.trippwill.dev/wake/source"
)

func TestDebtIssueInvariants(t *testing.T) {
	coupon, _ := financial.NewQuantity("1.25")
	if _, err := source.NewDebtTerms(source.DebtInput{Type: source.TreasuryBill, Coupon: coupon}); err == nil {
		t.Fatal("coupon-bearing bill")
	}

	gbp, _ := financial.ParseCurrency("GBP")
	for _, kind := range []source.DebtType{source.TreasuryNote, source.TreasuryBond, source.ConventionalGilt} {
		c := gbp
		if kind != source.ConventionalGilt {
			c, _ = financial.ParseCurrency("USD")
		}

		d, err := source.NewDebtTerms(
			source.DebtInput{Type: kind, Currency: c, Coupon: coupon, PaymentsPerYear: 2},
		)
		if err != nil || d.Fields().Coupon.Value() != "1.25" {
			t.Fatal(kind, err)
		}
	}

	if _, err := source.NewDebtTerms(source.DebtInput{Type: source.TreasuryNote, Currency: gbp}); err == nil {
		t.Fatal("wrong Treasury currency")
	}
}
