// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0
package application

import (
	"cmp"
	"reflect"
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
	comparisonKey func(T) any,
) (T, bool, error) {
	candidates := []T{base}

	keys := []any{comparisonKey(base)}
	accepted := make([]T, 0, len(observations))
	conflict := false
	for _, o := range observations {
		if !compatible(base, o) {
			conflict = true
			continue
		}

		accepted = append(accepted, o)

		key := comparisonKey(o)
		if slices.ContainsFunc(keys, func(existing any) bool { return reflect.DeepEqual(existing, key) }) {
			continue
		}

		for _, previous := range candidates[1:] {
			if !compatible(previous, o) {
				return base, true, nil
			}
		}

		keys = append(keys, key)
		candidates = append(candidates, o)
	}

	selected := base

	var err error
	// Preserve the original enrichment order, including repeated observations.
	for _, o := range accepted {
		selected, err = enrich(selected, o)
		if err != nil {
			return base, true, nil
		}
	}

	return selected, conflict, nil
}

func SelectTrades(base source.Trade, observations []source.Trade) (source.Trade, bool, error) {
	return selectObservations(base, observations, Compatible, enrichTrade, tradeComparisonKey)
}

func tradeComparisonKey(v source.Trade) any {
	f := v.Fields()
	f.Location = ""

	return f
}

func lifecycleComparisonKey(v source.Lifecycle) any {
	f := v.Fields()
	f.Location = ""

	return f
}

func conversionComparisonKey(v source.Conversion) any {
	f := v.Fields()
	f.Location = ""

	return f
}

func SelectLifecycles(
	base source.Lifecycle,
	observations []source.Lifecycle,
) (source.Lifecycle, bool, error) {
	return selectObservations(base, observations, CompatibleLifecycle, enrichLifecycle, lifecycleComparisonKey)
}

func SelectConversions(
	base source.Conversion,
	observations []source.Conversion,
) (source.Conversion, bool, error) {
	return selectObservations(
		base,
		observations,
		CompatibleConversion,
		enrichConversion,
		conversionComparisonKey,
	)
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
		var err error

		f.Debt, err = enrichDebt(f.Debt, g.Debt)
		if err != nil {
			return source.Instrument{}, err
		}
	}

	return source.NewInstrument(f)
}

func enrichDebt(a, b source.DebtTerms) (source.DebtTerms, error) {
	x, y := a.Fields(), b.Fields()
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

	return source.NewDebtTerms(x)
}

func mergeCharges(a, b []source.Charge) ([]source.Charge, error) {
	type role struct{ field, scope string }

	known := make(map[role]source.Charge, len(a)+len(b))
	for _, charges := range [][]source.Charge{a, b} {
		for _, charge := range charges {
			key := role{charge.Field(), charge.Scope()}
			if previous, ok := known[key]; ok {
				if !equalCharge(previous, charge) {
					return nil, ErrInvalid
				}
			} else {
				known[key] = charge
			}
		}
	}

	if len(known) > 32 {
		return nil, ErrInvalid
	}

	if len(known) == 0 {
		return nil, nil
	}

	out := make([]source.Charge, 0, len(known))
	for _, charge := range known {
		out = append(out, charge)
	}

	slices.SortFunc(out, func(a, b source.Charge) int {
		if n := cmp.Compare(a.Field(), b.Field()); n != 0 {
			return n
		}

		return cmp.Compare(a.Scope(), b.Scope())
	})

	return out, nil
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

	charges, chargeErr := mergeCharges(f.Charges, g.Charges)
	if chargeErr != nil {
		return source.Trade{}, chargeErr
	}

	f.Charges = charges

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

	charges, chargeErr := mergeCharges(f.Charges, g.Charges)
	if chargeErr != nil {
		return source.Conversion{}, chargeErr
	}

	f.Charges = charges

	return source.NewConversion(f)
}
