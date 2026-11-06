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

func TestPositionRevisionReplacementRetiresCycle(t *testing.T) {
	for _, date := range []string{"2026-11-03", "2026-11-02"} {
		t.Run(date, func(t *testing.T) {
			refs := make([]source.Reference, 3)
			for i, id := range []string{"a", "b", "replacement"} {
				refs[i], _ = source.NewReference("broker", "snapshot", "", id)
			}

			candidates := []PositionCandidate{
				positionCandidate(t, "2026-11-03", "", "1", "", ""),
				positionCandidate(t, "2026-11-03", "", "2", "", ""),
				positionCandidate(t, date, "", "3", "", ""),
			}
			for i := range candidates {
				f := candidates[i].Observation.Fields()
				f.Reference = refs[i]
				f.Supersedes = []source.Reference{refs[1]}
				if i == 1 {
					f.Supersedes = []source.Reference{refs[0]}
				}

				if i == 2 {
					f.Supersedes = refs[:2]
				}

				candidates[i].Observation, _ = source.NewPositionSetObservation(f)
			}

			if got := SelectPositions(candidates[:2]); got.Reason != "conflict" {
				t.Fatal("cycle published", got)
			}

			for _, rows := range [][]PositionCandidate{candidates, {candidates[2], candidates[1], candidates[0]}} {
				if got := SelectPositions(
					rows,
				); got.Reason != "" ||
					got.Source.Positions[0].Quantity().Value() != "3" {
					t.Fatal("replacement did not retire cycle", got)
				}
			}
		})
	}
}

func TestPositionDuplicateRowsRequireCompatibleOneToOneMatching(t *testing.T) {
	set := func(costs []string, quantity string) PositionCandidate {
		t.Helper()

		c := positionCandidate(t, "2026-11-03", "", quantity, costs[0], "")
		f := c.Observation.Fields()
		for _, cost := range costs[1:] {
			other := positionCandidate(t, "2026-11-03", "", quantity, cost, "")
			f.Positions = append(f.Positions, other.Observation.Fields().Positions[0])
			c.InstrumentIDs = append(c.InstrumentIDs, "instrument")
		}

		c.Observation, _ = source.NewPositionSetObservation(f)

		return c
	}
	for _, tc := range []struct {
		name       string
		base, next []string
		reason     string
	}{
		{"reordered equivalent scales", []string{"1.00", "2.00"}, []string{"2", "1"}, ""},
		{"optional cost requires reassignment", []string{"", "1"}, []string{"1.00", "2.00"}, ""},
		{"unknown costs stay unknown", []string{"", "1"}, []string{"1.00", ""}, ""},
		{"multiplicity differs", []string{"1", "2"}, []string{"1", "1"}, "conflict"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := SelectPositions(
				[]PositionCandidate{set(tc.base, "10.00"), set(tc.next, "10")},
			); got.Reason != tc.reason {
				t.Fatal(got)
			}

			if tc.name == "unknown costs stay unknown" {
				got := SelectPositions([]PositionCandidate{set(tc.base, "10.00"), set(tc.next, "10")})
				if got.Source.Positions[0].CostBasis().Amount() != "" {
					t.Fatal("invented certainty about an unknown cost", got)
				}
			}
		})
	}

	sets := []PositionCandidate{
		set([]string{"", "1"}, "10.00"),
		set([]string{"1.00", ""}, "10"),
		set([]string{"2", "1"}, "10"),
	}
	for _, order := range [][3]int{{0, 1, 2}, {0, 2, 1}, {1, 0, 2}, {1, 2, 0}, {2, 0, 1}, {2, 1, 0}} {
		got := SelectPositions([]PositionCandidate{sets[order[0]], sets[order[1]], sets[order[2]]})
		if got.Reason != "" {
			t.Fatal("compatible duplicate reports depend on input order", order, got)
		}

		costs := map[string]int{}
		for _, p := range got.Source.Positions {
			costs[positionNumber(p.CostBasis().Amount())]++
		}

		if costs["1"] != 1 || costs["2"] != 1 || len(costs) != 2 {
			t.Fatal("lost reported costs", order, costs)
		}
	}
}

func TestPositionDuplicateRowsPairReportedContractTerms(t *testing.T) {
	for _, kind := range []source.InstrumentKind{source.Option, source.Debt} {
		t.Run(string(kind), func(t *testing.T) {
			set := func(terms []string, costs ...string) PositionCandidate {
				t.Helper()

				c := positionCandidate(t, "2026-11-03", "", "1", "", "")

				f := c.Observation.Fields()
				f.Positions = nil
				c.InstrumentIDs = nil
				for i, term := range terms {
					input := source.InstrumentInput{Kind: kind, Name: "Synthetic contract"}
					if kind == source.Option {
						multiplier, _ := financial.NewQuantity(term)
						input.Option, _ = source.NewOptionTerms(source.OptionInput{Multiplier: multiplier})
					} else {
						maturity, _ := financial.ParseDate(term)
						input.Debt, _ = source.NewDebtTerms(source.DebtInput{Maturity: maturity})
					}

					instrument, err := source.NewInstrument(input)
					if err != nil {
						t.Fatal(err)
					}

					quantity, _ := financial.NewQuantity("1")

					cost := financial.Money{}
					if i < len(costs) && costs[i] != "" {
						usd, _ := financial.ParseCurrency("USD")
						cost, _ = financial.NewMoney(costs[i], usd)
					}

					p, err := source.NewPosition(instrument, quantity, cost)
					if err != nil {
						t.Fatal(err)
					}

					f.Positions = append(f.Positions, p)
					c.InstrumentIDs = append(c.InstrumentIDs, "instrument")
				}

				c.Observation, _ = source.NewPositionSetObservation(f)

				return c
			}
			terms := []string{"10", "100"}
			if kind == source.Debt {
				terms = []string{"2030-01-01", "2031-01-01"}
			}

			if got := SelectPositions(
				[]PositionCandidate{set(terms), set([]string{terms[1], terms[0]})},
			); got.Reason != "" {
				t.Fatal("reordered reported terms conflicted", got)
			}

			sets := []PositionCandidate{
				set(terms, "", "1"),
				set([]string{"", ""}, "1.00", ""),
				set(terms, "2", "1"),
			}
			for _, order := range [][3]int{{0, 1, 2}, {0, 2, 1}, {1, 0, 2}, {1, 2, 0}, {2, 0, 1}, {2, 1, 0}} {
				got := SelectPositions([]PositionCandidate{sets[order[0]], sets[order[1]], sets[order[2]]})
				if got.Reason != "" {
					t.Fatal("compatible contract reports depend on input order", order, got)
				}

				costs := map[string]string{}
				for _, p := range got.Source.Positions {
					term := p.Instrument().Option().Fields().Multiplier.Value()
					if kind == source.Debt {
						term = p.Instrument().Debt().Fields().Maturity.String()
					}

					costs[term] = positionNumber(p.CostBasis().Amount())
				}

				if costs[terms[0]] != "2" || costs[terms[1]] != "1" || len(costs) != 2 {
					t.Fatal("misassociated reported terms and costs", order, costs)
				}
			}
		})
	}
}
