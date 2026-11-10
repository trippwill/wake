// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package provider

import "time"

// MaxPollInterval bounds operator-configured recurring acquisition intervals.
const MaxPollInterval = 7 * 24 * time.Hour

// Polling describes a stream's recurring acquisition policy. A zero value
// describes a stream that does not offer configurable polling.
type Polling struct {
	defaultInterval time.Duration
	minimum         time.Duration
}

func (s Stream) WithPolling(defaultInterval, minimum time.Duration) (Stream, error) {
	if s.ID() == "" || minimum < time.Minute || defaultInterval < minimum || defaultInterval > MaxPollInterval {
		return Stream{}, ErrInvalid
	}

	s.polling = Polling{defaultInterval: defaultInterval, minimum: minimum}

	return s, nil
}

func (s Stream) Polling() Polling        { return s.polling }
func (p Polling) Default() time.Duration { return p.defaultInterval }
func (p Polling) Minimum() time.Duration { return p.minimum }

func (p Polling) Allows(interval time.Duration) bool {
	return p.defaultInterval != 0 && interval >= p.minimum && interval <= MaxPollInterval
}

// ParsePollInterval parses the common launch and connection interval syntax.
func ParsePollInterval(value string) (time.Duration, error) {
	if len(value) > 32 {
		return 0, ErrInvalid
	}

	interval, err := time.ParseDuration(value)
	if err != nil || interval < time.Minute || interval > MaxPollInterval {
		return 0, ErrInvalid
	}

	return interval, nil
}
