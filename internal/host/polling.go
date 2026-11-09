// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package host

import (
	"context"
	"fmt"
	"time"

	"go.trippwill.dev/wake/provider"
)

const ErrPollInterval Error = "invalid polling interval"

// CheckPollingDefaults checks launch defaults against the registered stream policies.
func CheckPollingDefaults(defaults map[string]time.Duration, registrations []provider.Registration) error {
	policies := map[string]provider.Polling{}
	for _, r := range registrations {
		if r.Acquirer() == nil {
			continue
		}

		for _, stream := range r.Descriptor().Streams() {
			policies[r.Descriptor().ID()+":"+stream.ID()] = stream.Polling()
		}
	}

	for key, interval := range defaults {
		if !policies[key].Allows(interval) {
			return fmt.Errorf("%w for %q", ErrPollInterval, key)
		}
	}

	return nil
}

type PollSetting struct {
	Stream                     string
	Override, Default, Minimum time.Duration
}

func (s PollSetting) Effective() time.Duration {
	if s.Override != 0 {
		return s.Override
	}

	return s.Default
}

// PollSettings reports defaults and overrides for a connection's recurring inputs.
func (h *Host) PollSettings(id string, overrides map[string]time.Duration) []PollSetting {
	r := h.registrations[id]
	if r.Acquirer() == nil {
		return nil
	}

	var settings []PollSetting
	for _, stream := range r.Descriptor().Streams() {
		if stream.Polling().Default() != 0 {
			override := overrides[stream.ID()]
			if !stream.Polling().Allows(override) {
				override = 0
			}

			settings = append(settings, PollSetting{
				Stream: stream.ID(), Override: override,
				Default: h.pollInterval(id, stream, 0), Minimum: stream.Polling().Minimum(),
			})
		}
	}

	return settings
}

func (h *Host) pollInterval(id string, stream provider.Stream, override time.Duration) time.Duration {
	if stream.Polling().Allows(override) {
		return override
	}

	if interval, ok := h.pollDefaults[id+":"+stream.ID()]; ok {
		return interval
	}

	return stream.Polling().Default()
}

// UpdatePolling patches one interval under the same admission as acquisition
// and settings edits. An empty value restores the default.
func (h *Host) UpdatePolling(ctx context.Context, id, stream, value string) error {
	work, finish, err := h.admit(ctx, id)
	if err != nil {
		return err
	}
	defer finish()

	connection, err := h.store.Connection(work, id)
	if err != nil {
		return ErrStorage
	}

	for _, setting := range h.PollSettings(connection.Provider, connection.PollOverrides) {
		if setting.Stream != stream {
			continue
		}

		if value == "" {
			delete(connection.PollOverrides, stream)
		} else {
			interval, err := provider.ParsePollInterval(value)
			if err != nil || interval < setting.Minimum {
				return ErrPollInterval
			}

			if connection.PollOverrides == nil {
				connection.PollOverrides = make(map[string]time.Duration)
			}

			connection.PollOverrides[stream] = interval
		}

		if err := h.store.SavePolling(work, connection); err != nil {
			return ErrStorage
		}

		return nil
	}

	return ErrPollInterval
}
