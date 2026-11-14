// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

//go:build racefocused

package financialapi

import "testing"

func TestRaceFocused(t *testing.T) {
	t.Run(
		"PublicationStreamSubscribesBeforeInitialReadAndCleansUp",
		TestPublicationStreamSubscribesBeforeInitialReadAndCleansUp,
	)
	t.Run("PublicationStreamShutdownCancelsMetadataRead", TestPublicationStreamShutdownCancelsMetadataRead)
	t.Run(
		"PublicationStreamDisconnectsClientThatDoesNotRead",
		TestPublicationStreamDisconnectsClientThatDoesNotRead,
	)
}
