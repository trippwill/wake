// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0
package application

import (
	"cmp"
	"slices"

	"go.trippwill.dev/wake/financial"
	"go.trippwill.dev/wake/source"
)

// Select keeps the durable baseline when compatible enrichments disagree. Neither
// observation arrival order nor missing data establishes a source revision.
func selectObservations[T any](
	base T,
	observations []T,
	compatible func(T, T) bool,
	enrich func(T, T) (T, error),
) (T, bool, error) {
	candidates := []T{base}
	conflict := false
	for _, o := range observations {
		if !compatible(base, o) {
			conflict = true
			continue
		}

		candidates = append(candidates, o)
	}

	for i, a := range candidates {
		for _, b := range candidates[i+1:] {
			if !compatible(a, b) {
				return base, true, nil
			}
		}
	}

	selected := base

	var err error
	for _, o := range candidates[1:] {
		selected, err = enrich(selected, o)
		if err != nil {
			return base, false, err
		}
	}

	return selected, conflict, nil
}

func SelectTrades(base source.Trade, observations []source.Trade) (source.Trade, bool, error) {
	return selectObservations(base, observations, Compatible, enrichTrade)
}

func SelectLifecycles(
	base source.Lifecycle,
	observations []source.Lifecycle,
) (source.Lifecycle, bool, error) {
	return selectObservations(base, observations, CompatibleLifecycle, enrichLifecycle)
}

func SelectConversions(
	base source.Conversion,
	observations []source.Conversion,
) (source.Conversion, bool, error) {
	return selectObservations(base, observations, CompatibleConversion, enrichConversion)
}

func EnrichInstrument(a, b source.Instrument) (source.Instrument, error) {
	f, g := a.Fields(), b.Fields()
	if f.Kind == source.UnknownInstrument && g.Kind != source.UnknownInstrument {
		f.Kind = g.Kind
	}

	if f.Name == "" {
		f.Name = g.Name
	}

	if f.Ticker == "" {
		f.Ticker = g.Ticker
	}

	if f.Venue == "" {
		f.Venue = g.Venue
	}

	if f.Reference.IsZero() {
		f.Reference = g.Reference
	}

	for _, id := range g.Identifiers {
		if !slices.Contains(f.Identifiers, id) {
			f.Identifiers = append(f.Identifiers, id)
		}
	}

	slices.SortFunc(f.Identifiers, func(a, b source.Identifier) int {
		if n := cmp.Compare(a.Kind(), b.Kind()); n != 0 {
			return n
		}

		if n := cmp.Compare(a.Value(), b.Value()); n != 0 {
			return n
		}

		return cmp.Compare(a.Field(), b.Field())
	})

	if f.Kind == source.Option {
		x, y := f.Option.Fields(), g.Option.Fields()
		if x.Right == "" {
			x.Right = y.Right
		}

		if x.Expiry.IsZero() {
			x.Expiry = y.Expiry
		}

		if x.Strike.Amount() == "" {
			x.Strike = y.Strike
		}

		if x.Multiplier.Value() == "" {
			x.Multiplier = y.Multiplier
		}

		if x.EffectiveFrom.IsZero() {
			x.EffectiveFrom = y.EffectiveFrom
		}

		if len(x.Deliverables) == 0 {
			x.Deliverables = y.Deliverables
		}

		if x.Underlier.Kind() == "" {
			x.Underlier = y.Underlier
		} else if y.Underlier.Kind() != "" {
			u, err := EnrichInstrument(x.Underlier, y.Underlier)
			if err != nil {
				return source.Instrument{}, err
			}

			x.Underlier = u
		}

		var err error

		f.Option, err = source.NewOptionTerms(x)
		if err != nil {
			return source.Instrument{}, err
		}
	}

	if f.Kind == source.Debt {
		x, y := f.Debt.Fields(), g.Debt.Fields()
		if x.Type == "" {
			x.Type = y.Type
		}

		if x.Currency == (financial.Currency{}) {
			x.Currency = y.Currency
		}

		if x.Coupon.Value() == "" {
			x.Coupon = y.Coupon
		}

		if x.Maturity.IsZero() {
			x.Maturity = y.Maturity
		}

		if x.IssueDate.IsZero() {
			x.IssueDate = y.IssueDate
		}

		if x.DatedDate.IsZero() {
			x.DatedDate = y.DatedDate
		}

		if x.PaymentsPerYear == 0 {
			x.PaymentsPerYear = y.PaymentsPerYear
		}

		var err error

		f.Debt, err = source.NewDebtTerms(x)
		if err != nil {
			return source.Instrument{}, err
		}
	}

	return source.NewInstrument(f)
}

