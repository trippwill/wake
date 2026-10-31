// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"slices"

	"go.trippwill.dev/wake/internal/persistence/sqlite/sqlc"
)

// claimData uses the same typed wire representation as retained evidence and
// decisions. Only the structural locator belongs to an appearance rather than
// the claim; financial precision, unknowns and qualified references remain exact.
func claimData(kind string, data []byte, location string) ([]byte, error) {
	return claimWire(kind, data, location, false)
}

func claimKeyData(kind string, data []byte) ([]byte, error) { return claimWire(kind, data, "", true) }

func claimWire(kind string, data []byte, location string, orderSets bool) ([]byte, error) {
	var value any
	switch kind {
	case "trade":
		var d tradeData
		if err := json.Unmarshal(data, &d); err != nil {
			return nil, err
		}

		d.Location = location
		if orderSets {
			canonicalTrade(&d)
		}

		value = d
	case "lifecycle":
		var d lifecycleData
		if err := json.Unmarshal(data, &d); err != nil {
			return nil, err
		}

		d.Trade.Location = location
		if orderSets {
			canonicalTrade(&d.Trade)
		}

		value = d
	case "conversion":
		var d conversionData
		if err := json.Unmarshal(data, &d); err != nil {
			return nil, err
		}

		d.Location = location
		if orderSets {
			orderClaimSet(d.EventAliases)
			orderClaimSet(d.Charges)
		}

		value = d
	case "correction":
		var d correctionData
		if err := json.Unmarshal(data, &d); err != nil {
			return nil, err
		}

		d.Location = location
		value = d
	case "instrument":
		var d instrumentData
		if err := json.Unmarshal(data, &d); err != nil {
			return nil, err
		}

		if orderSets {
			canonicalInstrument(&d)
		}

		value = d
	case "rejected":
		return data, nil
	default:
		return nil, ErrCorrupt
	}

	return json.Marshal(value)
}

func materializeRecords(rows []sqlc.RecordValue, err error) ([]sqlc.RecordValue, error) {
	if err != nil {
		return nil, err
	}

	for i := range rows {
		rows[i].Data, err = claimData(rows[i].Kind, rows[i].Data, rows[i].Location)
		if err != nil {
			return nil, ErrCorrupt
		}
	}

	return rows, nil
}

func recordValues(ctx context.Context, q *sqlc.Queries, id, version string) ([]sqlc.RecordValue, error) {
	return materializeRecords(q.Records(ctx, sqlc.RecordsParams{ArtifactID: id, Version: version}))
}

func activityInputs(ctx context.Context, q *sqlc.Queries, id string) ([]sqlc.RecordValue, error) {
	return materializeRecords(q.ActivityInputs(ctx, sql.NullString{String: id, Valid: true}))
}

// Set ordering is structural; entries and their multiplicity remain intact.
func orderClaimSet[T any](values []T) {
	slices.SortStableFunc(values, func(a, b T) int {
		left, _ := json.Marshal(a)
		right, _ := json.Marshal(b)

		return bytes.Compare(left, right)
	})
}

func canonicalTrade(d *tradeData) {
	orderClaimSet(d.EventAliases)
	orderClaimSet(d.Relationships)
	orderClaimSet(d.Charges)
	canonicalInstrument(&d.Instrument)
}

func canonicalInstrument(d *instrumentData) {
	orderClaimSet(d.Identifiers)

	if d.Option != nil {
		orderClaimSet(d.Option.Deliverables)

		if d.Option.Underlier != nil {
			canonicalInstrument(d.Option.Underlier)
		}
	}
}
