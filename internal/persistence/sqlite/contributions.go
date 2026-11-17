// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"context"
	"database/sql"

	"go.trippwill.dev/wake/internal/application/calculation"
	"go.trippwill.dev/wake/internal/persistence/sqlite/recordcodec"
	"go.trippwill.dev/wake/internal/persistence/sqlite/sqlc"
	"go.trippwill.dev/wake/source"
)

// Admission can temporarily bind candidates while resolving identities. Only
// useful subject contributions survive the processing transaction.
func retainActivityContributions(ctx context.Context, q *sqlc.Queries, id string) error {
	rows, err := q.CalculationInputs(ctx, sql.NullString{String: id, Valid: true})
	if err != nil {
		return err
	}

	prior := []source.ActivityObservation{}
	for _, row := range rows {
		data, e := recordcodec.WithLocation(row.Kind, row.Data, row.Location)
		if e != nil {
			return e
		}

		value, e := recordcodec.DecodeObservation(data)
		if e != nil {
			return e
		}

		outcome := calculation.ObservationContribution(prior, value)
		if outcome == "" {
			if e = q.DiscardActivitySupport(ctx, sqlc.DiscardActivitySupportParams{
				ClaimID: row.ClaimID, Ordinal: row.Ordinal,
			}); e != nil {
				return e
			}

			if e = q.DeleteObservation(ctx, sqlc.DeleteObservationParams{
				ArtifactID: row.ArtifactID, Version: row.Version, Location: row.Location,
			}); e != nil {
				return e
			}

			continue
		}

		prior = append(prior, value)

		if row.LinkOutcome != string(calculation.ObservationTermsConflict) &&
			calculation.ObservationOutcome(row.LinkOutcome).NeedsReview() {
			outcome = calculation.ObservationOutcome(row.LinkOutcome)
		}

		if e = q.SetObservationOutcome(ctx, sqlc.SetObservationOutcomeParams{
			ArtifactID: row.ArtifactID, Version: row.Version, Location: row.Location, Outcome: string(outcome),
		}); e != nil {
			return e
		}
	}

	return nil
}

func retainInstrumentContributions(ctx context.Context, q *sqlc.Queries, id string) error {
	inputs, err := q.InstrumentContributionInputs(ctx, id)
	if err != nil {
		return err
	}

	row, err := q.Instrument(ctx, id)
	if err != nil {
		return err
	}

	instrument, err := instrumentValue(row)
	if err != nil {
		return err
	}

	references := map[source.Reference]struct{}{}
	claims := make([]calculation.RootClaim, 0, len(inputs))
	for _, input := range inputs {
		reported, e := reportedRootClaim(input.Kind, input.Data, input.Location, input.Role)
		if e != nil {
			return e
		}

		claims = append(
			claims,
			calculation.RootClaim{ID: input.ClaimID, Ordinal: input.Ordinal, Role: input.Role, Reported: reported},
		)

		ref := reported.Option().Fields().Underlier.Reference()
		if !ref.IsZero() {
			references[ref] = struct{}{}
		}
	}

	bindings, err := newRootPhase(q).sourceBindings(ctx, row, instrument.Reported(), references)
	if err != nil {
		return err
	}

	prior := []source.Instrument{}
	for _, row := range claims {
		value := row.Reported

		outcome, e := calculation.InstrumentContribution(prior, value, bindings)
		if e != nil {
			return e
		}

		if outcome == "" {
			if e = q.DeleteInstrumentClaim(ctx, sqlc.DeleteInstrumentClaimParams{
				InstrumentID: id, ClaimID: row.ID, Ordinal: row.Ordinal, Role: row.Role,
			}); e != nil {
				return e
			}

			continue
		}

		prior = append(prior, value)

		if e = q.SetInstrumentClaimOutcome(ctx, sqlc.SetInstrumentClaimOutcomeParams{
			InstrumentID: id, ClaimID: row.ID, Ordinal: row.Ordinal, Role: row.Role, Outcome: string(outcome),
		}); e != nil {
			return e
		}
	}

	return nil
}
