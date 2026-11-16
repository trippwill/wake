// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package application

import (
	"context"
	"time"

	"go.trippwill.dev/wake/internal/application/calculation"
)

const HistoryBatchSize = 50

type HistoryProgress struct {
	Phase, Cursor string
	Processed     int
	Finished      bool
}

type HistoryActivity struct {
	ID    string
	State calculation.ActivityState
}

type HistoryStore interface {
	UpdateHistory(context.Context, func(HistoryWriter, HistoryProgress) (HistoryProgress, error)) (bool, error)
	HistoryRetryAt() time.Time
	SetHistoryRetry(context.Context, time.Time, error)
}

type HistoryWriter interface {
	HistoryActivities(context.Context, string, int) ([]HistoryActivity, error)
	HistoryInstruments(context.Context, string, int) ([]string, error)
	RebuildActivity(context.Context, string, calculation.ActivityState) error
	RebuildInstrument(context.Context, string) error
	RebuildPositions(context.Context, int) (int, error)
}

// RunHistoryUpdate retries local rebuild work from the last committed checkpoint.
func RunHistoryUpdate(ctx context.Context, store HistoryStore, now time.Time) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}

	if now.Before(store.HistoryRetryAt()) {
		return false, nil
	}

	changed, err := store.UpdateHistory(ctx, rebuildHistoryBatch(ctx))
	if err == nil {
		store.SetHistoryRetry(ctx, time.Time{}, nil)
		return changed, nil
	}

	if ctx.Err() != nil {
		return changed, err
	}

	store.SetHistoryRetry(ctx, now.Add(time.Minute).UTC(), err)

	return true, nil
}

func rebuildHistoryBatch(ctx context.Context) func(HistoryWriter, HistoryProgress) (HistoryProgress, error) {
	return func(writer HistoryWriter, progress HistoryProgress) (HistoryProgress, error) {
		switch progress.Phase {
		case "activity":
			rows, err := writer.HistoryActivities(ctx, progress.Cursor, HistoryBatchSize)
			if err != nil {
				return progress, err
			}

			for _, row := range rows {
				if err := writer.RebuildActivity(ctx, row.ID, row.State.SourceCorrectionState()); err != nil {
					return progress, err
				}

				progress.Cursor = row.ID
			}

			progress.Processed = len(rows)
			if progress.Processed < HistoryBatchSize {
				progress.Phase, progress.Cursor = "instruments", ""
			}
		case "instruments":
			ids, err := writer.HistoryInstruments(ctx, progress.Cursor, HistoryBatchSize)
			if err != nil {
				return progress, err
			}

			for _, id := range ids {
				if err := writer.RebuildInstrument(ctx, id); err != nil {
					return progress, err
				}

				progress.Cursor = id
			}

			progress.Processed = len(ids)
			if progress.Processed < HistoryBatchSize {
				progress.Phase, progress.Cursor = "positions", ""
			}
		case "positions":
			count, err := writer.RebuildPositions(ctx, HistoryBatchSize)
			if err != nil {
				return progress, err
			}

			progress.Processed = count
			progress.Finished = count == 0
		default:
			return progress, ErrInvalidHistoryPhase
		}

		return progress, nil
	}
}

const ErrInvalidHistoryPhase calculation.Error = "invalid retained history update phase"
