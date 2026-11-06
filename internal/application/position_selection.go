// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package application

import (
	"context"
	"reflect"
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
func SelectPositions(ctx context.Context, inputs []PositionCandidate) (result PositionSelection, err error) {
	defer func() {
		if err == nil {
			err = ctx.Err()
		}
	}()

	result = PositionSelection{Reason: "not_reported"}
	if err = ctx.Err(); err != nil {
		return result, err
	}

	if len(inputs) == 0 {
		return result, nil
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
		return result, nil
	}

	base := latest[0]

	result.Source = base.Observation.Fields()
	result.InstrumentIDs = slices.Clone(base.InstrumentIDs)
	if result.Source.Date.IsZero() {
		result.Reason = "incomplete"
		return result, nil
	}

	for _, c := range latest {
		f := c.Observation.Fields()
		if cycles[f.Reference] {
			result.Reason = "conflict"
			return result, nil
		}

		if !f.Complete {
			result.Reason = f.Reason
			return result, nil
		}

		if !f.At.Equal(result.Source.At) {
			result.Reason = "conflict"
			return result, nil
		}

		if len(c.InstrumentIDs) != len(f.Positions) || slices.Contains(c.InstrumentIDs, "") {
			result.Reason = "identity_unresolved"
			return result, nil
		}
	}

	search := &positionPairingSearch{ctx: ctx, remaining: positionPairingSteps}
	if !mergePositionSets(search, &result, latest[1:]) {
		result.Reason = "conflict"
		if search.exhausted {
			result.Reason = "incomplete"
		}

		return result, nil
	}

	result.Reason = ""

	return result, nil
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

// Bound ambiguous multiset work across the entire account selection. Exhaustion
// establishes no financial disagreement; the set is incomplete for publication.
const positionPairingSteps = 10000

type positionPairingSearch struct {
	ctx       context.Context
	remaining int
	exhausted bool
}

func (s *positionPairingSearch) step() bool {
	if s.ctx.Err() != nil {
		return false
	}

	if s.remaining == 0 {
		s.exhausted = true
		return false
	}

	s.remaining--

	return true
}

// Reconcile each instrument/quantity multiset across all applicable reports before
// committing enrichment. Duplicate rows have no source-established row identity.
func mergePositionSets(
	search *positionPairingSearch,
	base *PositionSelection,
	candidates []PositionCandidate,
) bool {
	type rowKey struct{ id, quantity, unit string }

	key := func(id string, p source.Position) rowKey {
		return rowKey{id, positionNumber(p.Quantity().Value()), p.Unit()}
	}
	indexes := map[rowKey][]int{}
	for i, p := range base.Source.Positions {
		k := key(base.InstrumentIDs[i], p)
		indexes[k] = append(indexes[k], i)
	}

	reports := make([]map[rowKey][]source.Position, 0, len(candidates))
	for _, c := range candidates {
		f := c.Observation.Fields()
		if len(f.Positions) != len(base.Source.Positions) {
			return false
		}

		groups := map[rowKey][]source.Position{}
		for i, p := range f.Positions {
			k := key(c.InstrumentIDs[i], p)
			if _, ok := indexes[k]; !ok {
				return false
			}

			groups[k] = append(groups[k], p)
		}

		reports = append(reports, groups)
	}

	for k, rows := range indexes {
		if search.ctx.Err() != nil {
			return false
		}

		initial := make([]source.Position, 0, len(rows))
		for _, i := range rows {
			initial = append(initial, base.Source.Positions[i])
		}

		claims := make([][]source.Position, 0, len(reports))
		for _, report := range reports {
			if len(report[k]) != len(rows) {
				return false
			}

			claims = append(claims, report[k])
		}

		selected, ok := mergePositionGroup(search, initial, claims)
		if !ok {
			return false
		}

		for j, i := range rows {
			base.Source.Positions[i] = selected[j]
		}
	}

	return true
}

func mergePositionGroup(
	work *positionPairingSearch,
	initial []source.Position,
	reports [][]source.Position,
) ([]source.Position, bool) {
	current := slices.Clone(initial)
	if len(reports) == 0 {
		return current, true
	}

	if len(initial) == 1 {
		for _, report := range reports {
			if work.ctx.Err() != nil {
				return nil, false
			}

			var ok bool

			current[0], ok = mergePositionRow(current[0], report[0])
			if !ok {
				return nil, false
			}
		}

		return current, true
	}

	var (
		consensus []source.Position
		search    func(int, int, []bool) bool
	)

	search = func(report, row int, used []bool) bool {
		if !work.step() {
			return true
		}

		if report == len(reports) {
			if consensus == nil {
				consensus = slices.Clone(current)
			} else {
				for i, p := range current {
					if !work.step() {
						return true
					}

					instrument, cost := consensus[i].Instrument(), consensus[i].CostBasis()
					if !samePositionTerms(instrument, p.Instrument()) {
						instrument = initial[i].Instrument()
					}

					if !cost.Equal(p.CostBasis()) {
						cost = initial[i].CostBasis()
					}

					consensus[i], _ = source.NewPosition(instrument, initial[i].Quantity(), cost)
				}
			}
			// No further solution can remove source facts already in the baseline.
			return slices.EqualFunc(consensus, initial, samePositionRow)
		}

		if row == len(initial) {
			return search(report+1, 0, make([]bool, len(initial)))
		}

		// Swapping indistinguishable rows creates the same financial solution.
		seen := []source.Position{}
		for i, prior := range current {
			if !work.step() {
				return true
			}

			if used[i] {
				continue
			}

			equivalent := false
			for _, p := range seen {
				if !work.step() {
					return true
				}

				if samePositionRow(p, prior) {
					equivalent = true
					break
				}
			}

			if equivalent {
				continue
			}

			seen = append(seen, prior)

			merged, ok := mergePositionRow(prior, reports[report][row])
			if !ok {
				continue
			}

			current[i], used[i] = merged, true
			stop := search(report, row+1, used)
			current[i], used[i] = prior, false

			if stop {
				return true
			}
		}

		return false
	}
	search(0, 0, make([]bool, len(initial)))

	if work.exhausted || work.ctx.Err() != nil {
		return nil, false
	}

	return consensus, consensus != nil
}

func samePositionRow(a, b source.Position) bool {
	return a.Quantity().Equal(b.Quantity()) && a.Unit() == b.Unit() &&
		a.CostBasis().Equal(b.CostBasis()) && samePositionTerms(a.Instrument(), b.Instrument())
}

func samePositionTerms(a, b source.Instrument) bool {
	if !CompatibleOption(a.Option(), b.Option()) || !CompatibleDebt(a.Debt(), b.Debt()) {
		return false
	}

	left, leftErr := EnrichInstrument(a, b)
	right, rightErr := EnrichInstrument(b, a)

	return leftErr == nil && rightErr == nil && reflect.DeepEqual(left, a) && reflect.DeepEqual(right, b)
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
