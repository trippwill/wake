// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/persistence/sqlite/sqlc"
	"go.trippwill.dev/wake/source"
)

//go:embed positions.sql
var positionsSQL string

func positionsStatement(name string) string {
	_, tail, ok := strings.Cut(positionsSQL, "-- name: "+name+"\n")
	if !ok {
		panic("unknown positions statement")
	}

	statement, _, _ := strings.Cut(tail, "-- name:")

	return statement
}

func positionInstrumentIdentity(d instrumentData) string {
	canonicalInstrument(&d)
	b, _ := json.Marshal(d)

	return string(b)
}

func calculatePositions(
	ctx context.Context,
	tx *sql.Tx,
	q *sqlc.Queries,
	account string,
) (application.PositionsView, error) {
	out := application.PositionsView{AccountID: account, Status: "unavailable", Reason: new("not_reported")}

	bindings, err := q.PositionBindings(ctx, account)
	if err != nil {
		return out, err
	}

	ids := map[string]string{}
	for _, b := range bindings {
		var d positionInstrumentData
		if json.Unmarshal(b.Data, &d) != nil {
			return out, ErrCorrupt
		}

		key := positionInstrumentIdentity(d.Instrument)
		id := b.InstrumentID
		if b.ReferenceStatus == "conflict" ||
			!application.CompatibleInstrumentKind(
				d.Instrument.Kind,
				source.InstrumentKind(b.Kind),
			) {
			id = ""
		}

		if prior, ok := ids[key]; ok && prior != id {
			ids[key] = ""
		} else if !ok {
			ids[key] = id
		}
	}

	rows, err := tx.QueryContext(ctx, positionsStatement("Inputs"), account, account, account)
	if err != nil {
		return out, err
	}
	defer func() { _ = rows.Close() }()

	candidates := []application.PositionCandidate{}
	for rows.Next() {
		var (
			raw      []byte
			location string
		)
		if err = rows.Scan(&raw, &location); err != nil {
			_ = rows.Close()
			return out, err
		}

		data, e := claimData("positions", raw, location)
		if e != nil {
			return out, e
		}

		o, e := decodePositionSet(data)
		if e != nil {
			return out, ErrCorrupt
		}

		c := application.PositionCandidate{Observation: o}
		for _, p := range o.Fields().Positions {
			c.InstrumentIDs = append(
				c.InstrumentIDs,
				ids[positionInstrumentIdentity(instrumentWire(p.Instrument()))],
			)
		}

		candidates = append(candidates, c)
	}

	err = rows.Err()
	_ = rows.Close()

	if err != nil {
		return out, err
	}

	selection, err := application.SelectPositions(ctx, candidates)
	if err != nil {
		return out, err
	}

	if !selection.Source.Date.IsZero() {
		out.AsOf = &application.PositionTime{
			Date:  selection.Source.Date.String(),
			At:    known(selection.Source.At.String()),
			Basis: selection.Source.TimeBasis,
		}
	}

	if out.AsOf != nil && out.AsOf.Basis == "" {
		out.AsOf.Basis = source.PositionBrokerEffective
	}

	out.Reason = known(selection.Reason)
	if selection.Reason != "" {
		return out, nil
	}

	out.Status = "available"
	out.Positions = []application.PublishedPosition{}
	for i, p := range selection.Source.Positions {
		side := "long"
		if !p.Quantity().Positive() {
			side = "short"
		}

		var unitCostBasis *application.PublishedUnitCostBasis
		if price := publicPrice(p.UnitCostBasis()); price != nil {
			unitCostBasis = &application.PublishedUnitCostBasis{
				PublishedMoney: *price,
				QuotationBasis: known(string(p.UnitCostBasisQuotationBasis())),
			}
		}

		out.Positions = append(
			out.Positions,
			application.PublishedPosition{
				InstrumentID:    selection.InstrumentIDs[i],
				Quantity:        application.PublishedQuantity{Value: p.Quantity().Value(), Unit: p.Unit()},
				Side:            side,
				UnitCostBasis:   unitCostBasis,
				InstrumentTerms: publicInstrumentTerms(p.Instrument()),
			},
		)
	}

	slices.SortStableFunc(out.Positions, func(a, b application.PublishedPosition) int {
		x, _ := json.Marshal(a)
		y, _ := json.Marshal(b)

		return bytes.Compare(x, y)
	})

	return out, nil
}

