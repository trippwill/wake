// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package financial_test

import (
	"errors"
	"testing"
	"time"

	"go.trippwill.dev/wake/financial"
)

func TestTimestampRetainsPrecisionAndNormalizesOnlyOffset(t *testing.T) {
	for _, fixture := range []struct {
		text, want string
		digits     int
	}{
		{"2026-10-23T14:32:21+01:00", "2026-10-23T13:32:21Z", 0},
		{"2026-10-23T14:32:21.0-04:00", "2026-10-23T18:32:21.0Z", 1},
		{"2026-10-23T14:32:21.1200Z", "2026-10-23T14:32:21.1200Z", 4},
		{"2026-10-23T14:32:21.000000000Z", "2026-10-23T14:32:21.000000000Z", 9},
	} {
		t.Run(fixture.text, func(t *testing.T) {
			got, err := financial.ParseTimestamp(fixture.text)
			if err != nil || got.String() != fixture.want || got.FractionalDigits() != fixture.digits ||
				got.Time().Location() != time.UTC {
				t.Fatal("lost source precision", got, err)
			}

			roundTrip, err := financial.ParseTimestamp(got.String())
			if err != nil || !roundTrip.Equal(got) {
				t.Fatal("precision did not round trip", roundTrip, err)
			}
		})
	}

	for _, text := range []string{"", "2026-10-23", "2026-10-23T14:32:21", "2026-10-23T14:32:21.1234567890Z", "2026-10-23T14:32:21.Z", "2026-10-23T14:32:21,1Z", "2026-10-23T14:32:21+24:00", "2026-10-23T14:32:21+01:60", "2026-10-23T1:32:21Z", "2026-02-30T14:32:21Z", "2026-10-23T14:32:60Z", "2026-10-23T14:32:21+00:00suffix", "0001-01-01T00:00:00+01:00", "9999-12-31T23:59:59-01:00"} {
		if _, err := financial.ParseTimestamp(text); !errors.Is(err, financial.ErrInvalidTimestamp) {
			t.Fatal("invalid or overprecise time accepted", text, err)
		}
	}

	at := time.Date(2026, 10, 23, 14, 32, 21, 123456789, time.UTC)
	for _, digits := range []int{-1, 0, 3, 8, 10} {
		if _, err := financial.NewTimestamp(at, digits); !errors.Is(err, financial.ErrInvalidTimestamp) {
			t.Fatal("constructor rounded a time", digits, err)
		}
	}

	if _, err := financial.NewTimestamp(time.Time{}, 0); !errors.Is(err, financial.ErrInvalidTimestamp) {
		t.Fatal("absence became a timestamp", err)
	}
}

func TestTimestampBucketsCompareReportedResolution(t *testing.T) {
	second, _ := financial.ParseTimestamp("2026-10-23T14:32:21Z")
	millis, _ := financial.ParseTimestamp("2026-10-23T14:32:21.123Z")

	precise, _ := financial.ParseTimestamp("2026-10-23T14:32:21.123999999Z")
	adjacent, _ := financial.ParseTimestamp("2026-10-23T14:32:21.124Z")
	boundary, _ := financial.ParseTimestamp("2026-10-23T14:32:22.000Z")
	if !second.Contains(millis) || !millis.Contains(precise) || millis.Contains(adjacent) ||
		second.Contains(boundary) ||
		precise.Contains(millis) ||
		(financial.Timestamp{}).Contains(second) ||
		second.Contains(financial.Timestamp{}) {
		t.Fatal("bucket containment lost precision or included a boundary")
	}
}
