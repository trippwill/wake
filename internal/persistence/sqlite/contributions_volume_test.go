//go:build !race

// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import "testing"

// Race tests exercise the same path at 64 activities; this checks volume without
// multiplying SQLite VM instrumentation across thousands of separate writes.
func TestThousandsOfActivitiesDoNotAccumulateInstrumentSupport(t *testing.T) {
	checkActivityContributionVolume(t, 2000)
}