func refreshPositions(ctx context.Context, tx *sql.Tx, q *sqlc.Queries, limit int) (int, bool, error) {
	rows, err := tx.QueryContext(ctx, positionsStatement("Dirty"), limit)
	if err != nil {
		return 0, false, err
	}

	accounts := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			_ = rows.Close()
			return 0, false, err
		}

		accounts = append(accounts, id)
	}

	err = rows.Err()
	_ = rows.Close()

	if err != nil {
		return 0, false, err
	}

	changed := false
	for _, id := range accounts {
		view, e := calculatePositions(ctx, tx, q, id)
		if e != nil {
			return 0, false, e
		}

		data, e := json.Marshal(view)
		if e != nil {
			return 0, false, e
		}

		var (
			revision int64
			previous []byte
		)

		e = tx.QueryRowContext(ctx, positionsStatement("Current"), id).Scan(&revision, &previous)
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			return 0, false, e
		}

		if !bytes.Equal(data, previous) {
			if _, e = tx.ExecContext(ctx, positionsStatement("Save"), id, revision+1, data); e != nil {
				return 0, false, e
			}

			changed = true
		}

		if _, e = tx.ExecContext(ctx, positionsStatement("ClearDirty"), id); e != nil {
			return 0, false, e
		}
	}

	return len(accounts), changed, nil
}

func positionsVersion(warehouse, account string, revision int64) string {
	b, _ := json.Marshal(struct {
		Warehouse, Account string
		Revision           int64
	}{warehouse, account, revision})
	h := sha256.Sum256(b)

	return hex.EncodeToString(h[:])
}

func (s *Store) Positions(
	ctx context.Context,
	id string,
	condition application.SnapshotCondition,
) (application.PositionsRead, error) {
	var out application.PositionsRead

	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, err
	}

	defer func() { _ = tx.Rollback() }()

	var account string
	if err = tx.QueryRowContext(ctx, positionsStatement("Account"), id).
		Scan(&account); errors.Is(
		err,
		sql.ErrNoRows,
	) {
		return out, application.ErrNotFound
	} else if err != nil {
		return out, err
	}

	state, err := publishingState(ctx, tx)
	if err != nil {
		return out, err
	}

	var revision int64
	if err = tx.QueryRowContext(ctx, positionsStatement("Revision"), id).Scan(&revision); err != nil {
		return out, err
	}

	out.View = application.PositionsView{
		WarehouseID: state.Warehouse,
		AccountID:   id,
		Version:     positionsVersion(state.Warehouse, id, revision),
	}
	out.Unchanged = condition.Matches(out.View.Version)
	if !out.Unchanged {
		var data []byte
		if err = tx.QueryRowContext(ctx, positionsStatement("Current"), id).Scan(&revision, &data); err != nil {
			return out, err
		}

		warehouse, version := out.View.WarehouseID, out.View.Version
		if json.Unmarshal(data, &out.View) != nil || out.View.AccountID != id {
			return out, ErrCorrupt
		}

		out.View.WarehouseID, out.View.Version = warehouse, version
	}

	err = tx.Commit()

	return out, err
}

// PositionsPublications reads only bounded publication metadata, never position rows.
func (s *Store) PositionsPublications(ctx context.Context) ([]application.PositionsPublication, error) {
	rows, err := s.db.QueryContext(ctx, positionsStatement("Metadata"))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := []application.PositionsPublication{}
	for rows.Next() {
		var (
			warehouse, account string
			revision           int64
		)
		if err = rows.Scan(&warehouse, &account, &revision); err != nil {
			return nil, err
		}

		out = append(
			out,
			application.PositionsPublication{
				WarehouseID: warehouse,
				AccountID:   account,
				Version:     positionsVersion(warehouse, account, revision),
			},
		)
	}

	return out, rows.Err()
}
