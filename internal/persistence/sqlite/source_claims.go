// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"context"
	"database/sql"

	"go.trippwill.dev/wake/internal/persistence/sqlite/recordcodec"
	"go.trippwill.dev/wake/internal/persistence/sqlite/sqlc"
)

func materializeRecords(rows []sqlc.RecordValue, err error) ([]sqlc.RecordValue, error) {
	if err != nil {
		return nil, err
	}

	for i := range rows {
		rows[i].Data, err = recordcodec.WithLocation(rows[i].Kind, rows[i].Data, rows[i].Location)
		if err != nil {
			return nil, ErrCorrupt
		}
	}

	return rows, nil
}

func recordValues(ctx context.Context, q *sqlc.Queries, id, version string) ([]sqlc.RecordValue, error) {
	return materializeRecords(q.Records(ctx, sqlc.RecordsParams{ArtifactID: id, Version: version}))
}

func activityInputs(ctx context.Context, q *sqlc.Queries, id string) ([]sqlc.RecordValue, error) {
	return materializeRecords(q.ActivityInputs(ctx, sql.NullString{String: id, Valid: true}))
}
