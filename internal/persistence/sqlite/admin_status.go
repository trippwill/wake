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

func prerequisites(ctx context.Context, q *sqlc.Queries) (application.Prerequisites, error) {
	out := application.Prerequisites{}

	profile, err := q.Profile(ctx)
	if err != nil {
		return out, err
	}

	out.Storage = application.StorageStatus{
		Paused:          profile.Paused != 0,
		ReferenceReview: profile.ReferenceReview != 0,
		WarehouseReset:  profile.WarehouseReset != 0,
		Revision:        profile.RecoveryRevision,
	}

	account, err := q.Account(ctx)
	if err == nil {
		out.Account, err = accountValue(account)
	}

	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}

	setting, err := q.AdminReferenceStatus(ctx)
	if err == nil {
		out.ReferenceKey = setting.HasKey
		out.ReferenceFailure = setting.Failure
	} else if !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}

	out.ConnectionCount, err = q.AdminSourceCount(ctx, "")

	return out, err
}

func (s *Store) Prerequisites(ctx context.Context) (application.Prerequisites, error) {
	return readModel(
		ctx,
		s,
		func(q *sqlc.Queries) (application.Prerequisites, error) { return prerequisites(ctx, q) },
	)
}

func connectionStatus(
	ctx context.Context,
	q *sqlc.Queries,
	connection sqlc.Connection,
	account application.Account,
) (application.ConnectionView, error) {
	view := application.ConnectionView{ID: connection.ID, Name: connection.Name, Provider: connection.Provider}
	if view.Name == "" {
		view.Name = "Retained source connection"
		if account.Connection() == connection.ID {
			view.Name = "Source for " + account.Details().Name()
		}
	}

	readiness, err := q.AccessReadiness(ctx, connection.ID)
	if err != nil {
		return view, err
	}

	view.SettingsStored = readiness.SettingsStored
	view.CredentialStored = readiness.CredentialStored
	view.MissingAccess = !view.SettingsStored

	view.CredentialKeys, err = q.CredentialKeys(ctx, connection.ID)
	if err != nil {
		return view, err
	}

	statuses, err := q.WorkStatuses(ctx, connection.ID)
	if err != nil {
		return view, err
	}

	for _, status := range statuses {
		view.Work = append(
			view.Work,
			application.WorkStatus{
				Operation: status.Operation,
				Progress:  provider.ProgressPhase(status.Observed),
				Failure:   status.Failure,
				Next:      status.Next,
			},
		)
	}

	processing, err := q.ProcessingStatuses(ctx, connection.ID)
	if err != nil {
		return view, err
	}

	for _, r := range processing {
		view.Processing = append(
			view.Processing,
			application.ProcessingStatus{
				Artifact:         r.ID,
				Receipt:          r.ReceiptID,
				Retrieved:        r.Retrieved,
				Hash:             r.Hash,
				ComparisonPolicy: r.Policy,
				Complete:         r.Processed == 1,
				RequestedFrom:    r.RequestedFrom,
				RequestedTo:      r.RequestedTo,
				ReportedFrom:     r.ReportedFrom,
				ReportedTo:       r.ReportedTo,
				Failure:          r.ProcessingFailure,
				TradeClaims:      r.TradeClaims,
				LifecycleClaims:  r.LifecycleClaims,
				ConversionClaims: r.ConversionClaims,
				ExcludedRows:     r.ExcludedRows,
				NeedsReview:      r.NeedsReview,
			},
		)
	}

	return view, nil
}

func (s *Store) SourcePage(ctx context.Context, query application.ListQuery) (application.SourcePage, error) {
	options := query.Options()
	if query.Scope() != application.SourceScope || options.Page < 1 || options.PageSize < 1 {
		return application.SourcePage{}, application.ErrInvalid
	}

	return readModel(ctx, s, func(q *sqlc.Queries) (application.SourcePage, error) {
		out := application.SourcePage{}

		var err error

		out.Prerequisites, err = prerequisites(ctx, q)
		if err != nil {
			return out, err
		}

		matched, err := q.AdminSourceCount(ctx, options.Search)
		if err != nil {
			return out, err
		}

		out.PageInfo = pageInfo(query, out.ConnectionCount, matched)

		rows, err := q.AdminSourceRows(
			ctx,
			sqlc.AdminSourceRowsParams{
				Search:     options.Search,
				PageSize:   int64(options.PageSize),
				PageOffset: query.Offset(),
			},
		)
		if err != nil {
			return out, err
		}

		for _, row := range rows {
			view, e := connectionStatus(ctx, q, row, out.Account)
			if e != nil {
				return out, e
			}

			out.Connections = append(out.Connections, view)
		}

		return out, nil
	})
}

