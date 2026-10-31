// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0
package persistence

import "testing"

// This test-only reconstruction proves retained-history calculation invariants.
// It is deliberately not an operator API, scheduler or update framework.
func recalculateFixture(t *testing.T, s *Store) {
	t.Helper()
	ctx := t.Context()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = tx.Rollback() }()

	q := s.queries.WithTx(tx)

	states, err := correctionStates(ctx, q)
	if err != nil {
		t.Fatal(err)
	}

	instruments, err := q.Instruments(ctx)
	if err != nil {
		t.Fatal(err)
	}

	ids := make([]string, 0, len(instruments))
	for _, i := range instruments {
		ids = append(ids, i.ID)
	}

	if err = projectRoots(ctx, q, ids); err != nil {
		t.Fatal(err)
	}

	for _, id := range ids {
		if err = projectInstrument(ctx, q, id); err != nil {
			t.Fatal(err)
		}
	}

	activities, err := q.Executions(ctx)
	if err != nil {
		t.Fatal(err)
	}

	for _, row := range activities {
		if err = projectActivity(ctx, q, row, states[row.ID]); err != nil {
			t.Fatal(err)
		}
	}

	if err = s.commit(ctx, tx); err != nil {
		t.Fatal(err)
	}
}
