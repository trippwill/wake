// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"context"
	"database/sql"
	"encoding/json"

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/application/calculation"
	"go.trippwill.dev/wake/internal/persistence/sqlite/sqlc"
	"go.trippwill.dev/wake/source"
)

func (p retainedProcessing) ActivitiesToMatch(ctx context.Context) ([]application.ActivityToMatch, error) {
	rows, err := p.q.ActivityToMatch(ctx, p.id)
	if err != nil {
		return nil, err
	}

	out := make([]application.ActivityToMatch, 0, len(rows))
	for _, row := range rows {
		data, err := claimData("activity", row.Data, row.Location)
		if err != nil {
			return nil, err
		}

		value, err := decodeObservation(data)
		if err != nil {
			return nil, err
		}

		out = append(out, application.ActivityToMatch{
			Artifact: row.ArtifactID,
			Version:  row.Version,
			Location: row.Location,
			Claim:    row.ClaimID,
			Ordinal:  row.Ordinal,
			Value:    value,
			Key: observationKey(
				sqlc.RecordValue{ArtifactID: row.ArtifactID, Version: row.Version, Location: row.Location},
			),
		})
	}

	return out, nil
}

func (p retainedProcessing) ActivityLink(
	ctx context.Context,
	item application.ActivityToMatch,
) (string, string, error) {
	row, err := p.q.ActivityObservation(
		ctx,
		sqlc.ActivityObservationParams{ArtifactID: item.Artifact, Version: item.Version, Location: item.Location},
	)

	return row.ExecutionID.String, row.Outcome, err
}

func (p retainedProcessing) CreateActivity(
	ctx context.Context,
	account string,
	item application.ActivityToMatch,
	terms calculation.ActivityTerms,
	selection calculation.ActivitySelection,
) (string, error) {
	return createActivity(ctx, p.q, account, item.Artifact, item.Location, terms, selection)
}

func (p retainedProcessing) ActivityBaseline(
	ctx context.Context,
	id string,
) (calculation.ActivityTerms, error) {
	row, err := p.q.Execution(ctx, id)
	if err != nil {
		return calculation.ActivityTerms{}, err
	}

	base, err := p.q.ActivityBase(ctx, id)
	if err != nil {
		return calculation.ActivityTerms{}, err
	}

	return decodeActivityTerms(row.Kind, base.Data)
}

func (p retainedProcessing) RetainedActivityClaims(
	ctx context.Context,
	id string,
) ([]calculation.ActivityClaim, error) {
	return retainedActivityClaims(ctx, p.q, id)
}

func (p retainedProcessing) SaveEventLink(
	ctx context.Context,
	account, target string,
	ref source.Reference,
	distinct bool,
) error {
	return p.q.PutEventLink(ctx, sqlc.PutEventLinkParams{
		AccountID: account, Reference: referenceKey(ref), ExecutionID: target, DistinctEvent: boolInt(distinct),
	})
}

func (p retainedProcessing) SaveActivityRelationship(
	ctx context.Context,
	id string,
	link source.Relationship,
) error {
	return putRelationships(ctx, p.q, id, []source.Relationship{link})
}

func (p retainedProcessing) RelinkActivity(
	ctx context.Context,
	item application.ActivityToMatch,
	target, outcome string,
) error {
	return p.q.RelinkActivityObservation(ctx, sqlc.RelinkActivityObservationParams{
		ExecutionID: sql.NullString{
			String: target,
			Valid:  target != "",
		},
		Outcome: outcome,
		ClaimID: item.Claim,
		Ordinal: item.Ordinal,
	})
}

func (p retainedProcessing) EventActivities(
	ctx context.Context,
	account string,
	ref source.Reference,
) ([]string, error) {
	return p.q.EventLinks(ctx, sqlc.EventLinksParams{AccountID: account, Reference: referenceKey(ref)})
}

func (p retainedProcessing) DistinctActivityReferences(
	ctx context.Context,
	id string,
) ([]source.Reference, error) {
	rows, err := p.q.ExecutionLinks(ctx, id)
	if err != nil {
		return nil, err
	}

	refs := make([]source.Reference, 0, len(rows))
	for _, row := range rows {
		var wire refData
		if json.Unmarshal([]byte(row), &wire) != nil {
			return nil, ErrCorrupt
		}

		ref, err := wire.reference()
		if err != nil {
			return nil, err
		}

		refs = append(refs, ref)
	}

	return refs, nil
}
