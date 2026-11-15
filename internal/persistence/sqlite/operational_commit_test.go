// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"testing"
	"time"

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/host"
)

func TestOperationalTransactionsDoNotFinalizeFinancialPublication(t *testing.T) {
	s := newStoreWithSyntheticAccount(t)
	ctx := t.Context()

	changes, unsubscribe := s.SubscribePublications()
	defer unsubscribe()

	before, err := s.Snapshot(ctx, application.AnySnapshotVersion())
	if err != nil {
		t.Fatal(err)
	}
	// An attempted financial finalization fails even when it would publish no changes.
	if _, err = s.db.Exec(
		`CREATE TRIGGER reject_operational_finalization BEFORE INSERT ON publishing BEGIN SELECT RAISE(ABORT,'unexpected financial finalization'); END`,
	); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	if _, err = s.ReserveTogether(
		ctx,
		"synthetic",
		now,
		host.PacingBudget{Policy: "requests", Scope: "scope", Spacing: time.Minute},
	); err != nil {
		t.Fatal(err)
	}

	if err = s.Delay(ctx, "synthetic", "requests", "scope", now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}

	if ok, _, e := s.ReserveReference(ctx, now, 5); e != nil || !ok {
		t.Fatal(ok, e)
	}

	if err = s.ReferenceCooldown(ctx, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	if err = s.ReferenceAllowance(ctx, 2, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	if err = s.ScheduleMissingAcquisition(ctx, "ibkr-flex", "positions"); err != nil {
		t.Fatal(err)
	}

	key := host.WorkKey{Connection: "synthetic", Stream: "confirmations", Operation: "acquire"}
	if err = s.Schedule(ctx, key, true); err != nil {
		t.Fatal(err)
	}

	if err = s.Retry(ctx, key); err != nil {
		t.Fatal(err)
	}

	after, err := s.Snapshot(ctx, application.MatchingSnapshotVersions(before.Snapshot.Version))
	if err != nil || !after.Unchanged || len(changes) != 0 {
		t.Fatal("operational work changed financial publication", err)
	}
}
