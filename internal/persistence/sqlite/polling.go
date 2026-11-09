// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"context"
	"encoding/json"
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
	NotBefore string `json:"not_before"`
	Version   string `json:"version"`
	Payload   []byte `json:"payload"`
}

func encodeCheckpointState(key host.WorkKey, step provider.Step) (string, []byte, error) {
	state := step.State()
	if key.Operation != "acquire" || !step.PollCompleted() {
		return state.Version(), state.Payload(), nil
	}

	body, err := json.Marshal(
		pollCheckpoint{step.PollNotBefore().UTC().Format(pollDeadlineFormat), state.Version(), state.Payload()},
	)

	return pollCheckpointVersion, body, err
}

func decodeCheckpointState(version string, payload []byte) (provider.State, error) {
	if version == pollCheckpointVersion {
		var checkpoint pollCheckpoint
		if json.Unmarshal(payload, &checkpoint) != nil ||
			(checkpoint.Version == "" && len(checkpoint.Payload) != 0) {
			return provider.State{}, ErrCorrupt
		}

		if _, err := time.Parse(pollDeadlineFormat, checkpoint.NotBefore); err != nil {
			return provider.State{}, ErrCorrupt
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

func (s *Store) RefreshPolls(ctx context.Context, now time.Time) (int64, error) {
	return s.queries.RefreshPolls(ctx, sqlc.RefreshPollsParams{
		Now: now.UTC().Format(pollDeadlineFormat), PollVersion: pollCheckpointVersion,
	})
}
