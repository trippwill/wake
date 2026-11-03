// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"context"

	"go.trippwill.dev/wake/internal/persistence/sqlite/sqlc"
	"go.trippwill.dev/wake/provider"
)

func scheduleAcquisition(
	ctx context.Context,
	q *sqlc.Queries,
	connection string,
	streams []provider.Stream,
) error {
	for _, stream := range streams {
		if err := q.EnsureWork(
			ctx,
			sqlc.EnsureWorkParams{ConnectionID: connection, Stream: stream.ID(), Operation: "acquire"},
		); err != nil {
			return err
		}

		if err := q.SetScheduled(
			ctx,
			sqlc.SetScheduledParams{
				ConnectionID: connection,
				Stream:       stream.ID(),
				Operation:    "acquire",
				Enabled:      1,
			},
		); err != nil {
			return err
		}
	}

	return nil
}
