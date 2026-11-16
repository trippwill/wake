// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/persistence/sqlite/sqlc"
	"go.trippwill.dev/wake/source"
)

type retainedProcessing struct {
	tx          *sql.Tx
	q           *sqlc.Queries
	id, version string
	activities  map[string]sqlc.Execution
}

func (s *Store) UpdateRetainedClaims(
	ctx context.Context,
	id, version string,
	fn func(application.RetainedProcessingWriter) error,
) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	q := s.queries.WithTx(tx)

	complete, err := q.ProcessingComplete(ctx, sqlc.ProcessingCompleteParams{ArtifactID: id, Version: version})
	if err != nil {
		return err
	}

	if !complete {
		if err := fn(
			retainedProcessing{tx: tx, q: q, id: id, version: version, activities: map[string]sqlc.Execution{}},
		); err != nil {
			return err
		}

		if err := s.commit(ctx, tx); err != nil {
			return err
		}
	}

	s.clearProcessingFailure(id)

	return nil
}

func (p retainedProcessing) RetainedSource(ctx context.Context) (string, string, application.Account, error) {
	retained, err := retainedArtifact(ctx, p.q, p.id)
	if err != nil {
		return "", "", application.Account{}, err
	}

	var account application.Account

	row, err := p.q.AccountByConnection(ctx, retained.Connection)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	} else if err == nil {
		account, err = accountValue(row)
	}

	return retained.Connection, retained.Provider, account, err
}

func (p retainedProcessing) RetainedClaims(ctx context.Context) ([]application.RetainedClaim, error) {
	rows, err := records(ctx, p.q, p.id, p.version)
	if err != nil {
		return nil, err
	}

	out := make([]application.RetainedClaim, 0, len(rows))
	for _, row := range rows {
		claim := application.RetainedClaim{
			ID: row.ClaimID, Location: row.Location, Kind: row.Kind, Ordinal: row.Ordinal,
			Observation: row.Observation, Correction: row.Correction, Positions: row.PositionSet,
		}
		if row.Kind == "position-instrument" {
			claim.PositionAccount, err = row.PositionInstrument.Account.reference()
			if err != nil {
				return nil, err
			}

			claim.PositionInstrument, err = row.PositionInstrument.Instrument.instrument()
			if err != nil {
				return nil, err
			}
		}

		out = append(out, claim)
	}

	return out, nil
}

func (p retainedProcessing) RecordClaim(ctx context.Context, claim application.RetainedClaim) (bool, error) {
	prior, err := p.q.ClaimProcessing(
		ctx,
		sqlc.ClaimProcessingParams{ClaimID: claim.ID, Ordinal: claim.Ordinal},
	)
	if err == nil {
		return false, p.q.CopyClaimCorrection(ctx, sqlc.CopyClaimCorrectionParams{
			ArtifactID: p.id, Version: p.version, Location: claim.Location,
			PreviousArtifact: prior.ArtifactID, PreviousVersion: prior.Version, PreviousLocation: prior.Location,
		})
	}

	if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}

	return true, p.q.PutClaimProcessing(ctx, sqlc.PutClaimProcessingParams{
		ClaimID: claim.ID, Ordinal: claim.Ordinal, ArtifactID: p.id, Version: p.version, Location: claim.Location,
	})
}

func (p retainedProcessing) SavePositionSet(
	ctx context.Context,
	account string,
	claim application.RetainedClaim,
) error {
	f := claim.Positions.Fields()
	date, ref := "", ""
	if !f.Date.IsZero() {
		date = f.Date.String()
	}

	if !f.Reference.IsZero() {
		ref = referenceKey(f.Reference)
	}

	supersedes := make([]string, 0, len(f.Supersedes))
	for _, ref := range f.Supersedes {
		supersedes = append(supersedes, referenceKey(ref))
	}

	revisions, err := json.Marshal(supersedes)
	if err != nil {
		return err
	}

	return p.q.PutPositionSet(ctx, sqlc.PutPositionSetParams{
		ClaimID:         claim.ID,
		AccountID:       account,
		EffectiveDate:   date,
		SourceReference: ref,
		Supersedes:      string(revisions),
	})
}

