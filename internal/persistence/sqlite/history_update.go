// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/persistence/sqlite/sqlc"
)

const (
	historyBatchSize        = 50
	ErrHistoryVersion Error = "unsupported history rule version; resume with the matching Wake build or restore a compatible backup"
)

func (s *Store) HistoryUpdating() bool { return s.historyUpdating.Load() }

func (s *Store) prepareHistoryUpdate(ctx context.Context, q *sqlc.Queries, fresh bool) error {
	_, suffix, _ := strings.Cut(string(s.historyRule), "/")

	version, err := strconv.ParseInt(suffix, 10, 64)
	if err != nil || version < 1 {
		return ErrHistoryVersion
	}

	if fresh {
		return q.InitializeHistoryUpdate(ctx, version)
	}

	state, err := q.HistoryUpdate(ctx)
	if err != nil {
		return err
	}

	if state.Applied > version || state.Target != 0 && state.Target != version {
		return ErrHistoryVersion
	}

	if state.Applied == version {
		return nil
	}

	if state.Target == 0 {
		if err = q.StartHistoryUpdate(ctx, version); err != nil {
			return err
		}
	}

	s.historyUpdating.Store(true)

	return nil
}

func (s *Store) RunHistoryUpdate(ctx context.Context, now time.Time) (bool, error) {
	changed, err := s.historyBatch(ctx, now)
	if err == nil || ctx.Err() != nil {
		return changed, err
	}

	retry := now.Add(time.Minute).UTC()
	if saveErr := s.queries.FailHistoryUpdate(ctx, retry.Format(time.RFC3339Nano)); saveErr != nil {
		return false, errors.Join(err, saveErr)
	}

	s.logger.WarnContext(ctx, "retained history update will retry", "event", "history_update_retry",
		"code", storageCode(err), "next_eligible_at", retry)

	return true, nil
}

func (s *Store) historyBatch(ctx context.Context, now time.Time) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()

	q := s.queries.WithTx(tx)

	state, err := q.HistoryUpdate(ctx)
	if err != nil || state.Target == 0 {
		return false, err
	}

	if state.RetryAt != "" {
		retry, e := time.Parse(time.RFC3339Nano, state.RetryAt)
		if e != nil {
			return false, ErrCorrupt
		}

		if now.Before(retry) {
			return false, nil
		}
	}

	phase, cursor := state.Phase, state.Cursor

	var processed int
	switch phase {
	case "activity":
		rows, e := q.HistoryActivities(ctx, cursor)
		if e != nil {
			return false, e
		}

		for _, row := range rows {
			// Claim conflicts are recalculated. The admitted source-correction state remains authoritative.
			prior, e := activityState(row)
			if e != nil {
				return false, e
			}

			sourceState, e := application.NewActivityState(
				prior.Lifecycle(),
				prior.ReviewReasons()&application.AmbiguousCorrection,
				application.AutomaticSelection,
			)
			if e != nil {
				return false, e
			}

			if e = projectActivity(ctx, q, row, sourceState, s.historyRule); e != nil {
				return false, e
			}

			cursor = row.ID
		}

		processed = len(rows)
		if processed < historyBatchSize {
			phase, cursor = "instruments", ""
		}
	case "instruments":
		ids, e := q.HistoryInstruments(ctx, cursor)
		if e != nil {
			return false, e
		}

		for _, id := range ids {
			if e = projectInstrument(ctx, q, id, s.historyRule); e != nil {
				return false, e
			}

			cursor = id
		}

		processed = len(ids)
		if processed < historyBatchSize {
			phase, cursor = "positions", ""
		}
	case "positions":
		processed, _, err = refreshPositions(ctx, tx, q, historyBatchSize)
		if err != nil {
			return false, err
		}
	default:
		return false, ErrCorrupt
	}

	finished := state.Phase == "positions" && processed == 0
	if finished {
		err = q.FinishHistoryUpdate(ctx)
	} else {
		err = q.AdvanceHistoryUpdate(ctx, sqlc.AdvanceHistoryUpdateParams{
			Phase: phase, Cursor: cursor, Processed: int64(processed),
		})
	}

	if err != nil {
		return false, err
	}

	if err = s.commit(ctx, tx); err != nil {
		return false, err
	}

	if finished {
		s.historyUpdating.Store(false)
	}

	return true, nil
}
