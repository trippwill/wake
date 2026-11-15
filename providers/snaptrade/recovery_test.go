// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package snaptrade

import (
	"context"
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
			if call != 3 || q.Get("startDate") != "2026-10-14" ||
				q.Get("endDate") != "2026-10-23" || q.Get("offset") != "0" {
				t.Error("unexpected bounded recovery", call, q.Get("startDate"), q.Get("endDate"), q.Get("offset"))
			}

			_, _ = fmt.Fprint(w, pageBody(0, 1, equityRow))
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
	for i := range 3 {
		step, err := registration.Acquirer().Acquire(t.Context(), request, access)
		if err != nil {
			t.Fatal(err)
		}

		if i == 2 && !step.PollNotBefore().Equal(request.Now.Add(recoveryActivitySpacing)) {
			t.Fatal("completed recovery did not retain its daily pacing floor", step.PollNotBefore())
		}

		request.State = step.State()
	}

	if access.count != 1 || calls.Load() != 3 || !request.State.IsZero() {
		t.Fatal("unexpected recovery pacing or completion", access.count, calls.Load())
	}
}
