// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"testing"

	"go.trippwill.dev/wake/internal/application"
)

func TestPublicationSubscriptionsOnlySignalCommittedChanges(t *testing.T) {
	s := newStoreWithSyntheticAccount(t)

	changes, unsubscribe := s.SubscribePublications()
	defer unsubscribe()

	other, removeOther := s.SubscribePublications()
	row := equityRow("U1", "notifications", "100")
	processConfirmationReport(t, s, "initial", row)

	if len(changes) != 1 || len(other) != 1 {
		t.Fatal("committed publication did not reach both subscribers")
	}

	<-changes
	<-other
	removeOther()
	removeOther()
	processConfirmationReport(t, s, "known-claims", row)

	if len(changes) != 0 {
		t.Fatal("known claims emitted a notification")
	}

	baseline, err := s.Snapshot(t.Context(), application.AnySnapshotVersion())
	if err != nil {
		t.Fatal(err)
	}

	for _, outcome := range []string{"rollback", "failed", "commit", "burst"} {
		tx, e := s.db.BeginTx(t.Context(), nil)
		if e != nil {
			t.Fatal(e)
		}

		if _, e = tx.Exec(`UPDATE accounts SET name=?`, outcome); e != nil {
			t.Fatal(e)
		}

		if len(changes) != 0 && outcome != "burst" {
			t.Fatal("uncommitted write notified")
		}

		switch outcome {
		case "rollback":
			e = tx.Rollback()
		case "failed":
			if _, e = tx.Exec(
				`CREATE TRIGGER reject_publication BEFORE UPDATE ON publishing BEGIN SELECT RAISE(ABORT,'injected'); END`,
			); e != nil {
				t.Fatal(e)
			}

			if e = s.commit(t.Context(), tx); e == nil {
				t.Fatal("injected publishing failure succeeded")
			}

			e = tx.Rollback()
		default:
			e = s.commit(t.Context(), tx)
		}

		if e != nil {
			t.Fatal(e)
		}

		if outcome == "rollback" || outcome == "failed" {
			if len(changes) != 0 {
				t.Fatal("rollback or failure notified")
			}

			read, e := s.Snapshot(t.Context(), application.MatchingSnapshotVersions(baseline.Snapshot.Version))
			if e != nil || !read.Unchanged {
				t.Fatal("failed transaction changed publication", read, e)
			}
		}
	}

	if len(changes) != 1 || len(other) != 0 {
		t.Fatal("burst did not coalesce or unsubscribe failed")
	}

	<-changes

	latest, err := s.Snapshot(t.Context(), application.MatchingSnapshotVersions(baseline.Snapshot.Version))
	if err != nil || latest.Unchanged {
		t.Fatal("notification preceded committed state", latest, err)
	}
}
