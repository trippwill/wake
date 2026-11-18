// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0
package persistence

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/application/calculation"
	"go.trippwill.dev/wake/internal/transport/financialapi"
)

func TestSnapshotDeltaFollowsAcceptedState(t *testing.T) {
	s := newStoreWithSyntheticAccount(t)
	ctx := t.Context()
	row := equityRow("U1", "snapshot-fill", "9007199254740993.0000000000001")
	processConfirmationReport(t, s, "snapshot", row)

	first, err := s.Snapshot(ctx, application.HistoryPublication{})
	if err != nil || first.Delta != nil || first.Snapshot.BaselineID == "" || first.Snapshot.Version == "" ||
		len(first.Snapshot.Resources) != 4 {
		t.Fatal(first, err)
	}

	activity := ""
	for _, resource := range first.Snapshot.Resources {
		if resource.Kind == "activity" {
			activity = resource.ID
			if resource.Activity.Trade.Price.Amount != "9007199254740993.0000000000001" {
				t.Fatal("snapshot lost exact precision", resource)
			}
		}
	}

	if activity == "" {
		t.Fatal("snapshot omitted activity")
	}

	processConfirmationReport(t, s, "same-source-again", row)

	unchanged, err := s.Snapshot(
		ctx,
		application.HistoryPublication{BaselineID: first.Snapshot.BaselineID, Version: first.Snapshot.Version},
	)
	if err != nil || unchanged.Delta == nil || unchanged.Delta.Version != first.Snapshot.Version ||
		len(unchanged.Delta.Upserts)+len(unchanged.Delta.Removals) != 0 {
		t.Fatal("repeat claims re-published or conditional read loaded resources", unchanged, err)
	}

	retainDecision(t, s, "activity", activity, "price", "12.000000000000001", "USD", "set")

	changed, err := s.Snapshot(
		ctx,
		application.HistoryPublication{BaselineID: first.Snapshot.BaselineID, Version: first.Snapshot.Version},
	)
	if err != nil || changed.Delta == nil || changed.Delta.Version == first.Snapshot.Version ||
		changed.Delta.BaselineID != first.Snapshot.BaselineID || changed.Delta.From != first.Snapshot.Version ||
		len(changed.Delta.Upserts) != 1 || len(changed.Delta.Removals) != 0 {
		t.Fatal("expected a single activity update", changed, err)
	}

	resource := changed.Delta.Upserts[0]
	if resource.ID != activity || resource.Activity.Trade.Price.Amount != "12.000000000000001" {
		t.Fatal("delta mixed resource state", resource)
	}

	if s.db.Stats().InUse != 0 {
		t.Fatal("snapshot retained a database transaction")
	}

	replacement := newStoreWithSyntheticAccount(t)
	processConfirmationReport(t, replacement, "new-warehouse", row)

	baseline, err := replacement.Snapshot(
		ctx,
		application.HistoryPublication{BaselineID: changed.Delta.BaselineID, Version: changed.Delta.Version},
	)
	if err != nil || baseline.Delta != nil || baseline.Snapshot.BaselineID == changed.Delta.BaselineID ||
		baseline.Snapshot.Version == changed.Delta.Version {
		t.Fatal("warehouse identity did not establish a new baseline", baseline, err)
	}
}

func TestEmptyWarehouseSnapshotIsComplete(t *testing.T) {
	s, _ := testStore(t)
	snapshot, err := s.Snapshot(t.Context(), application.HistoryPublication{})
	if err != nil || snapshot.Snapshot.BaselineID == "" || snapshot.Snapshot.Version == "" ||
		snapshot.Snapshot.Resources == nil ||
		len(snapshot.Snapshot.Resources) != 0 {
		t.Fatal(snapshot, err)
	}
}

