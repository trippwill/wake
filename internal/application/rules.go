// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package application

import (
	"maps"
	"slices"

	"go.trippwill.dev/wake/financial"
	"go.trippwill.dev/wake/source"
)

const (
	AuthorityRule  = "account-authority/1"
	RepeatRule     = "qualified-activity-observations/2"
	InstrumentRule = "qualified-instrument-reference/1"
)

// Compatible leaves absent optional facts unknown. It never reconciles a changed
// monetary term, native identity, civil date or incompatible time bucket.
func Compatible(a, b source.Trade) bool {
	x, y := a.Fields(), b.Fields()
	if (x.QuotationBasis != "" && y.QuotationBasis != "" && x.QuotationBasis != y.QuotationBasis) ||
		x.Kind != y.Kind ||
		x.Account != y.Account ||
		x.Side != y.Side ||
		x.Quantity != y.Quantity ||
		x.Price != y.Price ||
		x.TradeDate != y.TradeDate ||
		x.Instrument.Kind() != y.Instrument.Kind() ||
		!CompatibleInstrument(x.Instrument, y.Instrument) {
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

	if !x.Order.IsZero() && !y.Order.IsZero() && x.Order != y.Order {
		return false
	}

	if x.PositionEffect != "" && y.PositionEffect != "" && x.PositionEffect != y.PositionEffect {
		return false
	}

	for _, pair := range [][2]financial.Money{{x.Gross, y.Gross}, {x.Net, y.Net}, {x.Proceeds, y.Proceeds}, {x.AccruedInterest, y.AccruedInterest}, {x.NetWithBillable, y.NetWithBillable}} {
		if pair[0].Amount() != "" && pair[1].Amount() != "" && pair[0] != pair[1] {
			return false
		}
	}

	return compatibleCharges(x.Charges, y.Charges)
}

func CompatibleOption(a, b source.OptionTerms) bool {
	x, y := a.Fields(), b.Fields()
	if !x.EffectiveFrom.IsZero() && !y.EffectiveFrom.IsZero() && x.EffectiveFrom != y.EffectiveFrom {
		return false
	}

	if x.Right != "" && y.Right != "" && x.Right != y.Right {
		return false
	}

	if !x.Expiry.IsZero() && !y.Expiry.IsZero() && x.Expiry != y.Expiry {
		return false
	}

	if x.Strike.Amount() != "" && y.Strike.Amount() != "" && x.Strike != y.Strike {
		return false
	}

	if x.Multiplier.Value() != "" && y.Multiplier.Value() != "" && x.Multiplier != y.Multiplier {
		return false
	}

	if !x.Underlier.Reference().IsZero() && !y.Underlier.Reference().IsZero() &&
		x.Underlier.Reference() != y.Underlier.Reference() {
		return false
	}

	if x.Underlier.Kind() != "" && y.Underlier.Kind() != "" && !CompatibleInstrument(x.Underlier, y.Underlier) {
		return false
	}

	return len(x.Deliverables) == 0 || len(y.Deliverables) == 0 || slices.Equal(x.Deliverables, y.Deliverables)
}

func CompatibleLifecycle(a, b source.Lifecycle) bool {
	x, y := a.Fields(), b.Fields()

	return x.Account == y.Account && x.Kind == y.Kind && x.Quantity == y.Quantity && x.Date == y.Date &&
		CompatibleInstrument(
			x.Instrument,
			y.Instrument,
		) && (x.Order.IsZero() || y.Order.IsZero() || x.Order == y.Order) &&
		(x.At.IsZero() || y.At.IsZero() || x.At.Contains(y.At) || y.At.Contains(x.At))
}

func CompatibleDebt(a, b source.DebtTerms) bool {
	x, y := a.Fields(), b.Fields()

	compatible := (x.Type == "" || y.Type == "" || x.Type == y.Type) &&
		(x.Currency == (financial.Currency{}) || y.Currency == (financial.Currency{}) || x.Currency == y.Currency) &&
		(x.Coupon.Value() == "" || y.Coupon.Value() == "" || x.Coupon == y.Coupon) &&
		(x.Maturity.IsZero() || y.Maturity.IsZero() || x.Maturity == y.Maturity) &&
		(x.IssueDate.IsZero() || y.IssueDate.IsZero() || x.IssueDate == y.IssueDate) &&
		(x.DatedDate.IsZero() || y.DatedDate.IsZero() || x.DatedDate == y.DatedDate) &&
		(x.PaymentsPerYear == 0 || y.PaymentsPerYear == 0 || x.PaymentsPerYear == y.PaymentsPerYear)
	if !compatible {
		return false
	}

	_, err := enrichDebt(a, b)

	return err == nil
}

func CompatibleConversion(a, b source.Conversion) bool {
	x, y := a.Fields(), b.Fields()
	if (!x.Order.IsZero() && !y.Order.IsZero() && x.Order != y.Order) || x.Account != y.Account ||
		x.Date != y.Date ||
		x.Sent.Currency() != y.Sent.Currency() ||
		x.Received.Currency() != y.Received.Currency() {
		return false
	}

	if x.Sent.Amount() != "" && y.Sent.Amount() != "" && x.Sent.Amount() != y.Sent.Amount() {
		return false
	}

	if x.Received.Amount() != "" && y.Received.Amount() != "" && x.Received.Amount() != y.Received.Amount() {
		return false
	}

	if x.Rate.Amount() != "" && y.Rate.Amount() != "" && (x.Rate != y.Rate || x.RateBase != y.RateBase) {
		return false
	}

	if !x.At.IsZero() && !y.At.IsZero() && !x.At.Contains(y.At) && !y.At.Contains(x.At) {
		return false
	}

	for _, p := range [][2]financial.Money{{x.Gross, y.Gross}, {x.Proceeds, y.Proceeds}, {x.Net, y.Net}, {x.NetWithBillable, y.NetWithBillable}} {
		if p[0].Amount() != "" && p[1].Amount() != "" && p[0] != p[1] {
			return false
		}
	}

	return compatibleCharges(x.Charges, y.Charges)
}

func CompatibleInstrument(a, b source.Instrument) bool {
	if !CompatibleInstrumentKind(a.Kind(), b.Kind()) ||
		(!a.Reference().IsZero() && !b.Reference().IsZero() && a.Reference() != b.Reference()) {
		return false
	}

	ids := func(i source.Instrument) map[source.IdentifierKind]map[string]bool {
		out := map[source.IdentifierKind]map[string]bool{}
		for _, id := range i.Identifiers() {
			if out[id.Kind()] == nil {
				out[id.Kind()] = map[string]bool{}
			}

			out[id.Kind()][id.Value()] = true
		}

		return out
	}

	x, y := ids(a), ids(b)
	for kind, values := range x {
		if other, reported := y[kind]; reported && !maps.Equal(values, other) {
			return false
		}
	}

	return CompatibleOption(a.Option(), b.Option()) && CompatibleDebt(a.Debt(), b.Debt())
}

func CompatibleInstrumentKind(a, b source.InstrumentKind) bool {
	if a == b {
		return true
	}

	return (a == source.UnknownInstrument && (b == source.Equity || b == source.Fund)) ||
		(b == source.UnknownInstrument && (a == source.Equity || a == source.Fund))
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
