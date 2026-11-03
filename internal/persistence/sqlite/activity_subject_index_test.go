// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"reflect"
	"strconv"
	"testing"

	"go.trippwill.dev/wake/source"
)

func TestPendingSubjectMatchingSkipsUnrelatedExecutions(t *testing.T) {
	for _, broadAlias := range []bool{false, true} {
		t.Run(strconv.FormatBool(broadAlias), func(t *testing.T) {
			const count = 2048

			account, _ := source.NewReference("synthetic", "account", "", "one")

			alias, _ := source.NewReference("synthetic", "order", "one", "shared")
			family, _ := source.NewActivityField(source.ActivityFieldInput{Name: source.FieldFamily, Text: "trade"})
			pending := make([]pendingActivity, count)
			for i := range pending {
				event, _ := source.NewReference("synthetic", "execution", "one", strconv.Itoa(i))

				subject := source.ActivityObservationInput{
					Location:      strconv.Itoa(i),
					Account:       account,
					Event:         event,
					EventDistinct: true,
					Fields:        []source.ActivityField{family},
				}
				if broadAlias {
					subject.EventAliases = []source.Reference{alias}
				}

				observation, err := source.NewActivityObservation(subject)
				if err != nil {
					t.Fatal(err)
				}

				pending[i].value = observation
			}

			index := newActivitySubjectIndex(pending)
			comparisons := 0
			for i, item := range pending {
				candidates := index.candidates(item.value)
				comparisons += len(candidates)
				if len(candidates) != 1 || candidates[0] != i {
					t.Fatalf(
						"execution %d reached %d candidates; unrelated executions must not be compared",
						i,
						len(candidates),
					)
				}
			}

			if comparisons != count {
				t.Fatal("matching grew with unrelated history", comparisons)
			}
		})
	}
}

func TestPendingSubjectIndexPreservesQualifiedIdentityRules(t *testing.T) {
	reference := func(kind, scope, value string) source.Reference {
		v, err := source.NewReference("synthetic", kind, scope, value)
		if err != nil {
			t.Fatal(err)
		}

		return v
	}
	account := reference("account", "", "one")
	otherAccount := reference("account", "", "two")
	first := reference("execution", "one", "first")
	second := reference("execution", "one", "second")
	broad := reference("order", "one", "shared")

	family, _ := source.NewActivityField(source.ActivityFieldInput{Name: source.FieldFamily, Text: "trade"})
	inputs := []source.ActivityObservationInput{
		{Account: account, Event: first, EventDistinct: true, EventAliases: []source.Reference{broad, first}},
		{Account: account, Event: second, EventDistinct: true, EventAliases: []source.Reference{broad}},
		{Account: account, Event: broad},
		{Account: account, Event: first},
		{Account: otherAccount, Event: first, EventDistinct: true},
		{
			Account:       account,
			Event:         reference("reported-event", "one", "result"),
			EventDistinct: true,
			EventAliases:  []source.Reference{first},
		},
		{Account: account, Event: reference("execution", "two", "first"), EventDistinct: true},
	}
	pending := make([]pendingActivity, len(inputs))
	for i, input := range inputs {
		input.Location = strconv.Itoa(i)
		input.Fields = []source.ActivityField{family}

		value, err := source.NewActivityObservation(input)
		if err != nil {
			t.Fatal(err)
		}

		pending[i].value = value
	}

	markAmbiguousSubjects(pending)

	if !pending[2].ambiguous {
		t.Fatal("broad alias must remain ambiguous")
	}

	index := newActivitySubjectIndex(pending)
	for i, item := range pending {
		want := []int{}
		for j, candidate := range pending {
			if !candidate.ambiguous && compatibleSubjects(item.value, candidate.value) {
				want = append(want, j)
			}
		}

		got := index.candidates(item.value)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("subject %d: indexed candidates %v differ from qualified identity matches %v", i, got, want)
		}
	}
}
