// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"context"
	"database/sql"

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/persistence/sqlite/sqlc"
)

type preparedCorrection struct {
	row        sqlc.Execution
	activity   application.ActivityCalculation
	instrument application.InstrumentCalculation
}

// prepareCorrection evaluates exactly the prospective retained decision set used
// by saving. All data belongs to the caller's transaction; evaluation writes nothing.
func prepareCorrection(
	ctx context.Context,
	q *sqlc.Queries,
	f application.DecisionInput,
) (preparedCorrection, error) {
	var out preparedCorrection
	if _, err := application.NewDecision(f); err != nil {
		return out, err
	}

	if f.TargetKind == "activity" {
		row, err := q.Execution(ctx, f.TargetID)
		if err != nil {
			return out, err
		}

		if row.Revision != f.ExpectedRevision {
			return out, application.ErrStale
		}

		states, err := readCorrectionStates(ctx, q)
		if err != nil {
			return out, err
		}

		in, err := loadActivityCalculation(ctx, q, row, states[row.ID], &f)
		if err != nil {
			return out, err
		}

		out.row = row
		out.activity, err = application.CalculateActivity(in)

		return out, err
	}

	row, err := q.Instrument(ctx, f.TargetID)
	if err != nil {
		return out, err
	}

	if row.Revision != f.ExpectedRevision {
		return out, application.ErrStale
	}

	in, err := loadInstrumentCalculation(ctx, q, f.TargetID, &f)
	if err != nil {
		return out, err
	}

	out.instrument, err = application.CalculateInstrument(in)

	return out, err
}

// CheckCorrection uses the same pure calculation as saving, in one consistent
// read. It does not write decisions, projections, revisions or operational work.
func (s *Store) CheckCorrection(ctx context.Context, decision application.Decision) error {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	_, err = prepareCorrection(ctx, s.queries.WithTx(tx), decision.Fields())

	return err
}
