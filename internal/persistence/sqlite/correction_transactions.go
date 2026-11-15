// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/persistence/sqlite/sqlc"
	"go.trippwill.dev/wake/source"
)

type (
	activityCorrectionReader struct{ q *sqlc.Queries }
	activityCorrectionWriter struct {
		activityCorrectionReader
		changed bool
	}
)

func (s *Store) ViewActivityCorrection(
	ctx context.Context,
	fn func(application.ActivityCorrectionReader) error,
) error {
	if s.HistoryUpdating() {
		return application.ErrHistoryUpdating
	}

	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}

	defer func() { _ = tx.Rollback() }()

	return fn(activityCorrectionReader{s.queries.WithTx(tx)})
}

func (s *Store) UpdateActivityCorrection(
	ctx context.Context,
	fn func(application.ActivityCorrectionWriter) error,
) error {
	if s.HistoryUpdating() {
		return application.ErrHistoryUpdating
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}

	defer func() { _ = tx.Rollback() }()

	writer := &activityCorrectionWriter{
		activityCorrectionReader: activityCorrectionReader{s.queries.WithTx(tx)},
	}
	if err = fn(writer); err != nil {
		return err
	}

	if !writer.changed {
		return nil
	}

	return s.commit(ctx, tx)
}

func (r activityCorrectionReader) ActivityRevision(ctx context.Context, id string) (int64, error) {
	row, err := r.q.Execution(ctx, id)
	return row.Revision, err
}

func (r activityCorrectionReader) ActivityDecisions(
	ctx context.Context,
	id string,
) ([]application.DecisionRecord, error) {
	rows, err := r.q.Decisions(ctx, sqlc.DecisionsParams{TargetKind: "activity", TargetID: id})
	if err != nil {
		return nil, err
	}

	out := make([]application.DecisionRecord, 0, len(rows))
	for _, row := range rows {
		d := row.OperatorDecision
		out = append(
			out,
			application.DecisionRecord{
				ID:         d.ID,
				Scope:      d.Scope,
				Text:       d.Text,
				Currency:   d.Currency,
				Action:     d.Action,
				Actor:      d.Actor,
				At:         d.At,
				Rationale:  d.Rationale,
				Supersedes: d.Supersedes,
				Active:     !row.Superseded && d.Action != "release",
			},
		)
	}

	return out, nil
}

func (r activityCorrectionReader) ActivityObservation(
	ctx context.Context,
	id, value string,
) (source.ActivityObservation, error) {
	var key [3]string
	if json.Unmarshal([]byte(value), &key) != nil {
		return source.ActivityObservation{}, application.ErrInvalid
	}

	row, err := r.q.SelectedObservation(
		ctx,
		sqlc.SelectedObservationParams{
			ExecutionID: sql.NullString{String: id, Valid: true},
			ArtifactID:  key[0],
			Version:     key[1],
			Location:    key[2],
		},
	)
	if err != nil {
		return source.ActivityObservation{}, application.ErrInvalid
	}

	data, err := claimData(row.Kind, row.Data, row.Location)
	if err != nil {
		return source.ActivityObservation{}, err
	}

	return decodeObservation(data)
}

func (r activityCorrectionReader) ActivityInputs(
	ctx context.Context,
	id string,
	selections []application.FieldSelection,
) (application.ActivityCalculationInput, error) {
	row, err := r.q.Execution(ctx, id)
	if err != nil {
		return application.ActivityCalculationInput{}, err
	}

	states, err := readCorrectionStates(ctx, r.q)
	if err != nil {
		return application.ActivityCalculationInput{}, err
	}

	return loadActivityCalculation(ctx, r.q, row, states[id], selections)
}

func (w *activityCorrectionWriter) Submission(
	ctx context.Context,
	id string,
) (application.DecisionInput, string, bool, error) {
	row, err := w.q.DecisionSubmission(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return application.DecisionInput{}, "", false, nil
	}

	if err != nil {
		return application.DecisionInput{}, "", false, err
	}

	var input application.DecisionInput
	if json.Unmarshal(row.Request, &input) != nil {
		return input, "", false, ErrCorrupt
	}

	return input, row.Actor, true, nil
}

func (w *activityCorrectionWriter) InsertDecision(
	ctx context.Context,
	field, request application.DecisionInput,
	actor string,
	at time.Time,
	supersedes string,
) error {
	data, err := json.Marshal(request)
	if err != nil {
		return err
	}

	w.changed = true
	id := rand.Text()
	if err = w.q.PutDecision(
		ctx,
		sqlc.PutDecisionParams{
			ID:         id,
			TargetKind: "activity",
			TargetID:   field.TargetID,
			Scope:      field.Scope,
			Text:       field.Text,
			Currency:   field.Currency,
			Action:     field.Action,
			Actor:      actor,
			At:         at.UTC().Format(time.RFC3339Nano),
			Rationale:  field.Rationale,
			Supersedes: supersedes,
			Submission: field.Submission,
			Request:    data,
		},
	); err != nil {
		return err
	}

	return w.q.SupersedeDecisions(
		ctx,
		sqlc.SupersedeDecisionsParams{
			DecisionID: id,
			TargetKind: "activity",
			TargetID:   field.TargetID,
			Scope:      field.Scope,
		},
	)
}

func (w *activityCorrectionWriter) SaveActivity(
	ctx context.Context,
	id string,
	calculation application.ActivityCalculation,
) error {
	w.changed = true

	row, err := w.q.Execution(ctx, id)
	if err != nil {
		return err
	}

	if err = persistActivityCalculation(ctx, w.q, row, calculation); err != nil {
		return err
	}
	// Choosing the same value still changes edit authority.
	return w.q.AdvanceActivityRevision(ctx, id)
}
