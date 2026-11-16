// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package calculation

import (
	"sort"

	"go.trippwill.dev/wake/source"
)

type ActivitySubjectIndex struct {
	buckets map[[2]source.Reference]*activitySubjectCandidates
}

type activitySubjectCandidates struct {
	loose    []int
	distinct map[[3]string]map[source.Reference][]int
}

func activityEventNamespace(ref source.Reference) [3]string {
	return [3]string{ref.Authority(), ref.Kind(), ref.Scope()}
}

func ActivityReferences(value source.ActivityObservation) []source.Reference {
	f := value.Fields()
	out := []source.Reference{}
	if !f.Event.IsZero() {
		out = append(out, f.Event)
	}

	return append(out, f.EventAliases...)
}

func (index *ActivitySubjectIndex) Add(position int, value source.ActivityObservation) {
	if index.buckets == nil {
		index.buckets = make(map[[2]source.Reference]*activitySubjectCandidates)
	}

	subject := value.Fields()
	for _, ref := range ActivityReferences(value) {
		key := [2]source.Reference{subject.Account, ref}
		bucket := index.buckets[key]
		if bucket == nil {
			bucket = &activitySubjectCandidates{distinct: map[[3]string]map[source.Reference][]int{}}
			index.buckets[key] = bucket
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

// Candidates preserves pending order and qualified identity boundaries. A shared
// broad alias never makes distinct events in the same namespace candidates for
// each other, so a many-fill order does not reintroduce a quadratic scan.
func (index ActivitySubjectIndex) Candidates(value source.ActivityObservation) []int {
	subject := value.Fields()
	seen := map[int]bool{}

	add := func(positions []int) {
		for _, position := range positions {
			seen[position] = true
		}
	}
	for _, ref := range ActivityReferences(value) {
		bucket := index.buckets[[2]source.Reference{subject.Account, ref}]
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
