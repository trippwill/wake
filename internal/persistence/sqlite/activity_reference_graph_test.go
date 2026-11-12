// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"strconv"
	"testing"

	"go.trippwill.dev/wake/source"
)

func TestActivityReferenceGraphPreservesSharedAliasClosureAndOrder(t *testing.T) {
	s := newStoreWithSyntheticAccount(t)
	subject := partialTrade(t).Observation().Fields()
	family := partialField(t, source.ActivityFieldInput{Name: source.FieldFamily, Text: "trade"})
	shared, _ := source.NewReference("fixture", "group", "U1", "shared")

	const count = 512

	values := make([]source.ActivityObservation, count)
	for i := range values {
		input := subject
		input.Location = "row-" + strconv.Itoa(i)
		input.Event, _ = source.NewReference("fixture", "execution", "U1", strconv.Itoa(i))
		input.EventAliases = []source.Reference{shared}
		values[i] = partialObservation(t, input, family)
	}

	processPartial(t, s, "shared-batch", values...)

	other := subject
	other.Location = "unrelated"
	other.Event, _ = source.NewReference("fixture", "execution", "other-scope", "unrelated")
	otherAlias, _ := source.NewReference("fixture", "group", "other-scope", "shared")
	other.EventAliases = []source.Reference{otherAlias}
	processPartial(t, s, "unrelated", partialObservation(t, other, family))

	tail, _ := source.NewReference("fixture", "group", "U1", "tail")
	bridge := subject
	bridge.Location = "bridge"
	bridge.Event, _ = source.NewReference("fixture", "execution", "U1", "bridge")
	bridge.EventAliases = []source.Reference{shared, tail}
	processPartial(t, s, "bridge", partialObservation(t, bridge, family))

	end := subject
	end.Location = "tail"
	end.Event = tail
	end.EventAliases = nil
	end.EventDistinct = false
	processPartial(t, s, "tail", partialObservation(t, end, family))

	rows, err := s.queries.ActivityToMatch(t.Context(), "shared-batch")
	if err != nil || len(rows) != count+2 {
		t.Fatal("transitive reference closure changed", len(rows), err)
	}

	seen := map[string]bool{}
	for i, row := range rows {
		if row.ArtifactID == "unrelated" || seen[row.ClaimID] {
			t.Fatal("qualified scope crossed or claim duplicated", row.ArtifactID)
		}

		seen[row.ClaimID] = true
		if i < count && row.ArtifactID != "shared-batch" {
			t.Fatal("durable claim order changed", i, row.ArtifactID)
		}
	}

	if rows[count].ArtifactID != "bridge" || rows[count+1].ArtifactID != "tail" {
		t.Fatal("connected history order changed")
	}

	if tableCount(t, s, "executions") != 0 {
		t.Fatal("reference traversal invented complete activity")
	}
}

func TestActivityReferenceGraphSeedsLinkedAppearances(t *testing.T) {
	s := newStoreWithSyntheticAccount(t)
	full := partialTrade(t).Observation()
	processPartial(t, s, "first", full)
	processPartial(t, s, "appearance", full)

	rows, err := s.queries.ActivityToMatch(t.Context(), "appearance")
	if err != nil || len(rows) != 1 || rows[0].ArtifactID != "first" {
		t.Fatal("linked appearance did not reach retained original claim", rows, err)
	}
}

func BenchmarkActivityReferenceGraph(b *testing.B) {
	for _, count := range []int{64, 256, 1024} {
		b.Run(strconv.Itoa(count), func(b *testing.B) {
			s := newStoreWithSyntheticAccount(b)
			subject := partialTrade(b).Observation().Fields()

			shared, _ := source.NewReference("fixture", "group", "U1", "shared")
			family := partialField(b, source.ActivityFieldInput{Name: source.FieldFamily, Text: "trade"})
			values := make([]source.ActivityObservation, count)
			for i := range values {
				input := subject
				input.Location = "row-" + strconv.Itoa(i)
				input.Event, _ = source.NewReference("fixture", "execution", "U1", strconv.Itoa(i))
				input.EventAliases = []source.Reference{shared}
				values[i] = partialObservation(b, input, family)
			}

			processPartial(b, s, "batch", values...)
			b.ReportAllocs()
			b.ResetTimer()

			for range b.N {
				rows, err := s.queries.ActivityToMatch(b.Context(), "batch")
				if err != nil || len(rows) != count {
					b.Fatal("reference graph incomplete", len(rows), err)
				}
			}
		})
	}
}
