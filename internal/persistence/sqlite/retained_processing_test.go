// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"context"

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/persistence/sqlite/sqlc"
	"go.trippwill.dev/wake/source"
)

func (s *Store) Process(ctx context.Context, id, version string) error {
	return application.ProcessRetained(ctx, s, id, version)
}

func matchReferences(
	ctx context.Context,
	q *sqlc.Queries,
	account string,
	event source.Reference,
	aliases []source.Reference,
	distinct bool,
) (string, bool, error) {
	return application.MatchActivityReferences(ctx, retainedProcessing{q: q}, account, event, aliases, distinct)
}
