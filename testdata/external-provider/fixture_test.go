// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package fixture

import "testing"

func TestRegistrationIsSideEffectFree(t *testing.T) {
	r, err := Register()
	if err != nil || r.Descriptor().ID() != "fixture" || r.Acquirer() == nil || r.Normalizer() == nil || r.Discoverer() == nil {
		t.Fatal(r, err)
	}
}
