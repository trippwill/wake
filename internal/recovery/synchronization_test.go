// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package recovery

import "testing"

func TestSyncPlanIDPreservesConfirmationBinding(t *testing.T) {
	paths := [storeCount]string{"/wake/warehouse.db", "/wake/access.db", "/wake/sync.db"}
	inputs := [storeCount]string{"warehouse-digest", "access-digest", ""}

	id, err := SyncPlanID(paths, inputs, "archive-digest")
	if err != nil {
		t.Fatal(err)
	}

	const previousID = "fc2260f617ef9a5744035a9f8bf9ef5981b81367196895ce17c0c639e26664e2"
	if id != previousID {
		t.Fatalf("synchronization recovery plan ID = %q, want %q", id, previousID)
	}
}
