// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0
package application

import (
	"testing"

	"go.trippwill.dev/wake/source"
)

func TestCorrectionGraphRejectsCyclesCompetitionAndAmbiguity(t *testing.T) {
	for _, edges := range [][]CorrectionEdge{
		{{Kind: source.Replacement, Targets: []string{"a"}, Replacements: []string{"b"}}, {Kind: source.Replacement, Targets: []string{"b"}, Replacements: []string{"a"}}},
		{{Kind: source.Replacement, Targets: []string{"a"}, Replacements: []string{"b"}}, {Kind: source.Replacement, Targets: []string{"a"}, Replacements: []string{"c"}}},
		{{Kind: source.Cancellation, Targets: []string{"a", "b"}}},
	} {
		states, statuses := CorrectionStates(edges)
		for _, status := range states {
			if !status.NeedsReview() {
				t.Fatal(states)
			}
		}

		for _, status := range statuses {
			if !status.NeedsReview() {
				t.Fatal(statuses)
			}
		}
	}

	states, statuses := CorrectionStates(
		[]CorrectionEdge{
			{Kind: source.Replacement, Targets: []string{"a"}, Replacements: []string{"b"}},
			{Kind: source.Replacement, Targets: []string{"b"}, Replacements: []string{"c"}},
		},
	)
	if states["a"].Lifecycle() != Superseded || states["b"].Lifecycle() != Superseded ||
		states["c"].Lifecycle() != "" ||
		statuses[0] != "applied" {
		t.Fatal(states, statuses)
	}
}
