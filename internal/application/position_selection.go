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

	for i, p := range f.Positions {
		key := rowKey{c.InstrumentIDs[i], positionNumber(p.Quantity().Value()), p.Unit()}

		matches := indexes[key]
		if len(matches) == 0 {
			return false
		}

		index := matches[0]
		indexes[key] = matches[1:]

		prior := base.Source.Positions[index]
		if !CompatibleOption(prior.Instrument().Option(), p.Instrument().Option()) ||
			!CompatibleDebt(prior.Instrument().Debt(), p.Instrument().Debt()) {
			return false
		}

		x, y := prior.CostBasis(), p.CostBasis()
		if x.Amount() != "" && y.Amount() != "" && !x.Equal(y) {
			return false
		}

		if x.Amount() == "" {
			x = y
		}

		instrument, err := EnrichInstrument(prior.Instrument(), p.Instrument())
		if err != nil {
			return false
		}

		v, err := source.NewPosition(instrument, prior.Quantity(), x)
		if err != nil {
			return false
		}

		base.Source.Positions[index] = v
	}

	return true
}

// Remove acyclic revision nodes before treating remaining revision authority as uncertain.
func positionRevisionCycles(inputs []PositionCandidate) map[source.Reference]bool {
	edges := map[source.Reference][]source.Reference{}
	incoming := map[source.Reference]int{}
	for _, c := range inputs {
		f := c.Observation.Fields()
		if f.Reference.IsZero() {
			continue
		}

		if _, ok := incoming[f.Reference]; !ok {
			incoming[f.Reference] = 0
		}

		for _, r := range f.Supersedes {
			edges[f.Reference] = append(edges[f.Reference], r)
			incoming[r]++
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
