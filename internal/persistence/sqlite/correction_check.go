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
	instrument application.InstrumentCalculation
}

// prepareInstrumentCorrection evaluates exactly the prospective retained decision set used
// by saving. All data belongs to the caller's transaction; evaluation writes nothing.
func prepareInstrumentCorrection(
	ctx context.Context,
	q *sqlc.Queries,
	f application.DecisionInput,
) (preparedCorrection, error) {
	var out preparedCorrection
	if f.TargetKind != "instrument" {
		return out, application.ErrInvalid
	}

	if _, err := application.NewDecision(f); err != nil {
		return out, err
	}

	target, err := canonicalID(ctx, q, "instruments", f.TargetID)
	if err != nil {
		return out, err
	}

	if target != f.TargetID {
		return out, application.ErrStale
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
func (s *Store) CheckInstrumentCorrection(ctx context.Context, decision application.Decision) error {
	if s.HistoryUpdating() {
		return application.ErrHistoryUpdating
	}

	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}

	defer func() { _ = tx.Rollback() }()

	_, err = prepareInstrumentCorrection(ctx, s.queries.WithTx(tx), decision.Fields())

	return err
}
