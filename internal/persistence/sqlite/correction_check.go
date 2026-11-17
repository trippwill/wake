// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"context"
	"database/sql"
	"errors"

	"go.trippwill.dev/wake/internal/application/calculation"
	"go.trippwill.dev/wake/internal/persistence/sqlite/sqlc"
	"go.trippwill.dev/wake/internal/reference/figi"
	"go.trippwill.dev/wake/source"
)

func (r correctionReader) InstrumentRevision(ctx context.Context, id string) (int64, error) {
	row, err := r.q.Instrument(ctx, id)
	return row.Revision, err
}

func (r correctionReader) CanonicalInstrument(ctx context.Context, id string) (string, error) {
	return canonicalID(ctx, r.q, "instruments", id)
}

func (r correctionReader) InstrumentKind(ctx context.Context, id string) (source.InstrumentKind, error) {
	row, err := r.q.Instrument(ctx, id)
	return source.InstrumentKind(row.Kind), err
}

func (r correctionReader) InstrumentBaseline(
	ctx context.Context,
	id string,
) (calculation.InstrumentSelection, source.InstrumentKind, error) {
	root, err := r.q.Instrument(ctx, id)
	if err != nil {
		return calculation.InstrumentSelection{}, "", err
	}

	base, err := r.q.InstrumentSelection(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		base = sqlc.InstrumentSelection{
			Name: root.Name, Ticker: root.Ticker, Figi: root.ReferenceFigi,
			Evidence: root.EvidenceID, Status: root.ReferenceStatus, UnderlierID: root.UnderlierID,
		}
	} else if err != nil {
		return calculation.InstrumentSelection{}, "", err
	}

	return instrumentSelection(base), source.InstrumentKind(root.Kind), nil
}

func instrumentSelection(base sqlc.InstrumentSelection) calculation.InstrumentSelection {
	return calculation.InstrumentSelection{
		Name:      base.Name,
		Ticker:    base.Ticker,
		FIGI:      base.Figi,
		Evidence:  base.Evidence,
		Status:    calculation.ReferenceStatus(base.Status),
		Underlier: base.UnderlierID.String,
	}
}

func (r correctionReader) InstrumentReference(
	ctx context.Context,
	id, value string,
) (figi.Record, string, error) {
	return referenceCandidate(ctx, r.q, id, value)
}

func (r correctionReader) CompetingInstrumentKinds(
	ctx context.Context,
	id, figi string,
) ([]source.InstrumentKind, error) {
	rows, err := r.q.CompetingFIGI(ctx, sqlc.CompetingFIGIParams{CurrentID: id, Figi: figi})
	if err != nil {
		return nil, err
	}

	kinds := make([]source.InstrumentKind, 0, len(rows))
	for _, row := range rows {
		kinds = append(kinds, source.InstrumentKind(row.Kind))
	}

	return kinds, nil
}

func (w *correctionWriter) SaveInstrument(
	ctx context.Context,
	id string,
	result calculation.InstrumentCalculation,
) error {
	w.changed = true
	if err := persistInstrumentCalculation(ctx, w.q, id, result); err != nil {
		return err
	}

	if err := w.q.AdvanceInstrumentRevision(ctx, id); err != nil {
		return err
	}

	return combineIdentity(ctx, w.q, id)
}
