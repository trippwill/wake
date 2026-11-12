// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

//go:build racefocused

package daemon

import "testing"

func TestRaceFocused(t *testing.T) {
	t.Run("ShellStartupAndJoinedShutdown", TestShellStartupAndJoinedShutdown)
	t.Run("DurableDiscoveryResumesAndJoinsCancellation", TestDurableDiscoveryResumesAndJoinsCancellation)
	t.Run("EventStreamStopsBeforeShutdownGrace", TestEventStreamStopsBeforeShutdownGrace)
	t.Run("CurrencyRefreshFollowsAdminAndDaemonLifetime", TestCurrencyRefreshFollowsAdminAndDaemonLifetime)
}