func TestSnapshotReleasesReadBeforeClientIOAndBindsExplanations(t *testing.T) {
	s := newStoreWithSyntheticAccount(t)
	processConfirmationReport(t, s, "blocked-client", equityRow("U1", "fill", "10.0000"))

	first, err := s.Snapshot(t.Context(), application.HistoryPublication{})
	if err != nil {
		t.Fatal(err)
	}

	var activity application.CanonicalResource
	for _, r := range first.Snapshot.Resources {
		if r.Kind == "activity" {
			activity = r
		}
	}

	writer := &pausedSnapshotWriter{
		header:  http.Header{},
		started: make(chan struct{}),
		release: make(chan struct{}),
	}

	var release sync.Once

	unblock := func() { release.Do(func() { close(writer.release) }) }
	defer unblock()

	done := make(chan struct{})
	go func() {
		defer close(done)

		financialapi.NewHandler(s, nil, nil).
			ServeHTTP(writer, httptest.NewRequest(http.MethodGet, "/v1/snapshot", nil))
	}()

	select {
	case <-writer.started:
	case <-time.After(5 * time.Second):
		t.Fatal("response did not start")
	}

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	if s.db.Stats().InUse != 0 {
		t.Fatal("slow client retains a read transaction")
	}

	form, err := s.CorrectionForm(ctx, "activity", activity.ID)
	if err != nil {
		t.Fatal(err)
	}

	decision, err := calculation.NewDecision(
		calculation.DecisionInput{
			TargetKind:       "activity",
			TargetID:         activity.ID,
			Scope:            "price",
			Text:             "12.000000001",
			Currency:         "USD",
			Action:           "set",
			Submission:       rand.Text(),
			ExpectedRevision: form.Revision,
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	if err = application.SaveDecision(ctx, s, decision, "synthetic/operator", time.Now()); err != nil {
		t.Fatal("slow client blocked accepted writes", err)
	}

	if _, err = s.Explanation(
		ctx,
		"activity",
		activity.ID,
		activity.Revision,
	); !errors.Is(
		err,
		application.ErrStale,
	) {
		t.Fatal("old explanation revision accepted", err)
	}

	current, err := s.Resource(ctx, "activity", activity.ID)
	if err != nil {
		t.Fatal(err)
	}

	if why, e := s.Explanation(
		ctx,
		"activity",
		activity.ID,
		current.Revision,
	); e != nil ||
		why.Revision != current.Revision {
		t.Fatal("current explanation unavailable", why, e)
	}

	unblock()

	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("client did not finish")
	}

	var downloaded application.FinancialSnapshot
	if err = json.Unmarshal(
		writer.body.Bytes(),
		&downloaded,
	); err != nil ||
		downloaded.Version != first.Snapshot.Version {
		t.Fatal("blocked response changed after materialization", downloaded.Version, err)
	}

	for _, r := range downloaded.Resources {
		if r.ID == activity.ID && r.Activity.Trade.Price.Amount != "10.0000" {
			t.Fatal("response mixed later accepted state", r)
		}
	}
}

type pausedSnapshotWriter struct {
	header           http.Header
	started, release chan struct{}
	once             sync.Once
	body             bytes.Buffer
}

func (w *pausedSnapshotWriter) Header() http.Header { return w.header }
func (w *pausedSnapshotWriter) WriteHeader(int)     {}
func (w *pausedSnapshotWriter) Write(data []byte) (int, error) {
	w.once.Do(func() { close(w.started) })
	<-w.release

	return w.body.Write(data)
}

func TestSnapshotReadIsConsistentAcrossConcurrentAcceptedCommits(t *testing.T) {
	s := newStoreWithSyntheticAccount(t)
	processConfirmationReport(t, s, "concurrent-snapshot", equityRow("U1", "fill", "10"))

	update := func(n int) error {
		tx, err := s.db.BeginTx(t.Context(), nil)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()

		marker := fmt.Sprintf("marker-%04d", n)
		if _, err = tx.ExecContext(t.Context(), "UPDATE accounts SET name=?", marker); err != nil {
			return err
		}

		if _, err = tx.ExecContext(t.Context(), "UPDATE listings SET ticker=?", marker); err != nil {
			return err
		}

		return s.commit(t.Context(), tx)
	}
	if err := update(0); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		for n := 1; n <= 40; n++ {
			if err := update(n); err != nil {
				done <- err
				return
			}
		}

		done <- nil
	}()

	for range 40 {
		snapshot, err := s.Snapshot(t.Context(), application.HistoryPublication{})
		if err != nil {
			t.Fatal(err)
		}

		name, ticker := "", ""
		for _, r := range snapshot.Snapshot.Resources {
			if r.Kind == "accounts" {
				name = r.Account.Name
			}

			if r.Kind == "listings" && r.Listing.Ticker != nil {
				ticker = *r.Listing.Ticker
			}
		}

		if name == "" || name != ticker {
			t.Fatal("snapshot crossed accepted transaction boundary", name, ticker)
		}
	}

	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotFallsBackWhenBaseExpires(t *testing.T) {
	s := newStoreWithSyntheticAccount(t)
	ctx := t.Context()

	base, err := s.HistoryPublication(ctx)
	if err != nil {
		t.Fatal(err)
	}

	for i := range retainedHistoryVersions {
		tx, e := s.db.BeginTx(ctx, nil)
		if e != nil {
			t.Fatal(e)
		}

		if _, e = tx.ExecContext(ctx, "UPDATE accounts SET name=?", fmt.Sprintf("account-%d", i)); e != nil {
			_ = tx.Rollback()

			t.Fatal(e)
		}

		if e = s.commit(ctx, tx); e != nil {
			_ = tx.Rollback()

			t.Fatal(e)
		}
	}

	fallback, err := s.Snapshot(ctx, base)
	if err != nil || fallback.Delta != nil || fallback.Snapshot.Version == base.Version ||
		fallback.Snapshot.Mode != "full" || len(fallback.Snapshot.Resources) != 1 {
		t.Fatal("expired base did not receive a full snapshot", fallback, err)
	}

	var oldest application.HistoryPublication

	oldest.BaselineID = base.BaselineID
	if err = s.db.QueryRowContext(ctx, "SELECT token FROM publication_versions ORDER BY sequence LIMIT 1").
		Scan(&oldest.Version); err != nil {
		t.Fatal(err)
	}

	retained, err := s.Snapshot(ctx, oldest)
	if err != nil || retained.Delta == nil || retained.Delta.Version != fallback.Snapshot.Version ||
		len(retained.Delta.Upserts) != 1 || len(retained.Delta.Removals) != 0 ||
		retained.Delta.Upserts[0].Account.Name != fallback.Snapshot.Resources[0].Account.Name {
		t.Fatal("oldest retained base did not coalesce all updates", retained, err)
	}

	unknown, err := s.Snapshot(
		ctx,
		application.HistoryPublication{BaselineID: base.BaselineID, Version: "unknown"},
	)
	if err != nil || unknown.Delta != nil || unknown.Snapshot.Version != fallback.Snapshot.Version {
		t.Fatal("unknown base did not receive a full snapshot", unknown, err)
	}
}

func TestSnapshotFallsBackWhenDeltaExceedsBudget(t *testing.T) {
	for _, test := range []struct {
		name string
		keys int
	}{
		{"response-key-limit", maxDeltaKeys + 1},
		{"journal-row-limit", retainedHistoryChanges + 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := newStoreWithSyntheticAccount(t)
			ctx := t.Context()

			base, err := s.HistoryPublication(ctx)
			if err != nil {
				t.Fatal(err)
			}

			keys := make([]application.ResourceKey, test.keys)
			for i := range keys {
				keys[i] = application.ResourceKey{Kind: "activity", ID: fmt.Sprint(i)}
			}

			tx, err := s.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}

			defer func() { _ = tx.Rollback() }()

			if err = advancePublication(ctx, tx, keys); err != nil {
				t.Fatal(err)
			}

			if err = tx.Commit(); err != nil {
				t.Fatal(err)
			}

			read, err := s.Snapshot(ctx, base)
			if err != nil || read.Delta != nil || read.Snapshot.Mode != "full" ||
				read.Snapshot.Version == base.Version {
				t.Fatal("expected full fallback", read, err)
			}

			if test.keys > retainedHistoryChanges {
				var count int
				if err = s.db.QueryRowContext(ctx, "SELECT count(*) FROM publication_changes").
					Scan(&count); err != nil ||
					count != 0 {
					t.Fatal("large publication retained journal rows", count, err)
				}
			}
		})
	}
}

func TestSnapshotFallsBackWhenDeltaExceedsByteBudget(t *testing.T) {
	s := newStoreWithSyntheticAccount(t)
	ctx := t.Context()

	base, err := s.HistoryPublication(ctx)
	if err != nil {
		t.Fatal(err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = tx.Rollback() }()

	if _, err = tx.ExecContext(
		ctx,
		"UPDATE accounts SET name=?",
		strings.Repeat("x", maxDeltaBytes),
	); err != nil {
		t.Fatal(err)
	}

	if err = s.commit(ctx, tx); err != nil {
		t.Fatal(err)
	}

	read, err := s.Snapshot(ctx, base)
	if err != nil || read.Delta != nil || read.Snapshot.Mode != "full" ||
		read.Snapshot.Version == base.Version || len(read.Snapshot.Resources) != 1 {
		t.Fatal("expected full snapshot for oversized upsert", err)
	}
}
