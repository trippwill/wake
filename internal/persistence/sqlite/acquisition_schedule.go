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
	return scheduleStreams(ctx, q, connection, streams, "acquire")
}

func scheduleStreams(
	ctx context.Context,
	q *sqlc.Queries,
	connection string,
	streams []provider.Stream,
	operation string,
) error {
	for _, stream := range streams {
		if operation == "discover" && !stream.CanDiscover() {
			continue
		}

		if err := q.EnsureWork(
			ctx,
			sqlc.EnsureWorkParams{ConnectionID: connection, Stream: stream.ID(), Operation: operation},
		); err != nil {
			return err
		}

		if err := q.SetScheduled(
			ctx,
			sqlc.SetScheduledParams{
				ConnectionID: connection,
				Stream:       stream.ID(),
				Operation:    operation,
				Enabled:      1,
			},
		); err != nil {
			return err
		}
	}

	return nil
}
