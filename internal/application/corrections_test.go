// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0
package application

import (
	"fmt"
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
		states["c"].Lifecycle() != Accepted ||
		statuses[0] != "applied" {
		t.Fatal(states, statuses)
	}
}

func TestCorrectionCalculationHandlesLongChainsAndWideAmbiguity(t *testing.T) {
	const size = 4000

	edges := make([]CorrectionEdge, 0, size+1)
	for i := range size {
		edges = append(
			edges,
			CorrectionEdge{
				Kind:         source.Replacement,
				Targets:      []string{fmt.Sprint(i)},
				Replacements: []string{fmt.Sprint(i + 1)},
			},
		)
	}

	states, statuses := CorrectionStates(edges)
	if len(states) != size+1 || len(statuses) != size || states["0"].Lifecycle() != Superseded ||
		states[fmt.Sprint(size-1)].Lifecycle() != Superseded {
		t.Fatal("long replacement chain changed meaning", len(states), len(statuses))
	}

	edges = append(
		edges,
		CorrectionEdge{
			Kind:         source.Replacement,
			Targets:      []string{fmt.Sprint(size)},
			Replacements: []string{"0"},
		},
	)
	states, statuses = CorrectionStates(edges)
	if len(states) != size+1 {
		t.Fatal("cycle lost related nodes", len(states))
	}

	for _, state := range states {
		if state.Lifecycle() != Accepted || state.ReviewReasons() != AmbiguousCorrection {
			t.Fatal("cycle partially applied", state)
		}
	}

	for _, status := range statuses {
		if status != CorrectionNeedsReview {
			t.Fatal("cycle partially accepted", status)
		}
	}

	targets := make([]string, size)
	for i := range targets {
		targets[i] = fmt.Sprint(i)
	}

	states, statuses = CorrectionStates(
		[]CorrectionEdge{{Kind: source.Replacement, Targets: targets, Replacements: []string{"replacement"}}},
	)
	if len(states) != size+1 || len(statuses) != 1 || statuses[0] != CorrectionNeedsReview {
		t.Fatal("wide ambiguity lost group", len(states), statuses)
	}
}

func TestResolvedCorrectionReturnsAcceptedReplacementState(t *testing.T) {
	pending, _ := CorrectionStates(
		[]CorrectionEdge{{Kind: source.Replacement, Replacements: []string{"replacement"}}},
	)
	resolved, _ := CorrectionStates(
		[]CorrectionEdge{
			{Kind: source.Replacement, Targets: []string{"original"}, Replacements: []string{"replacement"}},
		},
	)
	if !pending["replacement"].NeedsReview() || resolved["replacement"].NeedsReview() ||
		resolved["replacement"].Lifecycle() != Accepted ||
		resolved["original"].Lifecycle() != Superseded {
		t.Fatal("replacement review did not clear", pending, resolved)
	}
}
