// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package application

import (
	"testing"

	"go.trippwill.dev/wake/internal/application/calculation"
)

func TestObservationLabelsAreImmutableAndScoped(t *testing.T) {
	labels := map[calculation.ObservationOutcome]string{calculation.ObservationAccepted: "accepted"}

	query, err := NewListQuery(ListOptions{ObservationLabels: labels}, ObservationScope)
	if err != nil {
		t.Fatal(err)
	}

	labels[calculation.ObservationAccepted] = "changed"
	returned := query.Options()
	returned.ObservationLabels[calculation.ObservationAccepted] = "also changed"
	if query.Options().ObservationLabels[calculation.ObservationAccepted] != "accepted" {
		t.Fatal("mutable presentation vocabulary")
	}

	for _, options := range []ListOptions{{ObservationLabels: map[calculation.ObservationOutcome]string{"unknown": "label"}}, {ObservationLabels: map[calculation.ObservationOutcome]string{calculation.ObservationAccepted: ""}}} {
		if _, err := NewListQuery(options, ObservationScope); err == nil {
			t.Fatal("invalid vocabulary accepted", options)
		}
	}

	if _, err := NewListQuery(ListOptions{ObservationLabels: labels}, ActivityScope); err == nil {
		t.Fatal("observation vocabulary accepted for activity")
	}
}
