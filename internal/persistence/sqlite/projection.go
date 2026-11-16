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
)

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
			ID:         root.ID,
			Allocation: root.ReferenceAllocation,
			Reason:     string(plan.Conflict.Reason()),
			Family:     root.Kind,
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
