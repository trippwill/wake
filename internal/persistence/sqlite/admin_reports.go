// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"context"
	"encoding/json"

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/application/calculation"
	"go.trippwill.dev/wake/internal/persistence/sqlite/sqlc"
)

func (s *Store) ReportList(ctx context.Context, query application.ListQuery) (application.ReportList, error) {
	options := query.Options()
	if query.Scope() != application.ReportsScope || options.Page < 1 || options.PageSize < 1 {
		return application.ReportList{}, calculation.ErrInvalid
	}

	return readModel(ctx, s, func(q *sqlc.Queries) (application.ReportList, error) {
		counts, err := q.AdminReportCount(ctx, options.Search)
		if err != nil {
			return application.ReportList{}, err
		}

		items, err := s.reportRows(ctx, q, options.Search, int64(options.PageSize), query.Offset())

		return application.ReportList{PageInfo: pageInfo(query, counts.Total, counts.Matched), Items: items}, err
	})
}

func (s *Store) reportRows(
	ctx context.Context,
	q *sqlc.Queries,
	search string,
	pageSize, pageOffset int64,
) ([]application.ReportEntry, error) {
	versions, versionJSON := s.reportVersions()

	rows, err := q.AdminReportRows(ctx, sqlc.AdminReportRowsParams{
		Search: search, PageSize: pageSize, PageOffset: pageOffset, Versions: versionJSON,
	})
	if err != nil {
		return nil, err
	}

	out := make([]application.ReportEntry, 0, len(rows))
	for _, row := range rows {
		received, e := parseOptionalTime(row.Retrieved)
		if e != nil {
			return nil, e
		}

		version, providerAvailable := versions[row.Provider]
		out = append(out, application.ReportEntry{
			ProcessingStatus: application.ProcessingStatus{
				Artifact: row.ArtifactID, Stream: row.Stream, Receipt: row.ID,
				Retrieved: row.Retrieved, Hash: row.Hash, ComparisonPolicy: row.Policy,
				RequestedFrom: row.RequestedFrom, RequestedTo: row.RequestedTo,
				ReportedFrom: row.ReportedFrom, ReportedTo: row.ReportedTo,
				Complete: row.Processed != 0, Failure: s.processingFailure(row.ArtifactID),
				ActivityClaims: row.ActivityClaims, ExcludedRows: row.ExcludedRows, NeedsReview: row.NeedsReview,
			},
			ConnectionID: row.ConnectionID, ConnectionName: row.ConnectionName, Provider: row.Provider,
			Operation: row.Operation, Received: received, ReusedBody: row.ReusedBody != 0,
			ProviderAvailable: providerAvailable, CanNormalize: version != "",
			Accepted: row.Accepted, Enriched: row.Enriched, Repeated: row.Repeated, PositionSets: row.PositionSets,
		})
	}

	return out, nil
}

func (s *Store) reportVersions() (map[string]string, string) {
	versions := make(map[string]string, len(s.registrations))
	for _, registration := range s.registrations {
		version := ""
		if registration.Normalizer() != nil {
			version = registration.Version()
		}

		versions[registration.Descriptor().ID()] = version
	}

	encoded, _ := json.Marshal(versions)

	return versions, string(encoded)
}
