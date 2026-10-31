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

func (s *Store) ActivityList(
	ctx context.Context,
	query application.ListQuery,
) (application.ActivityList, error) {
	options := query.Options()
	if query.Scope() != application.ActivityScope || options.Page < 1 || options.PageSize < 1 {
		return application.ActivityList{}, application.ErrInvalid
	}

	states, references, kinds := searchMatches(options)
	amounts, _ := json.Marshal(options.AmountMatches)

	return readModel(ctx, s, func(q *sqlc.Queries) (application.ActivityList, error) {
		out := application.ActivityList{}

		account, err := q.Account(ctx)
		if err == nil {
			out.Account, err = accountValue(account)
		}

		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return out, err
		}

		counts, err := q.AdminActivityCount(
			ctx,
			sqlc.AdminActivityCountParams{
				KindFilter:       options.Kind,
				StateFilter:      options.State,
				Search:           options.Search,
				StateMatches:     states,
				KindMatches:      kinds,
				AmountMatches:    string(amounts),
				ReferenceMatches: references,
			},
		)
		if err != nil {
			return out, err
		}

		out.PageInfo = pageInfo(query, counts.Total, counts.Matched)

		rows, err := q.AdminActivityRows(
			ctx,
			sqlc.AdminActivityRowsParams{
				KindFilter:       options.Kind,
				StateFilter:      options.State,
				Search:           options.Search,
				StateMatches:     states,
				KindMatches:      kinds,
				AmountMatches:    string(amounts),
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

		out.Items = make([]application.ActivityListItem, 0, len(rows))
		for _, row := range rows {
			resource, e := decodeAcceptedResource(row.Snapshot, "activity")
			if e != nil {
				return out, e
			}

			state, e := activityState(
				sqlc.Execution{
					Status:             row.Status,
					ReviewReasons:      row.ReviewReasons,
					SelectionAuthority: row.SelectionAuthority,
				},
			)
			if e != nil {
				return out, e
			}

			out.Items = append(
				out.Items,
				application.ActivityListItem{
					Resource:       resource,
					State:          state,
					InstrumentName: row.InstrumentName,
					Reference: application.ReferenceMatch{
						Status:    application.ReferenceStatus(row.ReferenceStatus),
						Authority: application.SelectionAuthority(row.ReferenceAuthority),
					},
				},
			)
		}

		return out, nil
	})
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
