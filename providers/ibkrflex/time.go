// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package ibkrflex

import (
	"strings"
	"time"

	"go.trippwill.dev/wake/financial"
	"go.trippwill.dev/wake/provider"
)

func parseDate(value string) (financial.Date, error) {
	if len(value) == 8 && digits(value) {
		value = value[:4] + "-" + value[4:6] + "-" + value[6:]
	}

	return financial.ParseDate(value)
}

func parseReportedTimestamp(value string) (financial.Timestamp, error) {
	if strings.Contains(value, ",") {
		return financial.Timestamp{}, provider.ErrMalformed
	}

	if len(value) > 10 && value[10] == 'T' {
		return financial.ParseTimestamp(value)
	}

	at, err := parseInstant(value)
	if err != nil {
		return financial.Timestamp{}, err
	}

	wall := strings.Fields(value)[0]
	digits := 0
	if _, fraction, ok := strings.Cut(wall, "."); ok {
		digits = len(fraction)
	}

	return financial.NewTimestamp(at, digits)
}

func parseInstant(value string) (time.Time, error) {
	parts := strings.Fields(value)
	if len(parts) != 2 {
		return time.Time{}, provider.ErrMalformed
	}

	wall := parts[0]
	if len(wall) >= 15 && wall[8] == ';' && digits(wall[:8]) {
		wall = wall[:4] + "-" + wall[4:6] + "-" + wall[6:8] + wall[8:]
	}

	if len(wall) >= 17 && wall[10] == ';' && digits(wall[11:17]) && (len(wall) == 17 || wall[17] == '.') {
		wall = wall[:13] + ":" + wall[13:15] + ":" + wall[15:]
	}

	local, err := time.Parse("2006-01-02;15:04:05", wall)
	if err != nil {
		return time.Time{}, provider.ErrMalformed
	}

	offset := 0
	switch parts[1] {
	case "EST":
		offset = -5 * 3600
	case "EDT":
		offset = -4 * 3600
	case "UTC", "GMT":
		return local.UTC(), nil
	default:
		return time.Time{}, provider.ErrMalformed
	}

	zone, err := time.LoadLocation("America/New_York")
	if err != nil {
		return time.Time{}, provider.ErrUnavailable
	}

	instant := local.Add(-time.Duration(offset) * time.Second)
	resolved := instant.In(zone)
	name, seconds := resolved.Zone()
	if name != parts[1] || seconds != offset ||
		resolved.Format("2006-01-02;15:04:05.999999999") != local.Format("2006-01-02;15:04:05.999999999") {
		return time.Time{}, provider.ErrMalformed
	}

	return instant.UTC(), nil
}

func first(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}

	return ""
}

// IBKR uses zero in optional native-ID fields when no referenced object exists.
// This is provider interpretation; Wake's generic references remain opaque.
func nativeValue(value string) string {
	if value == "0" {
		return ""
	}

	return value
}
