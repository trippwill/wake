// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package application

import (
	"testing"

	"go.trippwill.dev/wake/financial"
	"go.trippwill.dev/wake/source"
)

func positionCandidate(t *testing.T, date, at, quantity, cost, reason string) PositionCandidate {
	t.Helper()

	account, _ := source.NewReference("broker", "account", "", "one")
	ref, _ := source.NewReference("broker", "security", "", "security")
	instrument, _ := source.NewInstrument(source.InstrumentInput{Reference: ref, Kind: source.Equity})
	d, _ := financial.ParseDate(date)

	f := source.PositionSetInput{
		Location: "set",
		Account:  account,
		Date:     d,
		Complete: reason == "",
		Reason:   reason,
	}
	if at != "" {
		f.At, _ = financial.ParseTimestamp(at)
	}

	ids := []string{}
	if quantity != "" && reason == "" {
		q, _ := financial.NewQuantity(quantity)

		c, _ := financial.ParseCurrency("USD")
		m := financial.Money{}
		if cost != "" {
			m, _ = financial.NewMoney(cost, c)
		}

		p, err := source.NewPosition(instrument, q, m)
		if err != nil {
			t.Fatal(err)
		}

		f.Positions = []source.Position{p}

		ids = append(ids, "instrument")
	}

	o, err := source.NewPositionSetObservation(f)
	if err != nil {
		t.Fatal(err)
	}

	return PositionCandidate{o, ids}
}

func TestPositionSelectionUsesCompleteEffectiveSetsAndExplicitRevisions(t *testing.T) {
	old := positionCandidate(t, "2026-11-03", "", "10.00", "1.2300", "")
	next := positionCandidate(t, "2026-11-04", "", "-5.0000", "0.0000", "")
	tests := []struct {
		name             string
		rows             []PositionCandidate
		reason, quantity string
	}{
		{"newer replaces", []PositionCandidate{next, old}, "", "-5.0000"},
		{"empty replaces", []PositionCandidate{old, positionCandidate(t, "2026-11-04", "", "", "", "")}, "", ""},
		{
			"missing withholds",
			[]PositionCandidate{old, positionCandidate(t, "2026-11-04", "", "", "", "not_reported")},
			"not_reported",
			"",
		},
		{
			"invalid withholds",
			[]PositionCandidate{next, positionCandidate(t, "2026-11-05", "", "", "", "incomplete")},
			"incomplete",
			"",
		},
		{
			"conflict",
			[]PositionCandidate{old, positionCandidate(t, "2026-11-03", "", "11", "1.2300", "")},
			"conflict",
			"",
		},
		{
			"same value scale",
			[]PositionCandidate{old, positionCandidate(t, "2026-11-03", "", "10", "1.23", "")},
			"",
			"10.00",
		},
		{
			"optional cost enriches",
			[]PositionCandidate{positionCandidate(t, "2026-11-03", "", "10", "", ""), old},
			"",
			"10",
		},
		{
			"later time",
			[]PositionCandidate{
				positionCandidate(t, "2026-11-03", "2026-11-03T12:00:00.1200Z", "11", "", ""),
				positionCandidate(t, "2026-11-03", "2026-11-03T13:00:00.000Z", "12", "", ""),
			},
			"",
			"12",
		},
		{
			"date and instant incomparable",
			[]PositionCandidate{old, positionCandidate(t, "2026-11-03", "2026-11-03T13:00:00Z", "10", "", "")},
			"conflict",
			"",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := SelectPositions(tc.rows)
			if got.Reason != tc.reason {
				t.Fatal(got)
			}

			if tc.reason == "" && tc.quantity != "" && got.Source.Positions[0].Quantity().Value() != tc.quantity {
				t.Fatal(got)
			}
		})
	}

	first, _ := source.NewReference("broker", "snapshot", "", "first")
	second, _ := source.NewReference("broker", "snapshot", "", "second")
	a := old.Observation.Fields()
	a.Reference = first
	old.Observation, _ = source.NewPositionSetObservation(a)
	b := positionCandidate(t, "2026-11-03", "", "11", "", "")
	f := b.Observation.Fields()
	f.Reference = second
	f.Supersedes = []source.Reference{first}

	b.Observation, _ = source.NewPositionSetObservation(f)
	if got := SelectPositions(
		[]PositionCandidate{b, old},
	); got.Reason != "" ||
		got.Source.Positions[0].Quantity().Value() != "11" {
		t.Fatal(got)
	}
}

func TestPositionTimeSelectionCannotHideDateOnlyEvidenceByInputOrder(t *testing.T) {
	dated := positionCandidate(t, "2026-11-03", "", "10", "", "")
	early := positionCandidate(t, "2026-11-03", "2026-11-03T12:00:00Z", "10", "", "")
	late := positionCandidate(t, "2026-11-03", "2026-11-03T13:00:00Z", "10", "", "")
	for _, rows := range [][]PositionCandidate{{dated, early, late}, {early, dated, late}, {early, late, dated}, {late, dated, early}} {
		if got := SelectPositions(rows); got.Reason != "conflict" {
			t.Fatal("hid incomparable evidence", got)
		}
	}
}
