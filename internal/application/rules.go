// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package application

import (
	"go.trippwill.dev/wake/financial"
	"go.trippwill.dev/wake/source"
)

const (
	AuthorityRule  = "account-authority/1"
	RepeatRule     = "qualified-execution-observations/1"
	InstrumentRule = "qualified-instrument-reference/1"
)

// Compatible leaves absent optional facts unknown. It never reconciles a changed
// monetary term, native identity, civil date or incompatible time bucket.
func Compatible(a, b source.Trade) bool {
	x, y := a.Fields(), b.Fields()
	if x.Account != y.Account || x.Side != y.Side || x.Quantity != y.Quantity || x.Price != y.Price ||
		x.TradeDate != y.TradeDate ||
		x.Instrument.Kind() != y.Instrument.Kind() {
		return false
	}

	if !x.Instrument.Reference().IsZero() && !y.Instrument.Reference().IsZero() &&
		x.Instrument.Reference() != y.Instrument.Reference() {
		return false
	}

	if !x.ExecutedAt.IsZero() && !y.ExecutedAt.IsZero() && !x.ExecutedAt.Contains(y.ExecutedAt) &&
		!y.ExecutedAt.Contains(x.ExecutedAt) {
		return false
	}

	if !x.SettlementDate.IsZero() && !y.SettlementDate.IsZero() && x.SettlementDate != y.SettlementDate {
		return false
	}

	if x.PositionEffect != "" && y.PositionEffect != "" && x.PositionEffect != y.PositionEffect {
		return false
	}

	for _, pair := range [][2]financial.Money{{x.Gross, y.Gross}, {x.Net, y.Net}, {x.Proceeds, y.Proceeds}} {
		if pair[0].Amount() != "" && pair[1].Amount() != "" && pair[0] != pair[1] {
			return false
		}
	}

	return compatibleCharges(x.Charges, y.Charges)
}

func compatibleCharges(a, b []source.Charge) bool {
	for _, left := range a {
		for _, right := range b {
			if left.Field() == right.Field() && left.Scope() == right.Scope() && !equalCharge(left, right) {
				return false
			}
		}
	}

	return true
}

func equalCharge(a, b source.Charge) bool {
	return a.Field() == b.Field() && a.Scope() == b.Scope() && a.Money() == b.Money()
}
