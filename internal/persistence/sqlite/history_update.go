// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"context"
	"database/sql"
	"time"

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/application/calculation"
	"go.trippwill.dev/wake/internal/persistence/sqlite/sqlc"
)

const ErrHistoryVersion Error = "unsupported history rule version; resume with the matching Wake build or restore a compatible backup"

func (s *Store) HistoryUpdating() bool { return s.historyUpdating.Load() }

func (s *Store) prepareHistoryUpdate(ctx context.Context, q *sqlc.Queries, fresh bool) error {
	version, err := ruleVersion(s.historyRule)
	if err != nil {
		return err
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

func (s *Store) HistoryRetryAt() time.Time {
	if retry := s.historyRetry.Load(); retry != nil {
		return *retry
	}

	return time.Time{}
}

func (s *Store) SetHistoryRetry(ctx context.Context, retry time.Time, cause error) {
	if cause == nil {
		s.historyRetry.Store(nil)
		return
	}

	s.historyRetry.Store(&retry)
	s.logger.WarnContext(ctx, "retained history update will retry", "event", "history_update_retry",
		"code", storageCode(cause), "next_eligible_at", retry)
}

func (s *Store) UpdateHistory(
	ctx context.Context,
	fn func(application.HistoryWriter, application.HistoryProgress) (application.HistoryProgress, error),
) (bool, error) {
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

	writer := &historyWriter{tx: tx, q: q, rule: s.historyRule}

	progress, err := fn(writer, application.HistoryProgress{Phase: state.Phase, Cursor: state.Cursor})
	if err != nil {
		return false, err
	}

	if progress.Finished {
		err = q.FinishHistoryUpdate(ctx)
	} else {
		err = q.AdvanceHistoryUpdate(ctx, sqlc.AdvanceHistoryUpdateParams{
			Phase: progress.Phase, Cursor: progress.Cursor, Processed: int64(progress.Processed),
		})
	}

	if err != nil {
		return false, err
	}

	if err := s.commit(ctx, tx); err != nil {
		return false, err
	}

	if progress.Finished {
		s.historyUpdating.Store(false)
	}

	return true, nil
}

type historyWriter struct {
	tx         *sql.Tx
	q          *sqlc.Queries
	rule       calculation.FinancialRule
	activities map[string]sqlc.Execution
}

func (w *historyWriter) HistoryActivities(
	ctx context.Context,
	cursor string,
	size int,
) ([]application.HistoryActivity, error) {
	rows, err := w.q.HistoryActivities(
		ctx,
		sqlc.HistoryActivitiesParams{Cursor: cursor, BatchSize: int64(size)},
	)
	if err != nil {
		return nil, err
	}

	w.activities = make(map[string]sqlc.Execution, len(rows))
	activities := make([]application.HistoryActivity, 0, len(rows))
	for _, row := range rows {
		state, err := activityState(row)
		if err != nil {
			return nil, err
		}

		activities = append(activities, application.HistoryActivity{ID: row.ID, State: state})
		w.activities[row.ID] = row
	}

	return activities, nil
}

func (w *historyWriter) HistoryInstruments(ctx context.Context, cursor string, size int) ([]string, error) {
	return w.q.HistoryInstruments(ctx, sqlc.HistoryInstrumentsParams{Cursor: cursor, BatchSize: int64(size)})
}

func (w *historyWriter) RebuildActivity(
	ctx context.Context,
	id string,
	state calculation.ActivityState,
) error {
	return projectActivity(ctx, w.q, w.activities[id], state, w.rule)
}

func (w *historyWriter) RebuildInstrument(ctx context.Context, id string) error {
	return projectInstrument(ctx, w.q, id, w.rule)
}

func (w *historyWriter) RebuildPositions(ctx context.Context, size int) (int, error) {
	n, _, err := refreshPositions(ctx, w.tx, w.q, size)
	return n, err
}
