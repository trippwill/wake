// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"strings"

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/persistence/sqlite/sqlc"
)

// Each response is materialized in one consistent read. The transaction is
// released before presentation or a client can perform any I/O with the result.
func readModel[T any](ctx context.Context, s *Store, read func(*sqlc.Queries) (T, error)) (T, error) {
	var zero T

	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return zero, err
	}

	defer func() { _ = tx.Rollback() }()

	result, err := read(s.queries.WithTx(tx))
	if err != nil {
		return zero, err
	}

	if err = tx.Commit(); err != nil {
		return zero, err
	}

	return result, nil
}

func pageInfo(query application.ListQuery, total, matched int64) application.PageInfo {
	options := query.Options()
	return application.PageInfo{Page: options.Page, PageSize: options.PageSize, Total: total, Matched: matched}
}

func searchMatches(options application.ListOptions) (string, string, string) {
	type stateMatch struct {
		Lifecycle application.LifecycleState     `json:"lifecycle"`
		Review    application.ReviewReasons      `json:"review"`
		Authority application.SelectionAuthority `json:"authority"`
	}

	type referenceMatch struct {
		Status    application.ReferenceStatus    `json:"status"`
		Authority application.SelectionAuthority `json:"authority"`
	}

	states := make([]stateMatch, 0, len(options.StateMatches))
	references := make([]referenceMatch, 0, len(options.ReferenceMatches))
	for _, state := range options.StateMatches {
		states = append(states, stateMatch{state.Lifecycle(), state.ReviewReasons(), state.Authority()})
	}

	for _, reference := range options.ReferenceMatches {
		references = append(references, referenceMatch{reference.Status, reference.Authority})
	}

	stateJSON, _ := json.Marshal(states)
	referenceJSON, _ := json.Marshal(references)

	kindsJSON, _ := json.Marshal(options.KindMatches)

	return string(stateJSON), string(referenceJSON), string(kindsJSON)
}

func decodeAcceptedResource(data []byte, kind string) (application.CanonicalResource, error) {
	var resource application.CanonicalResource
	if json.Unmarshal(data, &resource) != nil || resource.ID == "" || resource.Kind != kind ||
		resource.Revision < 1 {
		return resource, ErrCorrupt
	}

	if kind == "activity" && resource.Activity == nil || kind == "instruments" && resource.Instrument == nil {
		return resource, ErrCorrupt
	}

	return resource, nil
}

func (s *Store) Accounts(ctx context.Context) ([]application.Account, error) {
	rows, err := s.queries.Accounts(ctx)
	if err != nil {
		return nil, err
	}

	accounts := make([]application.Account, 0, len(rows))
	for _, row := range rows {
		account, err := accountValue(row)
		if err != nil {
			return nil, err
		}

		accounts = append(accounts, account)
	}

	return accounts, nil
}

//go:embed admin_activity.sql
var adminActivityTemplateSQL string

//go:embed activity_search_fields.sql
var activitySearchFieldsSQL string

//go:embed activity_search_index.sql
var activitySearchIndexTemplateSQL string

var (
	adminActivitySQL       = withActivitySearchFields(adminActivityTemplateSQL)
	activitySearchIndexSQL = withActivitySearchFields(activitySearchIndexTemplateSQL)
)

func withActivitySearchFields(statement string) string {
	_, fields, _ := strings.Cut(activitySearchFieldsSQL, "\n\n")
	return strings.ReplaceAll(statement, "{{activity_search_fields}}", strings.TrimSpace(fields))
}

//go:embed admin_activity_date.sql
var adminActivityDateSQL string

const errActivityIndexIncomplete Error = "incomplete Activity index keys"

var (
	adminActivityDateAscSQL  = strings.ReplaceAll(adminActivityDateSQL, "{{direction}}", "ASC")
	adminActivityDateDescSQL = strings.ReplaceAll(adminActivityDateSQL, "{{direction}}", "DESC")
)

