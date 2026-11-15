// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package snaptrade

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"go.trippwill.dev/wake/financial"
	"go.trippwill.dev/wake/provider"
)

type restartAccess struct {
	testAccess
	count int
}

func (a *restartAccess) ReservePacingTogether(
	ctx context.Context, budgets ...provider.PacingBudget,
) (provider.Reservation, error) {
	for _, budget := range budgets {
		if budget.Policy == recoveryActivityPacing {
			a.count++
		}
	}

	return a.testAccess.ReservePacingTogether(ctx, budgets...)
}

func TestRestartVerifiesSnapTradeAccountBeforeFixedScan(t *testing.T) {
	var calls atomic.Int32

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := calls.Add(1)
		switch r.URL.Path {
		case "/authorizations":
			if call != 1 {
				t.Error("unexpected authorization sequence", call)
			}

			_, _ = fmt.Fprint(w, authBody)
		case "/accounts/all":
			if call != 2 {
				t.Error("unexpected account verification sequence", call)
			}

			_, _ = fmt.Fprint(w, accountsBody)
		case "/accounts/account-a/activities":
			q := r.URL.Query()
			if q.Get("startDate") != "2026-10-14" || q.Get("endDate") != "2026-10-23" {
				t.Error("unexpected bounded recovery", call, q.Get("startDate"), q.Get("endDate"), q.Get("offset"))
			}

			switch {
			case call == 3 && q.Get("offset") == "0":
				_, _ = fmt.Fprint(w, pageBody(0, 3, equityRow, optionRow))
			case call == 4 && q.Get("offset") == "1":
				_, _ = fmt.Fprint(w, pageBody(1, 3, optionRow, excludedRow))
			default:
				t.Error("unexpected recovery page", call, q.Get("offset"))
				http.NotFound(w, r)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	registration, err := Registration(Options{HTTPClient: server.Client(), BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}

	settings, _ := provider.NewSettings(map[string]string{clientField: "local-client"})
	scope := accountScope{
		Broker:             ibkr,
		Connection:         "auth-a",
		Institution:        "broker-a",
		InstitutionAccount: "brokerage-account",
	}
	account, _ := scope.reference("account-a")
	retained, _ := financial.ParseDate("2026-10-20")

	at := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	plan, err := registration.Acquirer().(provider.RestartPlanner).PlanRestart(provider.RestartRequest{
		Stream: streamID, Settings: settings, SourceAccount: account, RetainedThrough: retained, RecoveredAt: at,
	})
	if err != nil || !plan.NotBefore().Equal(at.Add(24*time.Hour)) {
		t.Fatal(plan.NotBefore(), err)
	}

	access := &restartAccess{testAccess: testAccess{key: "synthetic-key", deadlines: map[string]time.Time{}}}
	request := provider.Request{
		Stream: streamID, Settings: settings, State: plan.State(), Now: plan.NotBefore(), SourceAccount: account,
	}
	for i := range 4 {
		step, err := registration.Acquirer().Acquire(t.Context(), request, access)
		if err != nil {
			t.Fatal(err)
		}

		if i == 2 && step.State().IsZero() {
			t.Fatal("initial page did not retain a continuation")
		}

		if i == 3 && !step.PollNotBefore().Equal(request.Now.Add(recoveryActivitySpacing)) {
			t.Fatal("completed recovery did not retain its daily pacing floor", step.PollNotBefore())
		}

		request.State = step.State()
	}

	if access.count != 1 || calls.Load() != 4 || !request.State.IsZero() {
		t.Fatal("unexpected recovery pacing or completion", access.count, calls.Load())
	}
}

func TestRestartPlansOnlySupportedSnapTradeAccounts(t *testing.T) {
	registration, err := Registration(Options{})
	if err != nil {
		t.Fatal(err)
	}

	planner := registration.Acquirer().(provider.RestartPlanner)
	settings, _ := provider.NewSettings(map[string]string{clientField: "local-client"})
	scope := accountScope{
		Broker:             fidelity,
		Connection:         "auth-a",
		Institution:        "broker-a",
		InstitutionAccount: "brokerage-account",
	}
	account, _ := scope.reference("account-a")

	at := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	request := provider.RestartRequest{
		Stream:        positionsID,
		Settings:      settings,
		SourceAccount: account,
		RecoveredAt:   at,
	}
	plan, err := planner.PlanRestart(request)
	if err != nil || !plan.From().IsZero() || !plan.To().IsZero() || plan.Description() == "" ||
		!plan.NotBefore().Equal(at.Add(recoveryActivitySpacing)) {
		t.Fatal("Fidelity holdings did not receive a current-snapshot restart plan", err)
	}

	scope.Broker = ibkr

	request.SourceAccount, _ = scope.reference("account-a")
	if _, err := planner.PlanRestart(request); !errors.Is(err, provider.ErrUnavailable) {
		t.Fatal("unsupported brokerage received positions recovery", err)
	}

	request.Stream = streamID
	request.RetainedThrough, _ = financial.ParseDate("2026-10-24")
	if _, err := planner.PlanRestart(request); !errors.Is(err, provider.ErrUnavailable) {
		t.Fatal("future retained bounds were accepted", err)
	}

	request.Stream = "unknown"
	if _, err := planner.PlanRestart(request); !errors.Is(err, provider.ErrUnavailable) {
		t.Fatal("unsupported stream received recovery", err)
	}
}
