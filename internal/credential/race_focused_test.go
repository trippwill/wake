// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

//go:build racefocused

package credential

import "testing"

func TestRaceFocused(t *testing.T) {
	t.Run("CredentialLifecycleIsRaceFree", TestCredentialLifecycleIsRaceFree)
}