func (s *Store) ActivityList(
	ctx context.Context,
	query application.ListQuery,
) (application.ActivityList, error) {
	options := query.Options()
	if query.Scope() != application.ActivityScope || options.Page < 1 || options.PageSize < 1 {
		return application.ActivityList{}, application.ErrInvalid
	}

	if options.Sort == "date" && options.Search == "" && options.AccountID == "" && options.State == "" {
		statement := adminActivityDateDescSQL
		if options.Direction == "asc" {
			statement = adminActivityDateAscSQL
		}

		page, err := s.readActivityPage(ctx, query, statement, options.Kind, options.PageSize, query.Offset())
		if !errors.Is(err, errActivityIndexIncomplete) {
			return page, err
		}
	}

	states, references, kinds := searchMatches(options)
	presentation, _ := json.Marshal(options.ActivityText)

	return s.readActivityPage(
		ctx, query, adminActivitySQL,
		options.AccountID, options.Kind, options.State, options.Search, string(presentation),
		options.DisplayMatches.MissingValues, options.DisplayMatches.InapplicableValues,
		kinds, states, references, options.Sort, options.Direction, options.PageSize, query.Offset(),
	)
}

func (s *Store) readActivityPage(
	ctx context.Context, query application.ListQuery, statement string, args ...any,
) (application.ActivityList, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if statement == adminActivitySQL {
		rows, err = s.activityQuery.QueryContext(ctx, args...)
	} else {
		rows, err = s.activityDB.QueryContext(ctx, statement, args...)
	}

	if err != nil {
		return application.ActivityList{}, err
	}

	defer func() { _ = rows.Close() }()

	var (
		total   sql.NullInt64
		matched int64
	)

	out := application.ActivityList{Items: []application.ActivityListItem{}}
	for rows.Next() {
		var (
			item     application.ActivityListItem
			snapshot []byte
			stateRow sqlc.Execution
		)

		if err = rows.Scan(
			&total, &matched, &snapshot, &stateRow.Status, &stateRow.ReviewReasons,
			&stateRow.SelectionAuthority, &item.InstrumentName, &item.InstrumentTicker,
			&item.Reference.Status, &item.Reference.Authority, &item.AccountName,
		); err != nil {
			return out, err
		}

		if !total.Valid {
			return out, errActivityIndexIncomplete
		}

		if snapshot == nil {
			continue
		}

		item.Resource, err = decodeAcceptedResource(snapshot, "activity")
		if err != nil {
			return out, err
		}

		item.State, err = activityState(stateRow)
		if err != nil {
			return out, err
		}

		out.Items = append(out.Items, item)
	}

	out.PageInfo = pageInfo(query, total.Int64, matched)

	return out, rows.Err()
}

func (s *Store) InstrumentList(
	ctx context.Context,
	query application.ListQuery,
) (application.InstrumentList, error) {
	options := query.Options()
	if query.Scope() != application.InstrumentScope || options.Page < 1 || options.PageSize < 1 {
		return application.InstrumentList{}, application.ErrInvalid
	}

	_, references, _ := searchMatches(options)

	return readModel(ctx, s, func(q *sqlc.Queries) (application.InstrumentList, error) {
		out := application.InstrumentList{}

		counts, err := q.AdminInstrumentCount(
			ctx,
			sqlc.AdminInstrumentCountParams{
				KindFilter:       options.Kind,
				MissingValues:    options.DisplayMatches.MissingValues,
				StateFilter:      options.State,
				Search:           options.Search,
				ReferenceMatches: references,
			},
		)
		if err != nil {
			return out, err
		}

		out.PageInfo = pageInfo(query, counts.Total, counts.Matched)

		rows, err := q.AdminInstrumentRows(
			ctx,
			sqlc.AdminInstrumentRowsParams{
				KindFilter:       options.Kind,
				MissingValues:    options.DisplayMatches.MissingValues,
				StateFilter:      options.State,
				Search:           options.Search,
				ReferenceMatches: references,
				SortField:        options.Sort,
				Direction:        options.Direction,
				PageSize:         int64(options.PageSize),
				PageOffset:       query.Offset(),
			},
		)
		if err != nil {
			return out, err
		}

		out.Items = make([]application.InstrumentListItem, 0, len(rows))
		for _, row := range rows {
			resource, e := decodeAcceptedResource(row.Snapshot, "instruments")
			if e != nil {
				return out, e
			}

			out.Items = append(
				out.Items,
				application.InstrumentListItem{
					Resource:  resource,
					Authority: application.SelectionAuthority(row.SelectionAuthority),
				},
			)
		}

		return out, nil
	})
}
