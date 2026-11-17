// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"

	"go.trippwill.dev/wake/internal/application/calculation"
	"go.trippwill.dev/wake/internal/persistence/sqlite/recordcodec"
	"go.trippwill.dev/wake/internal/persistence/sqlite/sqlc"
)

func createActivity(
	ctx context.Context,
	q *sqlc.Queries,
	account, artifact, location string,
	terms calculation.ActivityTerms,
	selection calculation.ActivitySelection,
) (string, error) {
	id := rand.Text()
	instrument, listing := "", ""

	var err error

	currency := ""
	if terms.Kind() == calculation.TradeActivity {
		currency = terms.Trade().Fields().Price.Currency().String()
	}

	if terms.Instrument().Kind() != "" {
		instrument, listing, err = acceptInstrument(ctx, q, artifact, location, terms.Instrument(), currency)
		if err != nil {
			return "", err
		}
	}

	data, err := encodeActivityTerms(terms)
	if err != nil {
		return "", err
	}

	switch terms.Kind() {
	case calculation.SecurityTransferActivity, calculation.CashActivity:
		err = q.PutMovementActivity(
			ctx,
			sqlc.PutMovementActivityParams{
				Kind:         string(terms.Kind()),
				ID:           id,
				AccountID:    account,
				InstrumentID: sql.NullString{String: instrument, Valid: instrument != ""},
				Accepted:     data,
				Rule:         calculation.RepeatRule,
			},
		)
	case calculation.TradeActivity:
		err = q.PutExecution(
			ctx,
			sqlc.PutExecutionParams{
				ID:           id,
				AccountID:    account,
				InstrumentID: sql.NullString{String: instrument, Valid: instrument != ""},
				ListingID:    sql.NullString{String: listing, Valid: listing != ""},
				Accepted:     data,
				Rule:         calculation.RepeatRule,
			},
		)
	case calculation.LifecycleActivity:
		err = q.PutLifecycleActivity(
			ctx,
			sqlc.PutLifecycleActivityParams{
				ID:           id,
				AccountID:    account,
				InstrumentID: sql.NullString{String: instrument, Valid: instrument != ""},
				Accepted:     data,
				Rule:         calculation.RepeatRule,
			},
		)
	case calculation.ConversionActivity:
		err = q.PutConversionActivity(
			ctx,
			sqlc.PutConversionActivityParams{
				ID:        id,
				AccountID: account,
				Accepted:  data,
				Rule:      calculation.RepeatRule,
			},
		)
	}

	if err != nil {
		return "", err
	}

	if err = q.PutActivityBase(
		ctx,
		sqlc.PutActivityBaseParams{
			ActivityID:   id,
			Data:         data,
			InstrumentID: sql.NullString{String: instrument, Valid: instrument != ""},
			ListingID:    sql.NullString{String: listing, Valid: listing != ""},
		},
	); err != nil {
		return "", err
	}

	if err = persistActivitySelection(ctx, q, id, selection); err != nil {
		return "", err
	}

	return id, nil
}

func persistActivitySelection(
	ctx context.Context,
	q *sqlc.Queries,
	id string,
	value calculation.ActivitySelection,
) error {
	base, data, err := recordcodec.EncodeActivitySelection(recordcodec.ActivitySelection{
		Baseline:       value.Baseline,
		Fields:         value.Fields,
		Sources:        value.Sources,
		Conflicts:      value.Conflicts,
		OperatorFields: value.OperatorFields,
	})
	if err != nil {
		return err
	}

	return q.SaveActivitySelection(
		ctx,
		sqlc.SaveActivitySelectionParams{ActivityID: id, Baseline: base, Selected: data},
	)
}

func loadActivitySelection(
	ctx context.Context,
	q *sqlc.Queries,
	id string,
) (calculation.ActivitySelection, error) {
	row, err := q.ActivitySelection(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return calculation.ActivitySelection{}, nil
	}

	if err != nil {
		return calculation.ActivitySelection{}, err
	}

	value, err := recordcodec.DecodeActivitySelection(row.Baseline, row.Selected)

	return calculation.ActivitySelection{
		Baseline:       value.Baseline,
		Fields:         value.Fields,
		Sources:        value.Sources,
		Conflicts:      value.Conflicts,
		OperatorFields: value.OperatorFields,
	}, err
}
