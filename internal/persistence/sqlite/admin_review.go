// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0
package persistence

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"go.trippwill.dev/wake/financial"
	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/persistence/sqlite/sqlc"
	"go.trippwill.dev/wake/provider"
	"go.trippwill.dev/wake/source"
)

func reviewList(
	ctx context.Context,
	q *sqlc.Queries,
	query application.ListQuery,
) (application.ReviewList, error) {
	options := query.Options()
	out := application.ReviewList{}

	kinds, _ := json.Marshal(options.KindMatches)
	matches, _ := json.Marshal(options.ReviewMatches)
	conversion, _ := json.Marshal(options.ConversionText)

	counts, err := q.AdminReviewCount(
		ctx,
		sqlc.AdminReviewCountParams{
			Search:                  options.Search,
			LabelSearch:             options.LabelSearch,
			MissingValues:           options.DisplayMatches.MissingValues,
			MissingInstrumentAction: options.DisplayMatches.MissingInstrumentAction,
			KindMatches:             string(kinds),
			ReviewMatches:           string(matches),
			ConversionText:          string(conversion),
		},
	)
	if err != nil {
		return out, err
	}

	out.PageInfo = pageInfo(query, counts.Total, counts.Matched)

	rows, err := q.AdminReviewRows(
		ctx,
		sqlc.AdminReviewRowsParams{
			Search:                  options.Search,
			LabelSearch:             options.LabelSearch,
			MissingValues:           options.DisplayMatches.MissingValues,
			MissingInstrumentAction: options.DisplayMatches.MissingInstrumentAction,
			KindMatches:             string(kinds),
			ReviewMatches:           string(matches),
			ConversionText:          string(conversion),
			PageSize:                int64(options.PageSize),
			PageOffset:              query.Offset(),
		},
	)
	if err != nil {
		return out, err
	}

	for _, row := range rows {
		var activity *application.PublishedActivity
		if row.Activity != "" {
			if json.Unmarshal([]byte(row.Activity), &activity) != nil || activity == nil {
				return out, ErrCorrupt
			}
		}

		out.Items = append(
			out.Items,
			application.ReviewEntry{
				Kind:     application.ReviewKind(row.Kind),
				Activity: activity,
				ID:       row.ID,
				Name:     row.Name,
				Artifact: row.Artifact,
				Location: row.Location,
				Status:   application.CorrectionStatus(row.Status),
				Count:    row.ClaimCount,
			},
		)
	}

	return out, nil
}

func (s *Store) ReviewList(ctx context.Context, query application.ListQuery) (application.ReviewList, error) {
	if query.Scope() != application.ReviewScope || query.Options().Page < 1 || query.Options().PageSize < 1 {
		return application.ReviewList{}, application.ErrInvalid
	}

	return readModel(
		ctx,
		s,
		func(q *sqlc.Queries) (application.ReviewList, error) { return reviewList(ctx, q, query) },
	)
}

func (s *Store) Overview(ctx context.Context) (application.OverviewView, error) {
	return readModel(ctx, s, func(q *sqlc.Queries) (application.OverviewView, error) {
		out := application.OverviewView{}

		var err error

		out.Status, err = systemStatus(ctx, q)
		if err != nil {
			return out, err
		}

		counts, err := q.AdminCounts(ctx)
		if err != nil {
			return out, err
		}

		out.ActivityCount, out.InstrumentCount = counts.ActivityCount, counts.InstrumentCount
		query, _ := application.NewListQuery(application.ListOptions{PageSize: 10}, application.ReviewScope)
		out.Review, err = reviewList(ctx, q, query)

		return out, err
	})
}

func (s *Store) SourceCandidate(
	ctx context.Context,
	id, artifact string,
	ref source.Reference,
) (application.Candidate, error) {
	return readModel(ctx, s, func(q *sqlc.Queries) (application.Candidate, error) {
		row, err := q.AdminCandidate(
			ctx,
			sqlc.AdminCandidateParams{
				ConnectionID: id,
				ArtifactID:   artifact,
				Authority:    ref.Authority(),
				Kind:         ref.Kind(),
				Scope:        ref.Scope(),
				Value:        ref.Value(),
			},
		)
		if errors.Is(err, sql.ErrNoRows) {
			return application.Candidate{}, application.ErrCandidate
		}

		if err != nil {
			return application.Candidate{}, err
		}

		currency := financial.Currency{}
		if row.Currency != "" {
			currency, err = financial.ParseCurrency(row.Currency)
			if err != nil {
				return application.Candidate{}, ErrCorrupt
			}
		}

		candidate, err := provider.NewCandidate(ref, row.Name, currency)
		if err != nil {
			return application.Candidate{}, ErrCorrupt
		}

		return application.Candidate{Artifact: row.ArtifactID, Candidate: candidate}, nil
	})
}
