// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0
package persistence

import (
	"bytes"
	"context"
	"database/sql"

	"go.trippwill.dev/wake/internal/application/calculation"
	"go.trippwill.dev/wake/internal/persistence/sqlite/recordcodec"
	"go.trippwill.dev/wake/internal/persistence/sqlite/sqlc"
	"go.trippwill.dev/wake/internal/reference/figi"
	"go.trippwill.dev/wake/source"
)

func persistRootEnrichment(
	ctx context.Context,
	q *sqlc.Queries,
	id string,
	result calculation.RootCalculation,
	underlier calculation.UnderlierLink,
) error {
	root, err := q.Instrument(ctx, id)
	if err != nil {
		return err
	}

	plan := calculation.PlanRootEnrichment(source.InstrumentKind(root.Kind), root.ReferenceFigi != "",
		root.ReferenceReason == string(figi.SourceConflict), result, underlier)
	if plan.Conflict != calculation.KeepSourceConflict {
		reason := ""
		if plan.Conflict == calculation.RecordSourceConflict {
			reason = string(figi.SourceConflict)
		}

		if err = q.IdentityMetadata(ctx, sqlc.IdentityMetadataParams{
			ID:         root.ID,
			Allocation: root.ReferenceAllocation,
			Reason:     reason,
			Family:     root.Kind,
		}); err != nil {
			return err
		}

		if err = applyRootConflict(ctx, q, id, plan.Conflict); err != nil {
			return err
		}
	}

	if plan.Reported != nil {
		data, err := recordcodec.EncodeInstrument(*plan.Reported)
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
