// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"context"
	"database/sql"
	"errors"
	"time"

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

	out.AccountCount, err = q.AccountCount(ctx)
	if err != nil {
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

func connectionAccess(
	ctx context.Context,
	q *sqlc.Queries,
	connection sqlc.Connection,
) (application.ConnectionView, error) {
	view := application.ConnectionView{ID: connection.ID, Name: connection.Name, Provider: connection.Provider}

	account, err := q.AccountByConnection(ctx, connection.ID)
	if err == nil {
		view.Account, err = accountValue(account)
	}

	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return view, err
	}

	if view.Name == "" {
		view.Name = "Retained source connection"
		if view.Account.ID() != "" {
			view.Name = "Source for " + view.Account.Details().Name()
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

	if view.SettingsStored {
		configured, e := q.Connection(ctx, connection.ID)
		if e != nil {
			return view, e
		}

		view.Settings, view.PollOverrides, err = decodeConnectionSettings(configured.Settings)
		if err != nil {
			return view, ErrCorrupt
		}
	}

	return view, nil
}

func connectionStatus(
	ctx context.Context,
	q *sqlc.Queries,
	connection sqlc.Connection,
) (application.ConnectionView, error) {
	view, err := connectionAccess(ctx, q, connection)
	if err != nil {
		return view, err
	}

	view.Work, err = connectionWork(ctx, q, connection.ID)
	if err != nil {
		return view, err
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
				Stream:           r.Stream,
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
				ActivityClaims:   r.ActivityClaims,
				ExcludedRows:     r.ExcludedRows,
				NeedsReview:      r.NeedsReview,
			},
		)
	}

	return view, nil
}

func connectionWork(ctx context.Context, q *sqlc.Queries, id string) ([]application.WorkStatus, error) {
	statuses, err := q.WorkStatuses(ctx, id)
	if err != nil {
		return nil, err
	}

	var work []application.WorkStatus
	for _, status := range statuses {
		next, err := parseOptionalTime(status.Next)
		if err != nil {
			return nil, err
		}

		last, err := parseOptionalTime(status.LastRetrieved)
		if err != nil {
			return nil, err
		}

		work = append(work, application.WorkStatus{
			Operation: status.Operation, Stream: status.Stream,
			Progress:  provider.ProgressPhase(status.Observed),
			Completed: status.ObservedCompleted, Total: status.ObservedTotal,
			Failure: status.Failure, Next: next, LastRetrieved: last,
			Scheduled: status.Scheduled, RetryBlocked: status.RetryBlocked != 0,
		})
	}

	return work, nil
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
			view, e := connectionStatus(ctx, q, row)
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

		out.Connection, err = connectionStatus(ctx, q, row)
		if err != nil {
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

	update, err := q.HistoryUpdate(ctx)
	if err != nil {
		return out, err
	}

	if update.Target != 0 {
		out.HistoryUpdate = application.HistoryUpdateStatus{
			Rule: string(application.RetainedClaimsRule), Phase: update.Phase, Completed: update.Completed,
		}
		if update.RetryAt != "" {
			out.HistoryUpdate.RetryAt, err = time.Parse(time.RFC3339Nano, update.RetryAt)
			if err != nil {
				return out, ErrCorrupt
			}
		}
	}

	rows, err := q.AdminSourceRows(ctx, sqlc.AdminSourceRowsParams{Search: "", PageSize: 50})
	if err != nil {
		return out, err
	}

	out.MoreConnections = out.ConnectionCount > int64(len(rows))
	for _, row := range rows {
		view, e := connectionStatus(ctx, q, row)
		if e != nil {
			return out, e
		}

		out.Connections = append(out.Connections, view)
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

// SourceAccessPage reads only access metadata, without processing history or financial state.
func (s *Store) SourceAccessPage(
	ctx context.Context,
	query application.ListQuery,
) (application.SourcePage, error) {
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
			view, e := connectionAccess(ctx, q, row)
			if e != nil {
				return out, e
			}

			out.Connections = append(out.Connections, view)
		}

		return out, nil
	})
}

// SourceAccess captures all source-access prerequisites together for authority checks.
// Unlike UI lists it is intentionally complete, and never reads financial/processing history.
func (s *Store) SourceAccess(ctx context.Context) (application.AccessSnapshot, error) {
	return readModel(ctx, s, func(q *sqlc.Queries) (application.AccessSnapshot, error) {
		out := application.AccessSnapshot{}

		var err error

		out.Prerequisites, err = prerequisites(ctx, q)
		if err != nil {
			return out, err
		}

		rows, err := q.AdminAccessSources(ctx)
		if err != nil {
			return out, err
		}

		for _, row := range rows {
			c, e := connectionAccess(ctx, q, row)
			if e != nil {
				return out, e
			}

			out.Connections = append(out.Connections, c)
		}

		return out, nil
	})
}
