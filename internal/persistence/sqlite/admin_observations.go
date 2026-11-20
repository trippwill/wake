// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/application/calculation"
	"go.trippwill.dev/wake/internal/persistence/sqlite/recordcodec"
	"go.trippwill.dev/wake/internal/persistence/sqlite/sqlc"
	"go.trippwill.dev/wake/source"
)

func (s *Store) ObservationList(
	ctx context.Context,
	kind, id string,
	query application.ListQuery,
) (application.ObservationList, error) {
	options := query.Options()
	if query.Scope() != application.ObservationScope || options.Page < 1 || options.PageSize < 1 || id == "" ||
		len(id) > 256 ||
		kind != "activity" && kind != "instrument" && kind != "artifact" && kind != "activity-appearances" {
		return application.ObservationList{}, calculation.ErrInvalid
	}

	matches, _ := json.Marshal(options.ObservationMatches)
	labels, _ := json.Marshal(options.ObservationLabels)

	return readModel(ctx, s, func(q *sqlc.Queries) (application.ObservationList, error) {
		out := application.ObservationList{Items: []application.Observation{}}

		var (
			err      error
			provider string
		)
		switch kind {
		case "activity", "activity-appearances":
			_, err = q.Execution(ctx, id)
		case "instrument":
			_, err = q.Instrument(ctx, id)
		case "artifact":
			provider, err = q.ArtifactProvider(ctx, id)
		}

		if errors.Is(err, sql.ErrNoRows) {
			return out, application.ErrNotFound
		}

		if err != nil {
			return out, err
		}

		rows, err := q.AdminObservationRows(
			ctx,
			sqlc.AdminObservationRowsParams{
				SubjectKind:    kind,
				SubjectID:      id,
				Search:         options.Search,
				OutcomeMatches: string(matches),
				OutcomeLabels:  string(labels),
				SortField:      options.Sort,
				Direction:      options.Direction,
				PageSize:       int64(options.PageSize),
				PageOffset:     query.Offset(),
			},
		)
		if err != nil {
			return out, err
		}

		if len(rows) > 0 {
			out.PageInfo = pageInfo(query, rows[0].Total, rows[0].Matched)
		}

		if kind == "artifact" && out.Total == 0 {
			out.Processing, err = s.reportProcessing(ctx, q, id, provider)
			if err != nil {
				return out, err
			}
		}

		for _, row := range rows {
			if row.ArtifactID == "" {
				continue
			}

			entry := application.Observation{
				Artifact:          row.ArtifactID,
				Version:           row.Version,
				Location:          row.Location,
				Rule:              row.Rule,
				Retrieved:         row.Retrieved,
				Outcome:           calculation.ObservationOutcome(row.Outcome),
				ActivityID:        row.ExecutionID,
				SourceName:        row.SourceName,
				SourceProvider:    row.SourceProvider,
				Stream:            row.Stream,
				EarlierAppearance: row.EarlierAppearance != 0,
			}
			if len(row.Reported) != 0 {
				entry.Reported, err = reportObservationValue(row.Location, row.Reported)
				if err != nil {
					return out, err
				}
			}

			out.Items = append(out.Items, entry)
		}

		return out, nil
	})
}

func (s *Store) reportProcessing(
	ctx context.Context,
	q *sqlc.Queries,
	id, provider string,
) (application.ReportProcessing, error) {
	version := s.interpretationVersion(provider)
	if version == "" {
		return application.ReportProcessingUnavailable, nil
	}

	complete, err := q.ProcessingComplete(ctx, sqlc.ProcessingCompleteParams{ArtifactID: id, Version: version})
	if err != nil {
		return application.ReportProcessingUnavailable, err
	}

	if complete {
		return application.ReportProcessingComplete, nil
	}

	if s.processingFailure(id) != "" {
		return application.ReportProcessingFailed, nil
	}

	return application.ReportProcessingPending, nil
}

func (s *Store) SourceObservation(
	ctx context.Context, key application.SourceLocation,
) (application.SourceObservation, error) {
	if key.Artifact == "" || len(key.Artifact) > 256 || key.Version == "" || len(key.Version) > 256 ||
		key.Location == "" || len(key.Location) > 1024 {
		return application.SourceObservation{}, calculation.ErrInvalid
	}

	row, err := s.queries.AdminSourceObservation(ctx, sqlc.AdminSourceObservationParams{
		ArtifactID: key.Artifact, Version: key.Version, Location: key.Location,
	})
	if errors.Is(err, sql.ErrNoRows) {
		err = application.ErrNotFound
	}

	if err != nil {
		return application.SourceObservation{}, err
	}

	reported, err := reportObservationValue(key.Location, row.Data)
	if err != nil {
		return application.SourceObservation{}, err
	}

	return application.SourceObservation{
		Observation: application.Observation{
			Artifact: key.Artifact, Version: key.Version, Location: key.Location,
			Rule: row.Rule, Outcome: calculation.ObservationOutcome(row.Outcome), ActivityID: row.ExecutionID,
			Reported: reported, EarlierAppearance: row.EarlierAppearance != 0,
		},
		Previous: row.Previous, Next: row.Next,
	}, nil
}

func reportObservationValue(location string, data []byte) (source.ActivityObservation, error) {
	value, err := recordcodec.WithLocation("activity", data, location)
	if err != nil {
		return source.ActivityObservation{}, ErrCorrupt
	}

	return recordcodec.DecodeObservation(value)
}
