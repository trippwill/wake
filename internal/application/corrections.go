// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0
package application

import "go.trippwill.dev/wake/source"

type (
	correctionAction struct {
		kind        source.CorrectionKind
		replacement string
	}
	correctionVisit uint8
)

const (
	unvisitedCorrection correctionVisit = iota
	visitingCorrection
	visitedCorrection
)

type CorrectionEdge struct {
	Kind                  source.CorrectionKind
	Targets, Replacements []string
}

// CorrectionStates evaluates whole connected groups, so a competing instruction
// or cycle cannot partially apply based on which report arrived first.
func CorrectionStates(edges []CorrectionEdge) (map[string]ActivityState, []CorrectionStatus) {
	states := map[string]ActivityState{}

	out := make([]CorrectionStatus, len(edges))
	adj := map[string][]string{}
	members := map[string][]int{}
	for i, e := range edges {
		ids := append(append([]string{}, e.Targets...), e.Replacements...)
		for _, id := range ids {
			members[id] = append(members[id], i)
			if len(ids) > 0 && id != ids[0] {
				adj[id] = append(adj[id], ids[0])
				adj[ids[0]] = append(adj[ids[0]], id)
			} else {
				if _, ok := adj[id]; !ok {
					adj[id] = nil
				}
			}
		}
	}

	seen := map[string]bool{}
	for root := range adj {
		if seen[root] {
			continue
		}

		nodes := []string{}
		group := map[int]bool{}
		queue := []string{root}
		for len(queue) > 0 {
			id := queue[0]
			queue = queue[1:]

			if seen[id] {
				continue
			}

			seen[id] = true
			nodes = append(nodes, id)
			for _, i := range members[id] {
				group[i] = true
			}

			queue = append(queue, adj[id]...)
		}

		invalid := false

		actions := map[string]correctionAction{}
		next := map[string]string{}
		replacementTargets := map[string]string{}
		for i := range group {
			e := edges[i]
			if len(e.Targets) != 1 || e.Kind == source.UnclearCorrection ||
				(e.Kind == source.Replacement && len(e.Replacements) != 1) {
				invalid = true
				continue
			}

			target := e.Targets[0]
			action := correctionAction{kind: e.Kind}
			if e.Kind == source.Replacement {
				replacement := e.Replacements[0]
				action.replacement = replacement
				next[target] = replacement
				if previous := replacementTargets[replacement]; previous != "" && previous != target {
					invalid = true
				}

				replacementTargets[replacement] = target
				if target == replacement {
					invalid = true
				}
			}

			if previous, ok := actions[target]; ok && previous != action {
				invalid = true
			}

			actions[target] = action
		}

		visits := map[string]correctionVisit{}
		for start := range next {
			if visits[start] == visitedCorrection {
				continue
			}

			path := []string{}
			id := start
			for id != "" && visits[id] == unvisitedCorrection {
				visits[id] = visitingCorrection
				path = append(path, id)
				id = next[id]
			}

			if id != "" && visits[id] == visitingCorrection {
				invalid = true
			}

			for _, id := range path {
				visits[id] = visitedCorrection
			}
		}

		if invalid {
			for _, id := range nodes {
				states[id], _ = NewActivityState(Accepted, AmbiguousCorrection, AutomaticSelection)
			}

			for i := range group {
				out[i] = CorrectionNeedsReview
			}

			continue
		}

		// Return the complete group state, including accepted replacement leaves.
		// A previously unresolved group may have marked those leaves for review.
		for _, id := range nodes {
			states[id], _ = NewActivityState(Accepted, 0, AutomaticSelection)
		}

		for i := range group {
			e := edges[i]
			out[i] = CorrectionApplied

			if e.Kind == source.Cancellation {
				states[e.Targets[0]], _ = NewActivityState(Cancelled, 0, AutomaticSelection)
			} else {
				states[e.Targets[0]], _ = NewActivityState(Superseded, 0, AutomaticSelection)
			}
		}
	}

	for i := range out {
		if out[i] == "" {
			out[i] = CorrectionUnresolved
		}
	}

	return states, out
}
