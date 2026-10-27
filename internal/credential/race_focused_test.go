// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

//go:build racefocused

package credential

import "testing"

// TestRaceFocused keeps the PR concurrency selection tied to ordinary behavior tests.
// Direct references make a removed or renamed selection fail compilation.
func TestRaceFocused(t *testing.T) {
	for _, test := range []struct {
		name string
		run  func(*testing.T)
	}{
		{"TestCredentialLifecycleIsRaceFree", TestCredentialLifecycleIsRaceFree},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Cleanup(func() {
				if t.Skipped() {
					t.Error("required race test was skipped")
				}
			})
			test.run(t)
		})
	}
}
