// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package application

import (
	"slices"
	"strings"

	"go.trippwill.dev/wake/source"
)

// PositionCandidate carries bindings independently of the retained source claims.
type PositionCandidate struct {
	Observation   source.PositionSetObservation
	InstrumentIDs []string
}

type PositionSelection struct {
	Source        source.PositionSetInput
	InstrumentIDs []string
	Reason        string
}

// SelectPositions uses financial effective time and explicit supersession only.
// Input order retains the first compatible text representation, never authority.
func SelectPositions(inputs []PositionCandidate) PositionSelection {
	result := PositionSelection{Reason: "not_reported"}
	if len(inputs) == 0 {
		return result
	}

	cycles := positionRevisionCycles(inputs)
	superseded := map[source.Reference]bool{}
	for _, c := range inputs {
		for _, r := range c.Observation.Fields().Supersedes {
			superseded[r] = true
		}
	}

	active := []PositionCandidate{}
	latestDate := ""
	for _, c := range inputs {
		f := c.Observation.Fields()
		if !f.Reference.IsZero() && superseded[f.Reference] && !cycles[f.Reference] {
			continue
		}

		active = append(active, c)

		if !f.Date.IsZero() && f.Date.String() > latestDate {
			latestDate = f.Date.String()
		}
	}

	latest := []PositionCandidate{}
	dateOnly := false

	var newest PositionCandidate
	for _, c := range active {
		f := c.Observation.Fields()
		if latestDate != "" && (f.Date.IsZero() || f.Date.String() != latestDate) {
			continue
		}

		latest = append(latest, c)
		dateOnly = dateOnly || f.At.IsZero()
		if newest.Observation.IsZero() || f.At.Time().After(newest.Observation.Fields().At.Time()) {
			newest = c
		}
	}
	// A date-only assertion overlaps every timed assertion on that financial date.
	// Find the latest bucket before pruning so input ordering cannot hide it.
	if !dateOnly && len(latest) > 0 {
		at := newest.Observation.Fields().At
		latest = slices.DeleteFunc(latest, func(c PositionCandidate) bool {
			t := c.Observation.Fields().At
			return !t.Contains(at) && !at.Contains(t)
		})
	}

	if len(latest) == 0 {
		result.Reason = "conflict"
		return result
	}

	base := latest[0]

	result.Source = base.Observation.Fields()
	result.InstrumentIDs = slices.Clone(base.InstrumentIDs)
	if result.Source.Date.IsZero() {
		result.Reason = "incomplete"
		return result
	}

	for _, c := range latest {
		f := c.Observation.Fields()
		if cycles[f.Reference] {
			result.Reason = "conflict"
			return result
		}

		if !f.Complete {
			result.Reason = f.Reason
			return result
		}

		if !f.At.Equal(result.Source.At) {
			result.Reason = "conflict"
			return result
		}

		if len(c.InstrumentIDs) != len(f.Positions) || slices.Contains(c.InstrumentIDs, "") {
			result.Reason = "identity_unresolved"
			return result
		}

		if !mergePositionRows(&result, c) {
			result.Reason = "conflict"
			return result
		}
	}

	result.Reason = ""

	return result
}

func positionNumber(s string) string {
	if strings.Contains(s, ".") {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}

	if s == "-0" {
		return "0"
	}

	return s
}

func mergePositionRows(base *PositionSelection, c PositionCandidate) bool {
	f := c.Observation.Fields()
	if len(base.Source.Positions) != len(f.Positions) || len(base.InstrumentIDs) != len(base.Source.Positions) {
		return false
	}

	type rowKey struct{ id, quantity, unit string }

	indexes := map[rowKey][]int{}
	for i, p := range base.Source.Positions {
		key := rowKey{base.InstrumentIDs[i], positionNumber(p.Quantity().Value()), p.Unit()}
		indexes[key] = append(indexes[key], i)
	}

	// Optional terms can match more than one duplicate row. Reassign earlier pairs
	// when necessary so one broad match cannot consume another row's only partner.
	paired := make([]int, len(base.Source.Positions))
	for i := range paired {
		paired[i] = -1
	}

	var match func(int, map[int]bool) bool

	match = func(i int, seen map[int]bool) bool {
		p := f.Positions[i]
		key := rowKey{c.InstrumentIDs[i], positionNumber(p.Quantity().Value()), p.Unit()}
		for _, occupied := range []bool{false, true} {
			for _, index := range indexes[key] {
				if seen[index] || (paired[index] >= 0) != occupied {
					continue
				}

				if _, ok := mergePositionRow(base.Source.Positions[index], p); !ok {
					continue
				}

				seen[index] = true
				if !occupied || match(paired[index], seen) {
					paired[index] = i
					return true
				}
			}
		}

		return false
	}
	for i := range f.Positions {
		if !match(i, map[int]bool{}) {
			return false
		}
	}

	for index, i := range paired {
		base.Source.Positions[index], _ = mergePositionRow(base.Source.Positions[index], f.Positions[i])
	}

	return true
}

func mergePositionRow(prior, next source.Position) (source.Position, bool) {
	if !CompatibleOption(prior.Instrument().Option(), next.Instrument().Option()) ||
		!CompatibleDebt(prior.Instrument().Debt(), next.Instrument().Debt()) {
		return source.Position{}, false
	}

	x, y := prior.CostBasis(), next.CostBasis()
	if x.Amount() != "" && y.Amount() != "" && !x.Equal(y) {
		return source.Position{}, false
	}

	if x.Amount() == "" {
		x = y
	}

	instrument, err := EnrichInstrument(prior.Instrument(), next.Instrument())
	if err != nil {
		return source.Position{}, false
	}

	v, err := source.NewPosition(instrument, prior.Quantity(), x)

	return v, err == nil
}

// Cycles and their revision targets have uncertain authority until an unambiguous
// replacement supersedes them. Re-evaluate after retiring authoritative targets.
func positionRevisionCycles(inputs []PositionCandidate) map[source.Reference]bool {
	edges := map[source.Reference][]source.Reference{}
	for _, c := range inputs {
		f := c.Observation.Fields()
		if f.Reference.IsZero() {
			continue
		}

		edges[f.Reference] = append(edges[f.Reference], f.Supersedes...)
	}

	retired := map[source.Reference]bool{}
	for {
		cycles := unresolvedPositionRevisions(edges, retired)
		changed := false
		for ref, targets := range edges {
			if cycles[ref] {
				continue
			}

			for _, target := range targets {
				if !retired[target] {
					retired[target] = true
					changed = true
				}
			}
		}

		if !changed {
			return cycles
		}
	}
}

func unresolvedPositionRevisions(
	edges map[source.Reference][]source.Reference,
	retired map[source.Reference]bool,
) map[source.Reference]bool {
	incoming := map[source.Reference]int{}
	for ref, targets := range edges {
		if retired[ref] {
			continue
		}

		incoming[ref] += 0
		for _, target := range targets {
			if !retired[target] {
				incoming[target]++
			}
		}
	}

	queue := []source.Reference{}
	for ref, n := range incoming {
		if n == 0 {
			queue = append(queue, ref)
		}
	}

	for i := 0; i < len(queue); i++ {
		for _, r := range edges[queue[i]] {
			if retired[r] {
				continue
			}

			incoming[r]--
			if incoming[r] == 0 {
				queue = append(queue, r)
			}
		}
	}

	cycles := map[source.Reference]bool{}
	for ref, n := range incoming {
		if n > 0 {
			cycles[ref] = true
		}
	}

	return cycles
}
