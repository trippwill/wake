// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"sort"

	"go.trippwill.dev/wake/source"
)

type activitySubjectIndex map[[2]source.Reference]*activitySubjectCandidates

type activitySubjectCandidates struct {
	loose    []int
	distinct map[[3]string]map[source.Reference][]int
}

func activityEventNamespace(ref source.Reference) [3]string {
	return [3]string{ref.Authority(), ref.Kind(), ref.Scope()}
}

func newActivitySubjectIndex(pending []pendingActivity) activitySubjectIndex {
	index := activitySubjectIndex{}
	for position, item := range pending {
		if item.ambiguous {
			continue
		}

		subject := item.value.Fields()
		for _, ref := range activityReferences(item.value) {
			key := [2]source.Reference{subject.Account, ref}
			bucket := index[key]
			if bucket == nil {
				bucket = &activitySubjectCandidates{distinct: map[[3]string]map[source.Reference][]int{}}
				index[key] = bucket
			}

			if !subject.EventDistinct {
				bucket.loose = append(bucket.loose, position)
				continue
			}

			namespace := activityEventNamespace(subject.Event)
			if bucket.distinct[namespace] == nil {
				bucket.distinct[namespace] = map[source.Reference][]int{}
			}

			bucket.distinct[namespace][subject.Event] = append(bucket.distinct[namespace][subject.Event], position)
		}
	}

	return index
}

// candidates preserves pending order and qualified identity boundaries. A shared
// broad alias never makes distinct events in the same namespace candidates for
// each other, so a many-fill order does not reintroduce a quadratic scan.
func (index activitySubjectIndex) candidates(value source.ActivityObservation) []int {
	subject := value.Fields()
	seen := map[int]bool{}

	add := func(positions []int) {
		for _, position := range positions {
			seen[position] = true
		}
	}
	for _, ref := range activityReferences(value) {
		bucket := index[[2]source.Reference{subject.Account, ref}]
		if bucket == nil {
			continue
		}

		add(bucket.loose)

		for namespace, events := range bucket.distinct {
			if subject.EventDistinct && namespace == activityEventNamespace(subject.Event) {
				add(events[subject.Event])
				continue
			}

			for _, positions := range events {
				add(positions)
			}
		}
	}

	result := make([]int, 0, len(seen))
	for position := range seen {
		result = append(result, position)
	}

	sort.Ints(result)

	return result
}