func (s *Store) SourceDetail(
	ctx context.Context,
	id string,
	query application.ListQuery,
) (application.SourceDetail, error) {
	options := query.Options()
	if query.Scope() != application.SourceScope || options.Page < 1 || options.PageSize < 1 {
		return application.SourceDetail{}, application.ErrInvalid
	}

	return readModel(ctx, s, func(q *sqlc.Queries) (application.SourceDetail, error) {
		out := application.SourceDetail{}

		var err error

		out.Prerequisites, err = prerequisites(ctx, q)
		if err != nil {
			return out, err
		}

		row, err := q.Descriptor(ctx, id)
		if errors.Is(err, sql.ErrNoRows) {
			return out, application.ErrNotFound
		}

		if err != nil {
			return out, err
		}

		out.Connection, err = connectionStatus(ctx, q, row, out.Account)
		if err != nil {
			return out, err
		}

		configured, err := q.Connection(ctx, id)
		if err == nil {
			var values map[string]string
			if json.Unmarshal(configured.Settings, &values) != nil {
				return out, ErrCorrupt
			}

			out.Connection.Settings, err = provider.NewSettings(values)
			if err != nil {
				return out, ErrCorrupt
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return out, err
		}

		count, err := q.AdminCandidateCount(ctx, id)
		if err != nil {
			return out, err
		}

		out.PageInfo = pageInfo(query, count, count)

		rows, err := q.AdminCandidates(
			ctx,
			sqlc.AdminCandidatesParams{
				ConnectionID: id,
				PageSize:     int64(options.PageSize),
				PageOffset:   query.Offset(),
			},
		)
		if err != nil {
			return out, err
		}

		for _, r := range rows {
			ref, e := source.NewReference(r.Authority, r.Kind, r.Scope, r.Value)
			if e != nil {
				return out, ErrCorrupt
			}

			currency := financial.Currency{}
			if r.Currency != "" {
				currency, e = financial.ParseCurrency(r.Currency)
				if e != nil {
					return out, ErrCorrupt
				}
			}

			candidate, e := provider.NewCandidate(ref, r.Name, currency)
			if e != nil {
				return out, ErrCorrupt
			}

			out.Connection.Candidates = append(
				out.Connection.Candidates,
				application.Candidate{Artifact: r.ArtifactID, Candidate: candidate},
			)
		}

		return out, nil
	})
}

func systemStatus(ctx context.Context, q *sqlc.Queries) (application.SystemStatus, error) {
	out := application.SystemStatus{}

	var err error

	out.Prerequisites, err = prerequisites(ctx, q)
	if err != nil {
		return out, err
	}

	rows, err := q.AdminSourceRows(ctx, sqlc.AdminSourceRowsParams{Search: "", PageSize: 50})
	if err != nil {
		return out, err
	}

	out.MoreConnections = out.ConnectionCount > int64(len(rows))
	for _, row := range rows {
		view, e := connectionStatus(ctx, q, row, out.Account)
		if e != nil {
			return out, e
		}

		out.Connections = append(out.Connections, view)
	}

	jobs, err := q.RecoveryJobs(ctx)
	if err != nil {
		return out, err
	}

	for _, job := range jobs {
		out.Recovery = append(
			out.Recovery,
			application.RecoveryJob{
				ID:        job.ID,
				Status:    job.Status,
				Failure:   job.Failure,
				Actor:     job.Actor,
				Created:   job.Created,
				Completed: job.Completed,
				Total:     job.Total,
			},
		)
	}

	return out, nil
}

func (s *Store) SystemStatus(ctx context.Context) (application.SystemStatus, error) {
	return readModel(
		ctx,
		s,
		func(q *sqlc.Queries) (application.SystemStatus, error) { return systemStatus(ctx, q) },
	)
}
