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

	underliers := map[string]bool{}
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
			underliers[root.ID] = true
		}
	}
	// Reconcile the complete retained parent claim set before choosing a batch's
	// target. Reference-less enrichment follows a qualified claim in either order.
	for parent := range underliers {
		result, e := calculateRoot(ctx, q, parent)
		if e != nil {
			return e
		}

		selected := ""
		if result.Underlier.Kind() != "" {
			selected, e = acceptAutomaticUnderlier(ctx, q, parent, result.Underlier)
			if e != nil {
				return e
			}
		}

		// Add compatible retained source inputs to the selected target without
		// removing their original bindings. Earlier anonymous identifiers remain
		// available when a later report selects an already-existing qualified root.
		inputs, e := q.RootCalculationInputs(ctx, parent)
		if e != nil {
			return e
		}

		for _, claim := range inputs {
			if claim.Role != "activity" {
				continue
			}

			reported, e := reportedRootClaim(claim)
			if e != nil {
				return e
			}

			underlier := reported.Option().Fields().Underlier
			if underlier.Kind() == "" {
				continue
			}

			id := selected
			if selected == "" || !application.CompatibleInstrument(result.Underlier, underlier) {
				id, _, e = acceptInstrument(ctx, q, "retained", parent+"/underlier", underlier, "")
				if e != nil {
					return e
				}
			}

			if e = q.PutInstrumentClaim(
				ctx,
				sqlc.PutInstrumentClaimParams{
					InstrumentID: id,
					ClaimID:      claim.ClaimID,
					Ordinal:      claim.Ordinal,
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
				candidate, e := retainedRootCalculation(ctx, q, qualified)
				if e != nil {
					return "", e
				}

				_, conflict, e := application.ReconcileInstrument(
					known,
					[]source.Instrument{reported, candidate.Reported},
				)
				if e != nil {
					return "", e
				}

				if conflict || candidate.Conflict {
					return root.ID, nil
				}

				return qualified.ID, nil
			}

			if !errors.Is(e, sql.ErrNoRows) {
				return "", e
			}
			// Never overwrite a different source key qualified earlier in this
			// transaction, before its complete claim set is projected.
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
		result, e := calculateRoot(ctx, q, id)
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

func calculateRoot(ctx context.Context, q *sqlc.Queries, id string) (application.RootCalculation, error) {
	root, e := q.Instrument(ctx, id)
	if e != nil {
		return application.RootCalculation{}, e
	}

	result, e := retainedRootCalculation(ctx, q, root)
	if e != nil {
		return application.RootCalculation{}, e
	}

	// A qualified identity held by another root is evidence for review, never
	// permission to discard identifiers retained on the established binding.
	for _, reported := range []source.Instrument{result.Reported, result.Underlier} {
		if reported.Reference().IsZero() {
			continue
		}

		candidate, e := q.InstrumentBySource(ctx, referenceKey(reported.Reference()))
		if errors.Is(e, sql.ErrNoRows) {
			continue
		}

		if e != nil {
			return application.RootCalculation{}, e
		}

		if candidate.ID == id {
			continue
		}

		value, e := retainedRootCalculation(ctx, q, candidate)
		if e != nil {
			return application.RootCalculation{}, e
		}

		result.Conflict = result.Conflict || value.Conflict ||
			!application.CompatibleInstrument(reported, value.Reported)
	}

	return result, nil
}

func reportedRootClaim(row sqlc.RootCalculationInputsRow) (source.Instrument, error) {
	data, err := claimData(row.Kind, row.Data, row.Location)
	if err != nil {
		return source.Instrument{}, err
	}

	terms, err := decodeActivityTerms(row.Kind, data)
	if err != nil {
		return source.Instrument{}, err
	}

	claim := terms.Instrument()
	if row.Role == "underlier" {
		claim = claim.Option().Fields().Underlier
	}

	return claim, nil
}

// Read all source inputs without consulting other roots or projected selections.
func retainedRootCalculation(
	ctx context.Context,
	q *sqlc.Queries,
	root sqlc.Instrument,
) (application.RootCalculation, error) {
	baseline, e := instrumentValue(root)
	if e != nil {
		return application.RootCalculation{}, e
	}

	rows, e := q.RootCalculationInputs(ctx, root.ID)
	if e != nil {
		return application.RootCalculation{}, e
	}

	claims := make([]source.Instrument, 0, len(rows))
	for _, row := range rows {
		claim, e := reportedRootClaim(row)
		if e != nil {
			return application.RootCalculation{}, e
		}

		claims = append(claims, claim)
	}

	return application.CalculateRoot(baseline.Reported(), claims)
}
