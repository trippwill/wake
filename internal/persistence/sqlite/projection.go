// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0
package persistence

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/persistence/sqlite/sqlc"
	"go.trippwill.dev/wake/source"
)

func correctionStates(ctx context.Context, q *sqlc.Queries) (map[string]application.ActivityState, error) {
	return correctionStatesFor(ctx, q, nil)
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

func correctionStatesFor(
	ctx context.Context,
	q *sqlc.Queries,
	entities map[string]bool,
) (map[string]application.ActivityState, error) {
	rows, edges, err := correctionInputs(ctx, q)
	if err != nil {
		return nil, err
	}

	states, statuses := application.CorrectionStates(edges)
	for i, r := range rows {
		applies := entities == nil
		for _, id := range append(append([]string{}, edges[i].Targets...), edges[i].Replacements...) {
			applies = applies || entities[id]
		}

		if !applies || r.Status == string(statuses[i]) {
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

func persistRootEnrichment(
	ctx context.Context,
	q *sqlc.Queries,
	id string,
	result application.RootCalculation,
	underlier application.UnderlierLink,
) error {
	root, err := q.Instrument(ctx, id)
	if err != nil {
		return err
	}

	plan := application.PlanRootEnrichment(identityRoot(root), result, underlier)
	if plan.Conflict != application.KeepSourceConflict {
		if err = q.IdentityMetadata(ctx, sqlc.IdentityMetadataParams{
			ID: root.ID, Allocation: root.ReferenceAllocation, Reason: string(plan.Conflict.Reason()), Family: root.Kind,
		}); err != nil {
			return err
		}

		if err = applyRootConflict(ctx, q, id, plan.Conflict); err != nil {
			return err
		}
	}

	if plan.Reported != nil {
		data, err := json.Marshal(instrumentWire(*plan.Reported))
		if err != nil {
			return err
		}

		if !bytes.Equal(data, root.Reported) {
			if err = enrichAutomaticInstrument(ctx, q, sqlc.EnrichInstrumentParams{
				ID: root.ID, Reported: data, Kind: string(plan.Family),
				Name: plan.Reported.Name(), Ticker: plan.Reported.Ticker(),
			}); err != nil {
				return err
			}
		}
	}

	if plan.Underlier != nil {
		return setAutomaticUnderlier(ctx, q, sqlc.SetUnderlierParams{
			ID: root.ID, UnderlierID: sql.NullString{String: *plan.Underlier, Valid: *plan.Underlier != ""},
		})
	}

	return nil
}