func enrichTrade(a, b source.Trade) (source.Trade, error) {
	f, g := a.Fields(), b.Fields()
	if f.Gross.Amount() == "" {
		f.Gross = g.Gross
	}

	if f.Proceeds.Amount() == "" {
		f.Proceeds = g.Proceeds
	}

	if f.Net.Amount() == "" {
		f.Net = g.Net
	}

	if f.AccruedInterest.Amount() == "" {
		f.AccruedInterest = g.AccruedInterest
	}

	if f.NetWithBillable.Amount() == "" {
		f.NetWithBillable = g.NetWithBillable
	}

	if f.QuotationBasis == "" {
		f.QuotationBasis = g.QuotationBasis
	}

	if f.PositionEffect == "" {
		f.PositionEffect = g.PositionEffect
	}

	if len(f.Charges) == 0 {
		f.Charges = g.Charges
	}

	if f.SettlementDate.IsZero() {
		f.SettlementDate = g.SettlementDate
	}

	if f.ExecutedAt.IsZero() || (!g.ExecutedAt.IsZero() && f.ExecutedAt.Contains(g.ExecutedAt)) {
		f.ExecutedAt = g.ExecutedAt
	}

	if f.Order.IsZero() {
		f.Order = g.Order
	}

	var err error

	f.Instrument, err = EnrichInstrument(f.Instrument, g.Instrument)
	if err != nil {
		return source.Trade{}, err
	}

	return source.NewTrade(f)
}

func enrichLifecycle(a, b source.Lifecycle) (source.Lifecycle, error) {
	f, g := a.Fields(), b.Fields()
	if f.At.IsZero() || (!g.At.IsZero() && f.At.Contains(g.At)) {
		f.At = g.At
	}

	if f.Order.IsZero() {
		f.Order = g.Order
	}

	var err error

	f.Instrument, err = EnrichInstrument(f.Instrument, g.Instrument)
	if err != nil {
		return source.Lifecycle{}, err
	}

	return source.NewLifecycle(f)
}

func enrichConversion(a, b source.Conversion) (source.Conversion, error) {
	f, g := a.Fields(), b.Fields()
	if f.Sent.Amount() == "" {
		f.Sent = g.Sent
	}

	if f.Received.Amount() == "" {
		f.Received = g.Received
	}

	if f.Rate.Amount() == "" {
		f.Rate = g.Rate
		f.RateBase = g.RateBase
	}

	if f.At.IsZero() || (!g.At.IsZero() && f.At.Contains(g.At)) {
		f.At = g.At
	}

	if f.Order.IsZero() {
		f.Order = g.Order
	}

	if f.Gross.Amount() == "" {
		f.Gross = g.Gross
	}

	if f.Proceeds.Amount() == "" {
		f.Proceeds = g.Proceeds
	}

	if f.Net.Amount() == "" {
		f.Net = g.Net
	}

	if f.NetWithBillable.Amount() == "" {
		f.NetWithBillable = g.NetWithBillable
	}

	if len(f.Charges) == 0 {
		f.Charges = g.Charges
	}

	return source.NewConversion(f)
}
