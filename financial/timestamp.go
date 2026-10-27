// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package financial

import (
	"strings"
	"time"
)

const ErrInvalidTimestamp Error = "invalid reported timestamp"

// Timestamp is a source-reported UTC time bucket, with zero to nine fractional
// second digits. Precision is resolution, not clock accuracy. Providers must not
// use a rounded time as a truncated bucket. Its zero value means no reported time.
type Timestamp struct {
	at     time.Time
	digits uint8
}

// NewTimestamp rejects values that would require rounding at the stated precision.
func NewTimestamp(at time.Time, fractionalDigits int) (Timestamp, error) {
	at = at.Round(0).UTC()
	if at.IsZero() || at.Year() < 1 || at.Year() > 9999 || fractionalDigits < 0 || fractionalDigits > 9 {
		return Timestamp{}, ErrInvalidTimestamp
	}

	tick := time.Second
	for range fractionalDigits {
		tick /= 10
	}

	if time.Duration(at.Nanosecond())%tick != 0 {
		return Timestamp{}, ErrInvalidTimestamp
	}

	return Timestamp{at: at, digits: uint8(fractionalDigits)}, nil
}

// ParseTimestamp requires RFC3339 with an explicit offset and preserves trailing
// fractional zeros. More than nine fractional digits are rejected, never truncated.
func ParseTimestamp(text string) (Timestamp, error) {
	if len(text) < 20 || text[10] != 'T' || text[13] != ':' || text[16] != ':' {
		return Timestamp{}, ErrInvalidTimestamp
	}

	digits := 0
	if len(text) > 19 && text[19] == '.' {
		for i := 20; i < len(text) && text[i] >= '0' && text[i] <= '9'; i++ {
			digits++
		}

		if digits == 0 || digits > 9 {
			return Timestamp{}, ErrInvalidTimestamp
		}
	} else if strings.Contains(text, ",") {
		return Timestamp{}, ErrInvalidTimestamp
	}

	zone := text[19:]
	if digits > 0 {
		zone = text[20+digits:]
	}

	if zone != "Z" && (len(zone) != 6 || (zone[0] != '+' && zone[0] != '-') || zone[3] != ':' ||
		zone[1] < '0' || zone[1] > '2' || zone[2] < '0' || zone[2] > '9' ||
		(zone[1] == '2' && zone[2] > '3') || zone[4] < '0' || zone[4] > '5' || zone[5] < '0' || zone[5] > '9') {
		return Timestamp{}, ErrInvalidTimestamp
	}

	at, err := time.Parse(time.RFC3339Nano, text)
	if err != nil {
		return Timestamp{}, ErrInvalidTimestamp
	}

	return NewTimestamp(at, digits)
}

func (t Timestamp) IsZero() bool          { return t.at.IsZero() }
func (t Timestamp) Time() time.Time       { return t.at }
func (t Timestamp) FractionalDigits() int { return int(t.digits) }

// String retains the reported precision after offset normalization, including zeros.
func (t Timestamp) String() string {
	if t.IsZero() {
		return ""
	}

	layout := "2006-01-02T15:04:05"
	if t.digits > 0 {
		layout += "." + strings.Repeat("0", int(t.digits))
	}

	return t.at.Format(layout + "Z07:00")
}

// Contains reports whether the other bucket is wholly within this one. Adjacent
// buckets do not corroborate, and an absent time never corroborates an event.
func (t Timestamp) Contains(other Timestamp) bool {
	if t.IsZero() || other.IsZero() || other.digits < t.digits {
		return false
	}

	tick := time.Second
	for range t.digits {
		tick /= 10
	}

	return !other.at.Before(t.at) && other.at.Before(t.at.Add(tick))
}

func (t Timestamp) Equal(other Timestamp) bool {
	return t.digits == other.digits && t.at.Equal(other.at)
}
