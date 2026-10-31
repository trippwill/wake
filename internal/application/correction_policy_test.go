// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package application

import "testing"

func TestStrikePolicyRequiresTheAmountExpectedByApplication(t *testing.T) {
	policy, ok := ActivityFieldPolicy(TradeActivity, "option.strike")
	if !ok || policy.Optional() || policy.ValueKind() != MoneyCorrection {
		t.Fatal("editor promises unsupported blank strike", policy, ok)
	}
}
