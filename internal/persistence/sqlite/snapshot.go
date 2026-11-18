// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0
package persistence

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"

	"go.trippwill.dev/wake/internal/application"
)

const (
	retainedHistoryVersions = 257 // Current publication plus up to 256 usable bases.
	retainedHistoryChanges  = 50_000
	maxDeltaKeys            = 4096
	maxDeltaBytes           = 4 << 20
)

func (state publicationState) history() application.HistoryPublication {
	token, _ := json.Marshal(state)
	digest := sha256.Sum256(token)

	return application.HistoryPublication{BaselineID: state.Warehouse, Version: hex.EncodeToString(digest[:])}
}

func advancePublication(ctx context.Context, tx *sql.Tx, keys []application.ResourceKey) error {
	state, err := publishingState(ctx, tx)
	if err != nil {
		return err
	}

	if _, err = tx.ExecContext(
		ctx,
		publishingStatement("RecordBase"),
		state.Version,
		state.history().Version,
	); err != nil {
		return err
	}

	if _, err = tx.ExecContext(ctx, publishingStatement("Advance")); err != nil {
		return err
	}

	state.Version++
	if _, err = tx.ExecContext(
		ctx,
		publishingStatement("RecordVersion"),
		state.Version,
		state.history().Version,
		len(keys),
	); err != nil {
		return err
	}

	// A single large publication breaks continuity for every older base.
	if len(keys) <= retainedHistoryChanges {
		data, e := json.Marshal(keys)
		if e != nil {
			return e
		}

		if _, err = tx.ExecContext(
			ctx,
			publishingStatement("RecordChanges"),
			state.Version,
			string(data),
		); err != nil {
			return err
		}
	}

	if _, err = tx.ExecContext(
		ctx,
		publishingStatement("PruneVersions"),
		retainedHistoryVersions,
		retainedHistoryChanges,
	); err != nil {
		return err
	}

	_, err = tx.ExecContext(ctx, publishingStatement("PruneBaseChanges"))

	return err
}

func (s *Store) HistoryPublication(ctx context.Context) (application.HistoryPublication, error) {
	state, err := publishingState(ctx, s.db)
	if err != nil {
		return application.HistoryPublication{}, err
	}

	return state.history(), nil
}

func (s *Store) Snapshot(
	ctx context.Context,
	base application.HistoryPublication,
) (application.SnapshotRead, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return application.SnapshotRead{}, err
	}

	defer func() { _ = tx.Rollback() }()

	state, err := publishingState(ctx, tx)
	if err != nil {
		return application.SnapshotRead{}, err
	}

	current := state.history()

	var out application.SnapshotRead
	if base.BaselineID == current.BaselineID {
		out.Delta, err = readSnapshotDelta(ctx, tx, base, current, state.Version)
		if err != nil {
			return application.SnapshotRead{}, err
		}
	}

	if out.Delta == nil {
		out.Snapshot = application.FinancialSnapshot{
			Mode: "full", BaselineID: current.BaselineID, Version: current.Version,
			Resources: []application.CanonicalResource{},
		}

		rows, e := tx.QueryContext(ctx, publishingStatement("Snapshot"))
		if e != nil {
			return application.SnapshotRead{}, e
		}

		defer func() { _ = rows.Close() }()

		for rows.Next() {
			key, data, e := readSnapshotRow(rows)
			if e != nil {
				return application.SnapshotRead{}, e
			}

			resource, e := snapshotResource(key, data)
			if e != nil {
				return application.SnapshotRead{}, e
			}

			out.Snapshot.Resources = append(out.Snapshot.Resources, resource)
		}

		if e = rows.Err(); e != nil {
			return application.SnapshotRead{}, e
		}
	}

	if err = tx.Commit(); err != nil {
		return application.SnapshotRead{}, err
	}

	return out, nil
}

func readSnapshotDelta(
	ctx context.Context, tx *sql.Tx, base, current application.HistoryPublication, target int64,
) (*application.SnapshotDelta, error) {
	delta := &application.SnapshotDelta{
		Mode: "delta", BaselineID: current.BaselineID, From: base.Version, Version: current.Version,
		Upserts: []application.CanonicalResource{}, Removals: []application.ResourceKey{},
	}
	if base.Version == current.Version {
		return delta, nil
	}

	var from int64

	err := tx.QueryRowContext(ctx, publishingStatement("SnapshotBase"), base.Version).Scan(&from)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}

	if err != nil {
		return nil, err
	}

	rows, err := tx.QueryContext(ctx, publishingStatement("SnapshotChanges"), from, target, maxDeltaKeys+1)
	if err != nil {
		return nil, err
	}

	defer func() { _ = rows.Close() }()

	// Count the envelope and leave room for array separators and the final newline.
	envelope, err := json.Marshal(delta)
	if err != nil {
		return nil, err
	}

	size := len(envelope) + 1
	for rows.Next() {
		if len(delta.Upserts)+len(delta.Removals) == maxDeltaKeys {
			return nil, nil
		}

		key, data, e := readSnapshotRow(rows)
		if e != nil {
			return nil, e
		}

		if data == nil {
			encoded, e := json.Marshal(key)
			if e != nil {
				return nil, e
			}

			size += len(encoded) + 1

			delta.Removals = append(delta.Removals, key)
		} else {
			size += len(data) + 1
			if size > maxDeltaBytes {
				return nil, nil
			}

			resource, e := snapshotResource(key, data)
			if e != nil {
				return nil, e
			}

			delta.Upserts = append(delta.Upserts, resource)
		}

		if size > maxDeltaBytes {
			return nil, nil
		}
	}

	if err = rows.Err(); err != nil {
		return nil, err
	}

	return delta, nil
}

func readSnapshotRow(rows *sql.Rows) (application.ResourceKey, []byte, error) {
	var (
		key  application.ResourceKey
		data []byte
	)

	err := rows.Scan(&key.Kind, &key.ID, &data)

	return key, data, err
}

func snapshotResource(key application.ResourceKey, data []byte) (application.CanonicalResource, error) {
	var resource application.CanonicalResource
	if json.Unmarshal(data, &resource) != nil || resource.Kind != key.Kind ||
		resource.ID != key.ID || resource.Revision < 1 {
		return application.CanonicalResource{}, ErrCorrupt
	}

	return resource, nil
}
