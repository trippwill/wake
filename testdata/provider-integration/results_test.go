// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package providerproof_test

import (
	"errors"
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
