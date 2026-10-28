// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0
package application

import "go.trippwill.dev/wake/source"

type CorrectionEdge struct {
	Kind                  source.CorrectionKind
	Targets, Replacements []string
}

// CorrectionStates evaluates whole connected groups, so a competing instruction
// or cycle cannot partially apply based on which report arrived first.
func CorrectionStates(edges []CorrectionEdge) (map[string]string, []string) {
	states := map[string]string{}

	out := make([]string, len(edges))
	adj := map[string][]string{}
	members := map[string][]int{}
	for i, e := range edges {
		ids := append(append([]string{}, e.Targets...), e.Replacements...)
		for _, id := range ids {
			members[id] = append(members[id], i)
			adj[id] = append(adj[id], ids...)
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

		actions := map[string]string{}
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
			action := string(e.Kind)
			if e.Kind == source.Replacement {
				replacement := e.Replacements[0]
				action += "/" + replacement
				next[target] = replacement
				if previous := replacementTargets[replacement]; previous != "" && previous != target {
					invalid = true
				}

				replacementTargets[replacement] = target
				if target == replacement {
					invalid = true
				}
			}

			if previous := actions[target]; previous != "" && previous != action {
				invalid = true
			}

			actions[target] = action
		}

		for start := range next {
			path := map[string]bool{}
			for id := start; id != ""; id = next[id] {
				if path[id] {
					invalid = true
					break
				}

				path[id] = true
			}
		}

		if invalid {
			for _, id := range nodes {
				states[id] = "needs review"
			}

			for i := range group {
				out[i] = "needs review"
			}

			continue
		}

		for i := range group {
			e := edges[i]
			out[i] = "applied"

			if e.Kind == source.Cancellation {
				states[e.Targets[0]] = "cancelled"
			} else {
				states[e.Targets[0]] = "superseded"
			}
		}
	}

	for i := range out {
		if out[i] == "" {
			out[i] = "endpoint unresolved; needs review"
		}
	}

	return states, out
}
