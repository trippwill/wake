// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0
package persistence

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"slices"

	"go.trippwill.dev/wake/internal/application"
)

func (s *Store) Snapshot(ctx context.Context, known []string) (application.SnapshotRead, error) {
	out := application.SnapshotRead{}

	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, err
	}

	defer func() { _ = tx.Rollback() }()

	state, err := publishingState(ctx, tx)
	if err != nil {
		return out, err
	}

	token, _ := json.Marshal(struct {
		Warehouse string
		Version   int64
	}{state.Warehouse, state.Version})
	digest := sha256.Sum256(token)

	out.Snapshot = application.FinancialSnapshot{
		WarehouseID: state.Warehouse,
		Version:     hex.EncodeToString(digest[:]),
		Resources:   []application.CanonicalResource{},
	}
	if slices.Contains(known, out.Snapshot.Version) || slices.Contains(known, "*") {
		out.Unchanged = true
	} else {
		rows, e := tx.QueryContext(ctx, publishingStatement("Snapshot"))
		if e != nil {
			return out, e
		}

		for rows.Next() {
			var (
				kind, id string
				data     []byte
			)
			if e = rows.Scan(&kind, &id, &data); e != nil {
				_ = rows.Close()
				return out, e
			}

			var resource application.CanonicalResource
			if json.Unmarshal(data, &resource) != nil || resource.Kind != kind || resource.ID != id ||
				resource.Revision < 1 {
				_ = rows.Close()
				return out, ErrCorrupt
			}

			out.Snapshot.Resources = append(out.Snapshot.Resources, resource)
		}

		e = rows.Err()
		_ = rows.Close()

		if e != nil {
			return out, e
		}
	}

	if err = tx.Commit(); err != nil {
		return application.SnapshotRead{}, err
	}

	return out, nil
}
