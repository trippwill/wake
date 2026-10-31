// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package application

import "testing"

func TestSnapshotConditionDistinguishesWildcardAndSealsVersions(t *testing.T) {
	versions := []string{"*", "first"}
	condition := MatchingSnapshotVersions(versions...)
	versions[0] = "mutated"

	if !condition.Matches("*") || !condition.Matches("first") || condition.Matches("other") ||
		condition.Matches("mutated") {
		t.Fatal("literal versions changed or became wildcards")
	}

	if (SnapshotCondition{}).Matches("first") || !AnySnapshotVersion().Matches("other") {
		t.Fatal("empty/wildcard condition mismatch")
	}
}
