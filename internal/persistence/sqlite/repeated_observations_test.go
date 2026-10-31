// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestRepeatedObservationsRetainEvidenceWithoutPublishedChanges(t *testing.T) {
	s := coverageStore(t)
	lifecycle := strings.NewReplacer(`execID="option-fill"`, `execID="exercise-fill"`, `tradeID="option-trade"`, `tradeID="exercise-trade"`, `transactionType="ExchTrade"`, `transactionType="BookTrade" code="Ex"`).
		Replace(syntheticOption)
	rows := []string{equityRow("U1", "equity", "100"), syntheticOption, syntheticDebt, syntheticFX, lifecycle}
	coverageReport(t, s, "base", strings.Join(rows, ""))

	before, err := snapshotResources(t.Context(), s, "activity")
	if err != nil || len(before.Items) != 5 {
		t.Fatal(before, err)
	}

	baseline, err := snapshotState(t.Context(), s)
	if err != nil {
		t.Fatal(err)
	}

	for i := range 32 {
		// Vary row locations without changing any financial observations.
		rows = append(rows[1:], rows[0])
		coverageReport(t, s, fmt.Sprintf("repeat-%d", i), strings.Join(rows, ""))
	}

	after, err := snapshotResources(t.Context(), s, "activity")
	if err != nil || !reflect.DeepEqual(before.Items, after.Items) {
		t.Fatal("repeated observations changed canonical data", before, after, err)
	}

	if changes := snapshotChanges(t, s, baseline); len(changes.Items) != 0 {
		t.Fatal("repeated observations published transitions", changes.Items)
	}

	var count int
	if err = s.db.QueryRow(`SELECT count(*) FROM execution_observations WHERE execution_id IS NOT NULL`).
		Scan(&count); err != nil ||
		count != 33*len(rows) {
		t.Fatal("comparison deduplication lost retained evidence", count, err)
	}
}