func (p retainedProcessing) BindPositionInstrument(
	ctx context.Context,
	account string,
	claim application.RetainedClaim,
) (string, error) {
	root, _, err := acceptInstrument(ctx, p.q, p.id, claim.Location, claim.PositionInstrument, "")
	if err != nil {
		return "", err
	}

	if err := p.q.PutPositionBinding(ctx, sqlc.PutPositionBindingParams{
		ClaimID: claim.ID, Ordinal: claim.Ordinal, AccountID: account, InstrumentID: root,
	}); err != nil {
		return "", err
	}

	return root, p.q.PutInstrumentClaim(ctx, sqlc.PutInstrumentClaimParams{
		InstrumentID: root, ClaimID: claim.ID, Ordinal: claim.Ordinal, Role: "subject",
	})
}

func (p retainedProcessing) SaveSourceCorrection(
	ctx context.Context,
	account string,
	correction source.Correction,
) error {
	f := correction.Fields()

	return p.q.PutSourceCorrection(ctx, sqlc.PutSourceCorrectionParams{
		ArtifactID: p.id, Version: p.version, Location: f.Location, AccountID: account,
		Kind: string(f.Kind), Target: referenceKey(f.Target), Replacement: referenceKey(f.Replacement),
	})
}

func (p retainedProcessing) SaveActivityObservation(
	ctx context.Context,
	account string,
	claim application.RetainedClaim,
	outcome string,
	refs []source.Reference,
) error {
	for _, reference := range refs {
		if err := p.q.PutActivityReference(ctx, sqlc.PutActivityReferenceParams{
			ClaimID: claim.ID, Ordinal: claim.Ordinal, AccountID: account, Reference: referenceKey(reference),
		}); err != nil {
			return err
		}
	}

	return p.q.PutObservation(ctx, sqlc.PutObservationParams{
		ArtifactID: p.id,
		Version:    p.version,
		Location:   claim.Location,
		Outcome:    outcome,
		Rule:       application.RepeatRule,
	})
}

func (p retainedProcessing) ProcessingReceipt(ctx context.Context) (bool, error) {
	var reinterpreting bool
	if err := p.tx.QueryRowContext(ctx, positionsStatement("Reinterpreting"), p.version, p.id).
		Scan(&reinterpreting); err != nil {
		return false, err
	}

	return reinterpreting, p.q.SaveProcessingReceipt(
		ctx,
		sqlc.SaveProcessingReceiptParams{ArtifactID: p.id, Version: p.version},
	)
}

func (p retainedProcessing) BindPositionUnderliers(ctx context.Context, roots []string) error {
	return bindUnderlierInstrumentClaims(ctx, p.q, roots)
}

func (p retainedProcessing) RetirePositionInterpretations(ctx context.Context) error {
	_, err := p.tx.ExecContext(ctx, positionsStatement("InterpretationChanged"), p.id)
	return err
}

func (p retainedProcessing) SeedActivityBases(ctx context.Context) error {
	return p.q.SeedArtifactBases(ctx, p.id)
}

func (p retainedProcessing) SourceCorrectionStates(
	ctx context.Context,
) (map[string]application.ActivityState, error) {
	return correctionStates(ctx, p.q)
}

func (p retainedProcessing) ClaimActivityIDs(ctx context.Context) ([]string, error) {
	rows, err := p.q.NewClaimActivityIDs(ctx, p.id)
	if err != nil {
		return nil, err
	}

	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.String)
	}

	return ids, nil
}

func (p retainedProcessing) ActivityState(ctx context.Context, id string) (application.ActivityState, error) {
	row, err := p.activityRow(ctx, id)
	if err != nil {
		return application.ActivityState{}, err
	}

	return activityState(row)
}

func (p retainedProcessing) BindActivityInstrument(ctx context.Context, id string) error {
	row, err := p.activityRow(ctx, id)
	if err != nil {
		return err
	}

	row, err = bindCashInstrument(ctx, p.q, row)
	if err == nil {
		p.activities[id] = row
	}

	return err
}

func (p retainedProcessing) ProjectArtifactRoots(ctx context.Context) error {
	return projectArtifactRoots(ctx, p.q, p.id)
}

func (p retainedProcessing) ProjectRetainedActivity(
	ctx context.Context,
	id string,
	state application.ActivityState,
) error {
	if err := retainActivityContributions(ctx, p.q, id); err != nil {
		return err
	}

	row, err := p.activityRow(ctx, id)
	if err != nil {
		return err
	}

	return projectActivity(ctx, p.q, row, state, application.RetainedClaimsRule)
}

func (p retainedProcessing) activityRow(ctx context.Context, id string) (sqlc.Execution, error) {
	if row, ok := p.activities[id]; ok {
		return row, nil
	}

	row, err := p.q.Execution(ctx, id)
	if err == nil {
		p.activities[id] = row
	}

	return row, err
}
