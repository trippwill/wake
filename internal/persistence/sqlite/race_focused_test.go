// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

//go:build racefocused

package persistence

import "testing"

func TestRaceFocused(t *testing.T) {
	t.Run("ClientRevocationStreamsAndPersistence", TestClientRevocationStreamsAndPersistence)
	t.Run("ClientConcurrentAdmissionAndRevocation", TestClientConcurrentAdmissionAndRevocation)
	t.Run(
		"SnapshotReadIsConsistentAcrossConcurrentAcceptedCommits",
		TestSnapshotReadIsConsistentAcrossConcurrentAcceptedCommits,
	)
	t.Run(
		"SnapshotReleasesReadBeforeClientIOAndBindsExplanations",
		TestSnapshotReleasesReadBeforeClientIOAndBindsExplanations,
	)
	t.Run(
		"ActivitySearchReadsCommittedSnapshotDuringWrite",
		TestActivitySearchReadsCommittedSnapshotDuringWrite,
	)
	t.Run(
		"PublicationSubscriptionsOnlySignalCommittedChanges",
		TestPublicationSubscriptionsOnlySignalCommittedChanges,
	)
	t.Run(
		"PositionsStreamUsesIndependentVersionsAndReconnects",
		TestPositionsStreamUsesIndependentVersionsAndReconnects,
	)
	t.Run("WorkflowRetryRefusesActiveDiscovery", TestWorkflowRetryRefusesActiveDiscovery)
}
