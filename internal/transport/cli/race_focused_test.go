// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

//go:build racefocused

package cli

import "testing"

func TestRaceFocused(t *testing.T) {
	t.Run("UnlockInputCancellationRestoresTerminal", TestUnlockInputCancellationRestoresTerminal)
	t.Run("GenerateKeyRejectsRunningInstance", TestGenerateKeyRejectsRunningInstance)
}
