// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package ibkrflex

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.trippwill.dev/wake/financial"
	"go.trippwill.dev/wake/provider"
	"go.trippwill.dev/wake/source"
)

func TestRestartRequestsFreshFlexReportWithinReviewedBounds(t *testing.T) {
	at := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	account, _ := source.NewReference("ibkr", "account", "", "U1")
	coverage, _ := financial.ParseDate("2026-11-12")
	settings, _ := provider.NewSettings(map[string]string{"query_id": "123"})

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.URL.Path != "/SendRequest" || q.Get("q") != "123" || q.Get("fd") != "20261106" ||
			q.Get("td") != "20261115" || q.Has("p") {
			t.Error("recovery did not request the reviewed dated report", r.URL.Path, q.Get("fd"), q.Get("td"))
		}

		_, _ = fmt.Fprint(
			w,
			"<FlexStatementResponse><Status>Fail</Status><ErrorCode>1003</ErrorCode></FlexStatementResponse>",
		)
	}))
	defer server.Close()

	registration, err := Registration(Options{HTTPClient: server.Client(), BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}

	plan, err := registration.Acquirer().(provider.RestartPlanner).PlanRestart(provider.RestartRequest{
		Stream:          Confirmations,
		Settings:        settings,
		SourceAccount:   account,
		RetainedThrough: coverage,
		RecoveredAt:     at,
	})
	if err != nil || plan.From().String() != "2026-11-06" ||
		plan.To().String() != "2026-11-15" || !plan.NotBefore().Equal(at.Add(time.Minute)) {
		t.Fatal(plan.From(), plan.To(), plan.NotBefore(), err)
	}

	access := &testAccess{token: []byte("synthetic"), now: plan.NotBefore()}

	_, err = registration.Acquirer().Acquire(t.Context(), provider.Request{
		Stream: Confirmations, Settings: settings, State: plan.State(), SourceAccount: account, Now: access.now,
	}, access)
	if !errors.Is(err, provider.ErrUnavailable) {
		t.Fatal("unavailable recovery expanded the approved range", err)
	}
}

func TestRestartRequiresConfirmedFlexAccountAndSupportedRange(t *testing.T) {
	registration, err := Registration(Options{})
	if err != nil {
		t.Fatal(err)
	}

	planner := registration.Acquirer().(provider.RestartPlanner)
	at := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	settings, _ := provider.NewSettings(map[string]string{"query_id": "123", "activity_query_id": "456"})

	request := provider.RestartRequest{Stream: Activity, Settings: settings, RecoveredAt: at}
	if _, err := planner.PlanRestart(request); !errors.Is(err, provider.ErrUnavailable) {
		t.Fatal("unconfirmed account received recovery", err)
	}

	request.SourceAccount, _ = source.NewReference("ibkr", "account", "", "U1")

	plan, err := planner.PlanRestart(request)
	if err != nil || plan.From().String() != at.AddDate(0, 0, -364).Format(time.DateOnly) ||
		plan.To().String() != at.Format(time.DateOnly) {
		t.Fatal("uncovered history was not bounded", plan.From(), plan.To(), err)
	}

	request.RetainedThrough, _ = financial.ParseDate("2026-11-16")
	if _, err := planner.PlanRestart(request); !errors.Is(err, provider.ErrUnavailable) {
		t.Fatal("future retained date was accepted", err)
	}

	request.Stream = "unavailable"
	if _, err := planner.PlanRestart(request); !errors.Is(err, provider.ErrUnavailable) {
		t.Fatal("unsupported stream received recovery", err)
	}
}
