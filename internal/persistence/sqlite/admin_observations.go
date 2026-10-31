// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/persistence/sqlite/sqlc"
)

func (s *Store) ObservationList(
	ctx context.Context,
	kind, id string,
	query application.ListQuery,
) (application.ObservationList, error) {
	options := query.Options()
	if query.Scope() != application.ObservationScope || options.Page < 1 || options.PageSize < 1 || id == "" ||
		len(id) > 256 ||
		kind != "activity" && kind != "instrument" {
		return application.ObservationList{}, application.ErrInvalid
	}

	matches, _ := json.Marshal(options.ObservationMatches)

	return readModel(ctx, s, func(q *sqlc.Queries) (application.ObservationList, error) {
		out := application.ObservationList{Items: []application.Observation{}}

		var err error
		if kind == "activity" {
			_, err = q.Execution(ctx, id)
		} else {
			_, err = q.Instrument(ctx, id)
		}

		if errors.Is(err, sql.ErrNoRows) {
			return out, application.ErrNotFound
		}

		if err != nil {
			return out, err
		}

		counts, err := q.AdminObservationCount(
			ctx,
			sqlc.AdminObservationCountParams{
				SubjectKind:    kind,
				SubjectID:      sql.NullString{String: id, Valid: true},
				Search:         options.Search,
				OutcomeMatches: string(matches),
			},
		)
		if err != nil {
			return out, err
		}

		out.PageInfo = pageInfo(query, counts.Total, counts.Matched)

		rows, err := q.AdminObservationRows(
			ctx,
			sqlc.AdminObservationRowsParams{
				SubjectKind:    kind,
				SubjectID:      sql.NullString{String: id, Valid: true},
				Search:         options.Search,
				OutcomeMatches: string(matches),
				SortField:      options.Sort,
				Direction:      options.Direction,
				PageSize:       int64(options.PageSize),
				PageOffset:     query.Offset(),
			},
		)
		if err != nil {
			return out, err
		}

		for _, row := range rows {
			out.Items = append(
				out.Items,
				application.Observation{
					Artifact:  row.ArtifactID,
					Version:   row.Version,
					Location:  row.Location,
					Rule:      row.Rule,
					Retrieved: row.Retrieved,
					Outcome:   application.ObservationOutcome(row.Outcome),
				},
			)
		}

		return out, nil
	})
}
