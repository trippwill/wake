// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0
package source

import "go.trippwill.dev/wake/financial"

type DebtType string

const (
	TreasuryBill     DebtType = "treasury-bill"
	TreasuryNote     DebtType = "treasury-note"
	TreasuryBond     DebtType = "treasury-bond"
	ConventionalGilt DebtType = "conventional-gilt"
)

type (
	DebtInput struct {
		Type                           DebtType
		Currency                       financial.Currency
		Maturity, IssueDate, DatedDate financial.Date
		Coupon                         financial.Quantity
		PaymentsPerYear                int
	}
	DebtTerms struct{ input *DebtInput }
)

func NewDebtTerms(f DebtInput) (DebtTerms, error) {
	switch f.Type {
	case "", TreasuryBill, TreasuryNote, TreasuryBond, ConventionalGilt:
	default:
		return DebtTerms{}, ErrInvalidInstrument
	}

	if f.Coupon.Value() != "" && f.Coupon.Value()[0] == '-' || f.PaymentsPerYear < 0 ||
		f.PaymentsPerYear > 366 {
		return DebtTerms{}, ErrInvalidInstrument
	}

	if f.Type == TreasuryBill && (f.Coupon.Positive() || f.PaymentsPerYear != 0) {
		return DebtTerms{}, ErrInvalidInstrument
	}

	if f.Currency != (financial.Currency{}) {
		if f.Type == ConventionalGilt && f.Currency.String() != "GBP" {
			return DebtTerms{}, ErrInvalidInstrument
		}

		if f.Type != "" && f.Type != ConventionalGilt && f.Currency.String() != "USD" {
			return DebtTerms{}, ErrInvalidInstrument
		}
	}

	for _, d := range []financial.Date{f.IssueDate, f.DatedDate} {
		if !d.IsZero() && !f.Maturity.IsZero() && d.String() >= f.Maturity.String() {
			return DebtTerms{}, ErrInvalidInstrument
		}
	}

	return DebtTerms{&f}, nil
}
func (d DebtTerms) IsZero() bool { return d.input == nil }
func (d DebtTerms) Fields() DebtInput {
	if d.input == nil {
		return DebtInput{}
	}

	return *d.input
}
