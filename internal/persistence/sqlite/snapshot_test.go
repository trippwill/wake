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
	"sync"
	"testing"
	"time"

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/transport/socket"
)

func TestSnapshotVersionAndConditionalReadFollowAcceptedState(t *testing.T) {
	s := coverageStore(t)
	ctx := t.Context()
	row := equityRow("U1", "snapshot-fill", "9007199254740993.0000000000001")
	coverageReport(t, s, "snapshot", row)

	first, err := s.Snapshot(ctx, nil)
	if err != nil || first.Unchanged || first.Snapshot.WarehouseID == "" || first.Snapshot.Version == "" ||
		len(first.Snapshot.Resources) != 4 {
		t.Fatal(first, err)
	}

	activity := ""
	for _, resource := range first.Snapshot.Resources {
		if resource.Kind == "activity" {
			activity = resource.ID
			if resource.Activity.Price.Amount != "9007199254740993.0000000000001" {
				t.Fatal("snapshot lost exact precision", resource)
			}
		}
	}

	if activity == "" {
		t.Fatal("snapshot omitted activity")
	}

	coverageReport(t, s, "same-source-again", row)

	unchanged, err := s.Snapshot(ctx, []string{first.Snapshot.Version})
	if err != nil || !unchanged.Unchanged || unchanged.Snapshot.Version != first.Snapshot.Version ||
		len(unchanged.Snapshot.Resources) != 0 {
		t.Fatal("repeat claims re-published or conditional read loaded resources", unchanged, err)
	}

	retainDecision(t, s, "activity", activity, "price", "12.000000000000001", "USD", "set")

	changed, err := s.Snapshot(ctx, []string{first.Snapshot.Version})
	if err != nil || changed.Unchanged || changed.Snapshot.Version == first.Snapshot.Version ||
		changed.Snapshot.WarehouseID != first.Snapshot.WarehouseID {
		t.Fatal("accepted change did not replace snapshot version", changed, err)
	}

	for _, resource := range changed.Snapshot.Resources {
		if resource.ID == activity && resource.Activity.Price.Amount != "12.000000000000001" {
			t.Fatal("snapshot mixed resource state", resource)
		}
	}

	if s.db.Stats().InUse != 0 {
		t.Fatal("snapshot retained a database transaction")
	}

	replacement := coverageStore(t)
	coverageReport(t, replacement, "new-warehouse", row)

	baseline, err := replacement.Snapshot(ctx, []string{changed.Snapshot.Version})
	if err != nil || baseline.Unchanged || baseline.Snapshot.WarehouseID == changed.Snapshot.WarehouseID ||
		baseline.Snapshot.Version == changed.Snapshot.Version {
		t.Fatal("warehouse identity did not establish a new baseline", baseline, err)
	}
}

func TestEmptyWarehouseSnapshotIsComplete(t *testing.T) {
	s, _ := testStore(t)
	snapshot, err := s.Snapshot(t.Context(), nil)
	if err != nil || snapshot.Snapshot.WarehouseID == "" || snapshot.Snapshot.Version == "" ||
		snapshot.Snapshot.Resources == nil ||
		len(snapshot.Snapshot.Resources) != 0 {
		t.Fatal(snapshot, err)
	}
}

func TestSnapshotReleasesReadBeforeClientIOAndBindsExplanations(t *testing.T) {
	s := coverageStore(t)
	coverageReport(t, s, "blocked-client", equityRow("U1", "fill", "10.0000"))

	first, err := s.Snapshot(t.Context(), nil)
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

		socket.NewHandler(s, nil).ServeHTTP(writer, httptest.NewRequest(http.MethodGet, "/v1/snapshot", nil))
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

	decision, err := application.NewDecision(
		application.DecisionInput{
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

	if err = s.SaveDecision(ctx, decision, "synthetic/operator", time.Now()); err != nil {
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
		if r.ID == activity.ID && r.Activity.Price.Amount != "10.0000" {
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
	s := coverageStore(t)
	coverageReport(t, s, "concurrent-snapshot", equityRow("U1", "fill", "10"))

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
		snapshot, err := s.Snapshot(t.Context(), nil)
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
