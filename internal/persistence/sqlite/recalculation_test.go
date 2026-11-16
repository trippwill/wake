// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0
package persistence

import (
	"context"
	"testing"

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/persistence/sqlite/sqlc"
	"go.trippwill.dev/wake/source"
)

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
		if err = projectInstrument(ctx, q, id, application.RetainedClaimsRule); err != nil {
			t.Fatal(err)
		}
	}

	activities, err := q.Executions(ctx)
	if err != nil {
		t.Fatal(err)
	}

	for _, row := range activities {
		if err = projectActivity(ctx, q, row, states[row.ID], application.RetainedClaimsRule); err != nil {
			t.Fatal(err)
		}
	}

	if err = s.commit(ctx, tx); err != nil {
		t.Fatal(err)
	}
}

func correctionInputs(
	ctx context.Context,
	q *sqlc.Queries,
) ([]sqlc.SourceCorrection, []application.CorrectionEdge, error) {
	rows, err := q.SourceCorrections(ctx)
	if err != nil {
		return nil, nil, err
	}

	edges := make([]application.CorrectionEdge, len(rows))
	for i, r := range rows {
		targets, e := q.EventLinks(ctx, sqlc.EventLinksParams{AccountID: r.AccountID, Reference: r.Target})
		if e != nil {
			return nil, nil, e
		}

		replacements, e := q.EventLinks(
			ctx,
			sqlc.EventLinksParams{AccountID: r.AccountID, Reference: r.Replacement},
		)
		if e != nil {
			return nil, nil, e
		}

		edges[i] = application.CorrectionEdge{
			Kind:         source.CorrectionKind(r.Kind),
			Targets:      targets,
			Replacements: replacements,
		}
	}

	return rows, edges, nil
}

func readCorrectionStates(
	ctx context.Context,
	q *sqlc.Queries,
) (map[string]application.ActivityState, error) {
	_, edges, err := correctionInputs(ctx, q)
	if err != nil {
		return nil, err
	}

	states, _ := application.CorrectionStates(edges)

	return states, nil
}

func correctionStates(ctx context.Context, q *sqlc.Queries) (map[string]application.ActivityState, error) {
	rows, edges, err := correctionInputs(ctx, q)
	if err != nil {
		return nil, err
	}

	states, statuses := application.CorrectionStates(edges)
	for i, r := range rows {
		if r.Status == string(statuses[i]) {
			continue
		}

		if err = q.CorrectionStatus(
			ctx,
			sqlc.CorrectionStatusParams{
				ArtifactID: r.ArtifactID,
				Version:    r.Version,
				Location:   r.Location,
				Status:     string(statuses[i]),
			},
		); err != nil {
			return nil, err
		}
	}

	return states, nil
}
