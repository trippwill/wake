// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/persistence/sqlite/sqlc"
	"go.trippwill.dev/wake/source"
)

func createActivity(
	ctx context.Context,
	q *sqlc.Queries,
	account, artifact, location string,
	terms application.ActivityTerms,
	selection application.ActivitySelection,
) (string, error) {
	id := rand.Text()
	instrument, listing := "", ""

	var err error

	currency := ""
	if terms.Kind() == application.TradeActivity {
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
	case application.SecurityTransferActivity, application.CashActivity:
		err = q.PutMovementActivity(
			ctx,
			sqlc.PutMovementActivityParams{
				Kind:         string(terms.Kind()),
				ID:           id,
				AccountID:    account,
				InstrumentID: sql.NullString{String: instrument, Valid: instrument != ""},
				Accepted:     data,
				Rule:         application.RepeatRule,
			},
		)
	case application.TradeActivity:
		err = q.PutExecution(
			ctx,
			sqlc.PutExecutionParams{
				ID:           id,
				AccountID:    account,
				InstrumentID: sql.NullString{String: instrument, Valid: instrument != ""},
				ListingID:    sql.NullString{String: listing, Valid: listing != ""},
				Accepted:     data,
				Rule:         application.RepeatRule,
			},
		)
	case application.LifecycleActivity:
		err = q.PutLifecycleActivity(
			ctx,
			sqlc.PutLifecycleActivityParams{
				ID:           id,
				AccountID:    account,
				InstrumentID: sql.NullString{String: instrument, Valid: instrument != ""},
				Accepted:     data,
				Rule:         application.RepeatRule,
			},
		)
	case application.ConversionActivity:
		err = q.PutConversionActivity(
			ctx,
			sqlc.PutConversionActivityParams{
				ID:        id,
				AccountID: account,
				Accepted:  data,
				Rule:      application.RepeatRule,
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

type activitySelectionData struct {
	Fields                    []activityFieldData
	Sources                   map[string][]string
	Conflicts, OperatorFields []string
}

func encodeActivityFields(fields []source.ActivityField) ([]byte, error) {
	out := make([]activityFieldData, 0, len(fields))
	for _, f := range fields {
		out = append(out, activityFieldWire(f))
	}

	return json.Marshal(out)
}

func decodeActivityFields(data []byte) ([]source.ActivityField, error) {
	var values []activityFieldData
	if json.Unmarshal(data, &values) != nil {
		return nil, ErrCorrupt
	}

	out := make([]source.ActivityField, 0, len(values))
	for _, v := range values {
		f, e := v.field()
		if e != nil {
			return nil, ErrCorrupt
		}

		out = append(out, f)
	}

	return out, nil
}

func persistActivitySelection(
	ctx context.Context,
	q *sqlc.Queries,
	id string,
	value application.ActivitySelection,
) error {
	base, err := encodeActivityFields(value.Baseline)
	if err != nil {
		return err
	}

	d := activitySelectionData{
		Sources:        value.Sources,
		Conflicts:      value.Conflicts,
		OperatorFields: value.OperatorFields,
	}
	for _, f := range value.Fields {
		d.Fields = append(d.Fields, activityFieldWire(f))
	}

	data, err := json.Marshal(d)
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
) (application.ActivitySelection, error) {
	row, err := q.ActivitySelection(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return application.ActivitySelection{}, nil
	}

	if err != nil {
		return application.ActivitySelection{}, err
	}

	var d activitySelectionData
	if json.Unmarshal(row.Selected, &d) != nil {
		return application.ActivitySelection{}, ErrCorrupt
	}

	out := application.ActivitySelection{
		Sources:        d.Sources,
		Conflicts:      d.Conflicts,
		OperatorFields: d.OperatorFields,
	}

	out.Baseline, err = decodeActivityFields(row.Baseline)
	if err != nil {
		return out, err
	}

	for _, v := range d.Fields {
		f, e := v.field()
		if e != nil {
			return out, ErrCorrupt
		}

		out.Fields = append(out.Fields, f)
	}

	return out, nil
}
