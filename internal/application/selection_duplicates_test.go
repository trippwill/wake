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

// Keep the original all-pairs selector as an independent behavior oracle.
func referenceSelection[T any](
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
	for _, o := range candidates[1:] {
		var err error

		selected, err = enrich(selected, o)
		if err != nil {
			return base, true, nil
		}
	}

	return selected, conflict, nil
}

func checkRepeatedSelection[T any](
	t *testing.T,
	base T,
	variants []T,
	compatible func(T, T) bool,
	enrich func(T, T) (T, error),
	key func(T) any,
) {
	t.Helper()

	for _, a := range variants {
		for _, b := range variants {
			for _, c := range variants {
				observations := []T{a, a, b, c, a, c, b}

				want, wantConflict, wantErr := referenceSelection(base, observations, compatible, enrich)
				got, conflict, err := selectObservations(base, observations, compatible, enrich, key)
				if !reflect.DeepEqual(got, want) || conflict != wantConflict || err != wantErr {
					t.Fatal("selection changed", got, want, conflict, wantConflict, err, wantErr)
				}
			}
		}
	}
}

func TestDuplicateComparisonPreservesSelection(t *testing.T) {
	usd, _ := financial.ParseCurrency("USD")
	base := selectionTrade(t, source.Option, nil, source.DebtTerms{})
	tradeVariants := []source.Trade{base}
	for index, value := range []string{"1.00", "1.000", "2"} {
		f := base.Fields()
		f.Location = fmt.Sprintf("report/%d", index)
		f.Net, _ = financial.NewMoney(value, usd)
		f.ExecutedAt, _ = financial.ParseTimestamp(
			[]string{"2026-10-20T12:00:00Z", "2026-10-20T12:00:00.000Z", "2026-10-20T12:00:00.001Z"}[index],
		)
		i := f.Instrument.Fields()
		multiplier, _ := financial.NewQuantity(value)
		i.Option, _ = source.NewOptionTerms(source.OptionInput{Multiplier: multiplier})
		f.Instrument, _ = source.NewInstrument(i)

		v, err := source.NewTrade(f)
		if err != nil {
			t.Fatal(err)
		}

		tradeVariants = append(tradeVariants, v)
	}

	checkRepeatedSelection(t, base, tradeVariants, Compatible, enrichTrade, tradeComparisonKey)
	lf := source.LifecycleInput{
		Location:   "base",
		Account:    base.Fields().Account,
		Instrument: base.Fields().Instrument,
		Kind:       source.Exercise,
		Quantity:   base.Fields().Quantity,
		Date:       base.Fields().TradeDate,
	}

	lifecycle, err := source.NewLifecycle(lf)
	if err != nil {
		t.Fatal(err)
	}

	lifecycles := []source.Lifecycle{lifecycle}
	for _, trade := range tradeVariants[1:] {
		f := lf
		f.Instrument = trade.Fields().Instrument
		f.At = trade.Fields().ExecutedAt

		v, e := source.NewLifecycle(f)
		if e != nil {
			t.Fatal(e)
		}

		lifecycles = append(lifecycles, v)
	}

	checkRepeatedSelection(
		t,
		lifecycle,
		lifecycles,
		CompatibleLifecycle,
		enrichLifecycle,
		lifecycleComparisonKey,
	)

	eur, _ := financial.ParseCurrency("EUR")
	sent, _ := source.NewCurrencyAmount(usd, "1")
	received, _ := source.NewCurrencyAmount(eur, "")
	cf := source.ConversionInput{
		Location: "base",
		Account:  base.Fields().Account,
		Date:     base.Fields().TradeDate,
		Sent:     sent,
		Received: received,
	}

	conversion, err := source.NewConversion(cf)
	if err != nil {
		t.Fatal(err)
	}

	conversions := []source.Conversion{conversion}
	for _, trade := range tradeVariants[1:] {
		f := cf
		f.Net = trade.Fields().Net
		f.At = trade.Fields().ExecutedAt

		v, e := source.NewConversion(f)
		if e != nil {
			t.Fatal(e)
		}

		conversions = append(conversions, v)
	}

	checkRepeatedSelection(
		t,
		conversion,
		conversions,
		CompatibleConversion,
		enrichConversion,
		conversionComparisonKey,
	)
}

func TestRepeatedObservationComparisonCount(t *testing.T) {
	base := selectionTrade(t, source.Equity, nil, source.DebtTerms{})
	for _, n := range []int{288, 576, 1152} {
		observations := make([]source.Trade, n)
		for i := range observations {
			f := base.Fields()
			f.Location = fmt.Sprintf("report/%d", i)

			var err error

			observations[i], err = source.NewTrade(f)
			if err != nil {
				t.Fatal(err)
			}
		}

		comparisons := 0
		_, conflict, err := selectObservations(base, observations, func(a, b source.Trade) bool {
			comparisons++
			return Compatible(a, b)
		}, enrichTrade, tradeComparisonKey)
		if err != nil || conflict || comparisons != n {
			t.Fatal("duplicate observations caused pairwise comparisons", n, comparisons, conflict, err)
		}
	}
}

func TestDistinctConflictingObservationsStopBeforeScanningHistory(t *testing.T) {
	base := selectionTrade(t, source.Equity, nil, source.DebtTerms{})
	observations := make([]source.Trade, 1152)
	for i := range observations {
		f := base.Fields()
		f.Net, _ = financial.NewMoney(fmt.Sprint(i+1), f.Price.Currency())
		observations[i], _ = source.NewTrade(f)
	}

	keys := 0
	_, conflict, err := selectObservations(
		base,
		observations,
		Compatible,
		enrichTrade,
		func(v source.Trade) any {
			keys++
			return tradeComparisonKey(v)
		},
	)
	if err != nil || !conflict || keys > 3 {
		t.Fatal("conflicting history was scanned before detecting its first disagreement", keys, conflict, err)
	}
}

func BenchmarkRepeatedObservationSelection(b *testing.B) {
	benchmarkRepeatedObservations(b, SelectTrades)
}

func BenchmarkRepeatedObservationReference(b *testing.B) {
	benchmarkRepeatedObservations(
		b,
		func(base source.Trade, observations []source.Trade) (source.Trade, bool, error) {
			return referenceSelection(base, observations, Compatible, enrichTrade)
		},
	)
}

func benchmarkRepeatedObservations(
	b *testing.B,
	selectTrades func(source.Trade, []source.Trade) (source.Trade, bool, error),
) {
	b.Helper()

	base := selectionTrade(b, source.Equity, nil, source.DebtTerms{})
	for _, n := range []int{288, 576, 1152} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			observations := make([]source.Trade, n)
			for i := range observations {
				f := base.Fields()
				f.Location = fmt.Sprintf("report/%d", i)
				observations[i], _ = source.NewTrade(f)
			}

			b.ReportAllocs()
			b.ResetTimer()

			for b.Loop() {
				if _, conflict, err := selectTrades(base, observations); err != nil || conflict {
					b.Fatal(conflict, err)
				}
			}
		})
	}
}
