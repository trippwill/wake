// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package application

import "testing"

func TestObservationLabelsAreImmutableAndScoped(t *testing.T) {
	labels := map[ObservationOutcome]string{ObservationAccepted: "accepted"}

	query, err := NewListQuery(ListOptions{ObservationLabels: labels}, ObservationScope)
	if err != nil {
		t.Fatal(err)
	}

	labels[ObservationAccepted] = "changed"
	returned := query.Options()
	returned.ObservationLabels[ObservationAccepted] = "also changed"
	if query.Options().ObservationLabels[ObservationAccepted] != "accepted" {
		t.Fatal("mutable presentation vocabulary")
	}

	for _, options := range []ListOptions{{ObservationLabels: map[ObservationOutcome]string{"unknown": "label"}}, {ObservationLabels: map[ObservationOutcome]string{ObservationAccepted: ""}}} {
		if _, err := NewListQuery(options, ObservationScope); err == nil {
			t.Fatal("invalid vocabulary accepted", options)
		}
	}

	if _, err := NewListQuery(ListOptions{ObservationLabels: labels}, ActivityScope); err == nil {
		t.Fatal("observation vocabulary accepted for activity")
	}
}
