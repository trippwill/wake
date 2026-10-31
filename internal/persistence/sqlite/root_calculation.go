// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0
package persistence

import (
	"context"

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
			id, _, e := acceptInstrument(ctx, q, "retained", root.ID+"/underlier", underlier, "")
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
