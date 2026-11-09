// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package source_test

import (
	"testing"

	"go.trippwill.dev/wake/financial"
	"go.trippwill.dev/wake/source"
)

func TestPositionSetsSealExactReportedValuesAndCompleteness(t *testing.T) {
	account, _ := source.NewReference("broker", "account", "", "one")
	ref, _ := source.NewReference("broker", "security", "", "security")
	instrument, _ := source.NewInstrument(source.InstrumentInput{Reference: ref, Kind: source.Equity})
	quantity, _ := financial.NewQuantity("-0.0000000000000012300")
	currency, _ := financial.ParseCurrency("USD")
	cost, _ := financial.NewPrice("0.0000", currency)

	p, err := source.NewPosition(instrument, quantity, cost)
	if err != nil || p.Unit() != "shares" || p.Quantity().Value() != quantity.Value() ||
		p.UnitCostBasis().Amount() != "0.0000" {
		t.Fatal(p, err)
	}

	date, _ := financial.ParseDate("2026-11-04")
	at, _ := financial.ParseTimestamp("2026-11-04T12:34:56.1200Z")
	rows := []source.Position{p}
	input := source.PositionSetInput{
		Location:  "set",
		Account:   account,
		Date:      date,
		At:        at,
		Complete:  true,
		Positions: rows,
	}

	set, err := source.NewPositionSetObservation(input)
	if err != nil {
		t.Fatal(err)
	}

	rows[0] = source.Position{}

	fields := set.Fields()
	fields.Positions[0] = source.Position{}
	if set.Fields().Positions[0].Quantity().Value() != quantity.Value() ||
		set.Fields().At.String() != at.String() {
		t.Fatal("mutable observation or lost precision")
	}

	for _, change := range []func(*source.PositionSetInput){
		func(f *source.PositionSetInput) { f.Location = "" },
		func(f *source.PositionSetInput) { f.Account = source.Reference{} },
		func(f *source.PositionSetInput) { f.Date = financial.Date{} },
		func(f *source.PositionSetInput) { f.Complete = false; f.Reason = "incomplete" },
		func(f *source.PositionSetInput) { f.Reason = "incomplete" },
	} {
		f := set.Fields()
		change(&f)

		if _, err = source.NewPositionSetObservation(f); err == nil {
			t.Fatal("accepted invalid set", f)
		}
	}

	empty := set.Fields()
	empty.Positions = nil
	if _, err = source.NewPositionSetObservation(empty); err != nil {
		t.Fatal("rejected known empty", err)
	}

	empty.Complete = false

	empty.Reason = "not_reported"
	empty.Date = financial.Date{}
	empty.At = financial.Timestamp{}
	if _, err = source.NewPositionSetObservation(empty); err != nil {
		t.Fatal("rejected unknown coverage", err)
	}

	if _, err = source.NewPosition(source.Instrument{}, quantity, cost); err == nil {
		t.Fatal("accepted missing instrument")
	}
}
