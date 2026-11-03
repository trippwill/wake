// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package providerproof_test

import (
	"context"
	"errors"
	persistence "go.trippwill.dev/wake/internal/persistence/sqlite"
	"go.trippwill.dev/wake/source"
	"strconv"
	"sync"
	"testing"
	"time"

	fixture "example.org/wakefixture"
	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/host"
	"go.trippwill.dev/wake/provider"
)

func TestExternalResultsDoNotRequireSeparateQueriesOrTradeCurrency(t *testing.T) {
	for _, delivery := range []string{"inline", "separate"} {
		t.Run(delivery, func(t *testing.T) {
			registration, err := fixture.RegisterWithResults()
			if err != nil {
				t.Fatal(err)
			}
			f := newFixture(t, registration)
			workflow := application.New(f.store, f.host, nil, func() time.Time { return f.now }, nil)
			partial, _ := provider.NewSettings(map[string]string{"tenant": "synthetic-tenant", "results_delivery": "separate"})
			if err = workflow.UpdateConnection(t.Context(), f.id, "Results source", partial); err != nil {
				t.Fatal(err)
			}
			discovery, err := f.host.Discover(t.Context(), f.id, "statements")
			if err != nil {
				t.Fatal(err)
			}
			candidates, err := f.store.Candidates(t.Context(), f.id)
			if err != nil || len(candidates) != 1 {
				t.Fatal(candidates, err)
			}
			details, _ := application.NewAccountDetails("Results account", "")
			if _, err = workflow.Confirm(t.Context(), f.id, discovery, candidates[0].Reference(), details); err != nil {
				t.Fatal(err)
			}
			f.now = f.now.Add(2 * time.Second)
			if err = workflow.RunOnce(t.Context(), f.now); err != nil {
				t.Fatal(err)
			}
			if f.count(t, "executions") != 1 {
				t.Fatal("initial acquisition did not produce one activity")
			}
			if _, err = f.host.Acquire(t.Context(), f.id, "results"); !errors.Is(err, provider.ErrUnavailable) {
				t.Fatal("incomplete configuration enabled another input", err)
			}
			key := host.WorkKey{Connection: f.id, Stream: "statements", Operation: "acquire"}
			before, err := f.store.Checkpoint(t.Context(), key)
			if err != nil {
				t.Fatal(err)
			}
			settings, _ := provider.NewSettings(map[string]string{"tenant": "synthetic-tenant", "include_results": "true", "results_delivery": delivery})
			if err = workflow.UpdateConnection(t.Context(), f.id, "Results source", settings); err != nil {
				t.Fatal(err)
			}
			after, err := f.store.Checkpoint(t.Context(), key)
			if err != nil || delivery == "separate" && (after.Revision != before.Revision || after.Next != before.Next || string(after.State.Payload()) != string(before.State.Payload())) {
				t.Fatal("provider's independent configuration reset primary work", before, after, err)
			}
			f.now = f.now.Add(2 * time.Second)
			if err = workflow.RunOnce(t.Context(), f.now); err != nil {
				t.Fatal(err)
			}
			snapshot, err := f.store.Snapshot(t.Context(), application.SnapshotCondition{})
			if err != nil {
				t.Fatal(err)
			}
			seen := 0
			for _, resource := range snapshot.Snapshot.Resources {
				if resource.Activity == nil {
					continue
				}
				seen++
				pnl := resource.Activity.ReportedRealizedPnL
				if pnl == nil || pnl.Amount == nil || pnl.CostBasis == nil || pnl.Amount.Amount != "12.34000000000000001" || pnl.CostBasis.Amount != "-100.00000000000000001" ||
					pnl.Amount.Currency != "EUR" || pnl.CostBasis.Currency != "USD" || pnl.ReportDate == nil || *pnl.ReportDate != "2026-10-24" || pnl.CostBasisReportDate == nil || *pnl.CostBasisReportDate != "2026-10-23" || pnl.Conflict {
					t.Fatal("provider financial meaning was constrained", resource)
				}
			}
			if seen != 1 || f.count(t, "executions") != 1 {
				t.Fatal("results created duplicate activity")
			}
			settings, _ = provider.NewSettings(map[string]string{"tenant": "synthetic-tenant"})
			if err = workflow.UpdateConnection(t.Context(), f.id, "Results source", settings); err != nil {
				t.Fatal(err)
			}
			if _, err = f.host.Acquire(t.Context(), f.id, "results"); !errors.Is(err, provider.ErrUnavailable) {
				t.Fatal("disabled input acquired", err)
			}
			retained, err := f.store.Snapshot(t.Context(), application.SnapshotCondition{})
			if err != nil || retained.Snapshot.Version != snapshot.Snapshot.Version {
				t.Fatal("disabling access erased retained results", err)
			}
		})
	}
}

type confirmationGate struct {
	*persistence.Store
	started chan struct{}
	resume  chan struct{}
	calls   int
}

func (s *confirmationGate) Confirm(ctx context.Context, connection host.Connection, artifact string, ref source.Reference, details application.AccountDetails) (application.Account, error) {
	s.calls++
	if s.calls == 1 {
		close(s.started)
		select {
		case <-s.resume:
		case <-ctx.Done():
			return application.Account{}, ctx.Err()
		}
	}
	return s.Store.Confirm(ctx, connection, artifact, ref, details)
}

func TestConfirmationUsesCommittedStreamSettings(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(strconv.FormatBool(enabled), func(t *testing.T) {
			registration, err := fixture.RegisterWithResults()
			if err != nil {
				t.Fatal(err)
			}
			f := newFixture(t, registration)
			gate := &confirmationGate{Store: f.store, started: make(chan struct{}), resume: make(chan struct{})}
			workflow := application.New(gate, f.host, nil, nil, nil)
			settings := func(include bool) provider.Settings {
				values := map[string]string{"tenant": "synthetic-tenant", "results_delivery": "separate"}
				if include {
					values["include_results"] = "true"
				}
				value, e := provider.NewSettings(values)
				if e != nil {
					t.Fatal(e)
				}
				return value
			}
			if err = workflow.UpdateConnection(t.Context(), f.id, "Source", settings(!enabled)); err != nil {
				t.Fatal(err)
			}
			discovery, err := f.host.Discover(t.Context(), f.id, "statements")
			if err != nil {
				t.Fatal(err)
			}
			candidates, err := f.store.Candidates(t.Context(), f.id)
			if err != nil || len(candidates) != 1 {
				t.Fatal(candidates, err)
			}
			details, _ := application.NewAccountDetails("Account", "")
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			var once sync.Once
			release := func() { once.Do(func() { close(gate.resume) }) }
			defer release()
			done := make(chan error, 1)
			go func() { _, e := workflow.Confirm(ctx, f.id, discovery, candidates[0].Reference(), details); done <- e }()
			select {
			case <-gate.started:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if err = workflow.UpdateConnection(ctx, f.id, "Updated source", settings(enabled)); err != nil {
				t.Fatal(err)
			}
			release()
			select {
			case err = <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			work, err := f.store.PendingWork(ctx, f.now.Add(time.Hour))
			if err != nil {
				t.Fatal(err)
			}
			results := false
			for _, item := range work {
				if item.Operation != "acquire" {
					t.Fatal("discovery remained enabled", item)
				}
				results = results || item.Stream == "results"
			}
			if results != enabled {
				t.Fatal("confirmation used stale configured streams", work, enabled)
			}
			if gate.calls != 2 {
				t.Fatal("stale snapshot was not retried", gate.calls)
			}
		})
	}
}
