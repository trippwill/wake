// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"go.trippwill.dev/wake/internal/host"
	"go.trippwill.dev/wake/internal/persistence/sqlite/sqlc"
	"go.trippwill.dev/wake/provider"
)

// A reserved host version wraps the provider continuation only while a routine
// poll is scheduled. The provider never sees this envelope.
const pollCheckpointVersion = "wake:poll-checkpoint/1"

// Equal-width UTC timestamps sort chronologically without SQLite date rounding.
const pollDeadlineFormat = "2006-01-02T15:04:05.000000000Z"

type pollCheckpoint struct {
	NotBefore string    `json:"not_before"`
	Version   string    `json:"version"`
	Payload   []byte    `json:"payload"`
	CycleAt   time.Time `json:"cycle_at,omitzero"`
}

func encodeCheckpointState(key host.WorkKey, step provider.Step, cycleAt time.Time) (string, []byte, error) {
	state := step.State()
	if key.Operation != "acquire" || !step.PollCompleted() {
		return state.Version(), state.Payload(), nil
	}

	body, err := json.Marshal(
		pollCheckpoint{
			NotBefore: step.PollNotBefore().UTC().Format(pollDeadlineFormat),
			Version:   state.Version(),
			Payload:   state.Payload(),
			CycleAt:   cycleAt.UTC(),
		},
	)

	return pollCheckpointVersion, body, err
}

func decodeCheckpointState(version string, payload []byte) (provider.State, error) {
	if version == pollCheckpointVersion {
		checkpoint, _, err := readPollCheckpoint(payload)
		if err != nil {
			return provider.State{}, err
		}

		version, payload = checkpoint.Version, checkpoint.Payload
	}

	if version == "" {
		return provider.State{}, nil
	}

	state, err := provider.NewState(version, payload)
	if err != nil {
		return provider.State{}, ErrCorrupt
	}

	return state, nil
}

func readPollCheckpoint(payload []byte) (pollCheckpoint, time.Time, error) {
	var checkpoint pollCheckpoint
	if json.Unmarshal(payload, &checkpoint) != nil ||
		(checkpoint.Version == "" && len(checkpoint.Payload) != 0) {
		return pollCheckpoint{}, time.Time{}, ErrCorrupt
	}

	floor, err := time.Parse(pollDeadlineFormat, checkpoint.NotBefore)
	if err != nil {
		return pollCheckpoint{}, time.Time{}, ErrCorrupt
	}

	return checkpoint, floor, nil
}

func reschedulePoll(
	ctx context.Context,
	q *sqlc.Queries,
	connection, stream string,
	interval time.Duration,
) error {
	payload, err := q.QueuedPoll(ctx, sqlc.QueuedPollParams{
		ConnectionID: connection, Stream: stream, StateVersion: pollCheckpointVersion,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}

	if err != nil {
		return err
	}

	checkpoint, floor, err := readPollCheckpoint(payload)
	if err != nil || checkpoint.CycleAt.IsZero() {
		return err
	}

	next := checkpoint.CycleAt.Add(interval)
	if floor.After(next) {
		next = floor
	}

	return q.SetPollDeadline(ctx, sqlc.SetPollDeadlineParams{
		ConnectionID: connection, Stream: stream, Next: next.UTC().Format(time.RFC3339Nano),
	})
}

func (s *Store) RefreshPolls(ctx context.Context, now time.Time) (int64, error) {
	return s.queries.RefreshPolls(ctx, sqlc.RefreshPollsParams{
		Now: now.UTC().Format(pollDeadlineFormat), PollVersion: pollCheckpointVersion,
	})
}
