// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"strconv"

	"go.trippwill.dev/wake/internal/application"
)

//go:embed admin_positions.sql
var adminPositionsSQL string

func (s *Store) PositionsList(
	ctx context.Context,
	query application.ListQuery,
) (application.PositionsList, error) {
	out := application.PositionsList{PageInfo: pageInfo(query, 0, 0)}
	options := query.Options()
	if query.Scope() != application.PositionsScope || options.Page < 1 || options.PageSize < 1 {
		return out, application.ErrInvalid
	}

	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, err
	}

	defer func() { _ = tx.Rollback() }()

	account, err := s.queries.WithTx(tx).Account(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return out, tx.Commit()
	}

	if err != nil {
		return out, err
	}

	out.Account, err = accountValue(account)
	if err != nil {
		return out, err
	}

	state, err := publishingState(ctx, tx)
	if err != nil {
		return out, err
	}

	rows, err := tx.QueryContext(
		ctx,
		adminPositionsSQL,
		account.ID,
		options.Search,
		options.Kind,
		options.State,
		options.Sort,
		options.Direction,
		options.PageSize,
		query.Offset(),
	)
	if err != nil {
		return out, err
	}

	defer func() { _ = rows.Close() }()

	found := false
	out.Items = []application.PositionListItem{}
	for rows.Next() {
		var (
			revision                         int64
			asOf, reason, name, ticker, kind sql.NullString
			ordinal                          sql.NullInt64
			raw                              []byte
		)
		if err = rows.Scan(
			&revision,
			&out.Status,
			&asOf,
			&reason,
			&out.Total,
			&out.Matched,
			&ordinal,
			&name,
			&ticker,
			&kind,
			&raw,
		); err != nil {
			return out, err
		}

		found = true

		out.Version = positionsVersion(state.Warehouse, account.ID, revision)
		out.Reason = known(reason.String)
		if asOf.Valid {
			out.AsOf = &application.PositionTime{}
			if json.Unmarshal([]byte(asOf.String), out.AsOf) != nil {
				return out, ErrCorrupt
			}
		}

		if !ordinal.Valid {
			continue
		}

		item := application.PositionListItem{
			Key:    strconv.FormatInt(ordinal.Int64, 10),
			Name:   name.String,
			Ticker: ticker.String,
			Kind:   kind.String,
		}
		if json.Unmarshal(raw, &item.Position) != nil || item.Position.InstrumentID == "" {
			return out, ErrCorrupt
		}

		// An ordinal alone could focus another instrument after complete replacement.
		item.Key = item.Position.InstrumentID + ":" + item.Key
		out.Items = append(out.Items, item)
	}

	err = rows.Err()
	_ = rows.Close()

	if err != nil {
		return out, err
	}

	if !found {
		return out, ErrCorrupt
	}

	return out, tx.Commit()
}
