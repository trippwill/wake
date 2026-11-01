// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0
package persistence

import (
	"context"
	"database/sql"
	"errors"

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/persistence/sqlite/sqlc"
	"go.trippwill.dev/wake/source"
)

// Bind each distinct source occurrence to its original root, including nested
// underliers. Activity/operator link choices cannot move these source inputs.
func bindArtifactInstrumentClaims(ctx context.Context, q *sqlc.Queries, artifact string) error {
	rows, err := q.ArtifactInstrumentClaims(ctx, artifact)
	if err != nil {
		return err
	}

	for _, row := range rows {
		if !row.InstrumentID.Valid {
			continue
		}

		data, e := claimData(row.Kind, row.Data, row.Location)
		if e != nil {
			return e
		}

		terms, e := decodeActivityTerms(row.Kind, data)
		if e != nil {
			return e
		}

		reported := terms.Instrument()

		root, e := q.Instrument(ctx, row.InstrumentID.String)
		if e != nil {
			return e
		}

		baseline, e := instrumentValue(root)
		if e != nil {
			return e
		}

		if !baseline.Reported().Reference().IsZero() && !reported.Reference().IsZero() &&
			baseline.Reported().Reference() != reported.Reference() {
			continue
		}

		if e = q.PutInstrumentClaim(
			ctx,
			sqlc.PutInstrumentClaimParams{
				InstrumentID: root.ID,
				ClaimID:      row.ClaimID,
				Ordinal:      row.Ordinal,
				Role:         "activity",
			},
		); e != nil {
			return e
		}

		if underlier := reported.Option().Fields().Underlier; underlier.Kind() != "" {
			id, e := acceptAutomaticUnderlier(ctx, q, root.ID, underlier)
			if e != nil {
				return e
			}

			if e = q.PutInstrumentClaim(
				ctx,
				sqlc.PutInstrumentClaimParams{
					InstrumentID: id,
					ClaimID:      row.ClaimID,
					Ordinal:      row.Ordinal,
					Role:         "underlier",
				},
			); e != nil {
				return e
			}
		}
	}

	return nil
}

// Follow the source-established link, never the operator-projected link. A
// compatible anonymous root can acquire its first qualified reference. Existing
// qualified roots remain distinct; only the parent's automatic selection moves.
func acceptAutomaticUnderlier(
	ctx context.Context,
	q *sqlc.Queries,
	parent string,
	reported source.Instrument,
) (string, error) {
	if err := q.EnsureInstrumentSelection(ctx, parent); err != nil {
		return "", err
	}

	selection, err := q.InstrumentSelection(ctx, parent)
	if err != nil {
		return "", err
	}

	if selection.UnderlierID.Valid {
		root, e := q.Instrument(ctx, selection.UnderlierID.String)
		if e != nil {
			return "", e
		}

		baseline, e := instrumentValue(root)
		if e != nil {
			return "", e
		}

		known := baseline.Reported()
		if application.CompatibleInstrument(known, reported) {
			if reported.Reference().IsZero() || !known.Reference().IsZero() {
				return root.ID, nil
			}

			key := referenceKey(reported.Reference())

			qualified, e := q.InstrumentBySource(ctx, key)
			if e == nil {
				return qualified.ID, nil
			}

			if !errors.Is(e, sql.ErrNoRows) {
				return "", e
			}
			// A previous claim in this transaction may already have supplied a
			// different reference, before the complete claim set is projected.
			if root.SourceKey == anonymousInstrumentKey("retained", parent+"/underlier") || root.SourceKey == key {
				if e = q.QualifyInstrument(ctx, sqlc.QualifyInstrumentParams{ID: root.ID, SourceKey: key}); e != nil {
					return "", e
				}

				return root.ID, nil
			}
		}
	}

	id, _, err := acceptInstrument(ctx, q, "retained", parent+"/underlier", reported, "")

	return id, err
}

func projectArtifactRoots(ctx context.Context, q *sqlc.Queries, artifact string) error {
	if err := bindArtifactInstrumentClaims(ctx, q, artifact); err != nil {
		return err
	}

	ids, err := q.ArtifactRootIDs(ctx, artifact)
	if err != nil {
		return err
	}

	return projectRoots(ctx, q, ids)
}

func projectRoots(ctx context.Context, q *sqlc.Queries, ids []string) error {
	results := make(map[string]application.RootCalculation, len(ids))
	// Every root, including a shared underlier without its own activity, is
	// reconciled exactly once against its pre-write baseline.
	for _, id := range ids {
		root, e := q.Instrument(ctx, id)
		if e != nil {
			return e
		}

		baseline, e := instrumentValue(root)
		if e != nil {
			return e
		}

		rows, e := q.RootCalculationInputs(ctx, id)
		if e != nil {
			return e
		}

		claims := make([]source.Instrument, 0, len(rows))
		for _, row := range rows {
			data, e := claimData(row.Kind, row.Data, row.Location)
			if e != nil {
				return e
			}

			terms, e := decodeActivityTerms(row.Kind, data)
			if e != nil {
				return e
			}

			claim := terms.Instrument()
			if row.Role == "underlier" {
				claim = claim.Option().Fields().Underlier
			}

			claims = append(claims, claim)
		}

		result, e := application.CalculateRoot(baseline.Reported(), claims)
		if e != nil {
			return e
		}

		results[id] = result
	}

	for _, id := range ids {
		if err := persistRootEnrichment(ctx, q, id, results[id]); err != nil {
			return err
		}
	}

	return nil
}
