// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package snaptrade

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.trippwill.dev/wake/provider"
	"go.trippwill.dev/wake/source"
)

func confirmedRequest(t *testing.T) provider.Request {
	t.Helper()

	settings, err := provider.NewSettings(map[string]string{clientField: "test-client"})
	if err != nil {
		t.Fatal(err)
	}

	ref, err := (accountScope{
		Broker: ibkr, Connection: "auth-a", Institution: "broker-a", InstitutionAccount: "brokerage-account",
	}).reference("account-a")
	if err != nil {
		t.Fatal(err)
	}

	return provider.Request{
		Stream:        streamID,
		Settings:      settings,
		Now:           time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC),
		SourceAccount: ref,
	}
}

func TestScanRevalidatesTheConfirmedAccount(t *testing.T) {
	for _, scenario := range []struct {
		name, authorizations, accounts string
	}{
		{"disabled authorization", strings.Replace(authBody, `"disabled":false`, `"disabled":true`, 1), ""},
		{"changed brokerage account", authBody, strings.Replace(accountsBody, `"brokerage-account"`, `"changed"`, 1)},
		{"missing account", authBody, `{"results":[]}`},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/authorizations":
					_, _ = io.WriteString(w, scenario.authorizations)
				case "/accounts/all":
					if scenario.accounts == "" {
						t.Error("disabled authorization permitted a further request")
					}

					_, _ = io.WriteString(w, scenario.accounts)
				default:
					t.Error("activity request escaped account revalidation")
					http.NotFound(w, r)
				}
			}))
			defer server.Close()

			reg, err := Registration(Options{HTTPClient: server.Client(), BaseURL: server.URL})
			if err != nil {
				t.Fatal(err)
			}

			r := confirmedRequest(t)
			access := testAccess{key: "synthetic-key"}

			step, err := reg.Acquirer().Acquire(t.Context(), r, access)
			if err != nil {
				t.Fatal(err)
			}

			if scenario.accounts != "" {
				r.State = step.State()

				step, err = reg.Acquirer().Acquire(t.Context(), r, access)
				if err != nil {
					t.Fatal(err)
				}
			}

			want := scenario.authorizations
			if scenario.accounts != "" {
				want = scenario.accounts
			}

			if !step.State().IsZero() || !step.NextEligibleAt().Equal(r.Now.Add(time.Hour)) ||
				!bytes.Equal(step.Artifact().Content(), []byte(want)) {
				t.Fatal("unavailable account failed to retain the exact response and defer")
			}
		})
	}
}

func TestActivityTransportFailurePolicy(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		status int
		body   string
		limit  int64
		err    error
		reason provider.RetryReason
		code   string
	}{
		{name: "unauthorized", status: http.StatusUnauthorized, err: provider.ErrUnauthorized},
		{name: "forbidden", status: http.StatusForbidden, err: provider.ErrUnauthorized},
		{name: "removed account", status: http.StatusNotFound, reason: provider.RetryUpstream, code: "snaptrade-upstream"},
		{name: "outage", status: http.StatusServiceUnavailable, reason: provider.RetryUpstream, code: "snaptrade-upstream"},
		{name: "network failure", reason: provider.RetryNetwork, code: "snaptrade-network"},
		{name: "redirect", status: http.StatusTemporaryRedirect, err: provider.ErrInvalid},
		{name: "bounded body", status: http.StatusOK, body: pageBody(0, 1, equityRow), limit: 64, err: provider.ErrLimit},
		{name: "duplicate page IDs", status: http.StatusOK, body: pageBody(0, 2, equityRow, equityRow), reason: provider.RetryUpstream, code: "snaptrade-invalid-page"},
		{name: "successful quota exhaustion", status: http.StatusOK, body: pageBody(0, 0)},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			r := confirmedRequest(t)
			retryAt := r.Now.Add(20 * time.Minute)

			state, err := (scan{End: r.Now.Format(time.DateOnly), Verified: true}).state()
			if err != nil {
				t.Fatal(err)
			}

			r.State = state

			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				if request.URL.Path != "/accounts/account-a/activities" {
					t.Error("request escaped the selected account endpoint")
					http.NotFound(w, request)

					return
				}

				if scenario.status == 0 {
					panic(http.ErrAbortHandler)
				}

				switch scenario.status {
				case http.StatusUnauthorized, http.StatusServiceUnavailable:
					w.Header().Set("Retry-After", retryAt.Format(http.TimeFormat))
				case http.StatusTemporaryRedirect:
					w.Header().Set("Location", "/unexpected")
				case http.StatusOK:
					w.Header().Set("X-RateLimit-Remaining", "0")
					w.Header().Set("X-RateLimit-Reset", "70")
					w.Header().Set("X-RateLimit-Account-Remaining", "0")
					w.Header().Set("X-RateLimit-Account-Reset", "80")
				}

				w.WriteHeader(scenario.status)
				_, _ = io.WriteString(w, scenario.body)
			}))
			defer server.Close()

			reg, err := Registration(Options{
				HTTPClient: server.Client(), BaseURL: server.URL, MaxReportBytes: scenario.limit,
			})
			if err != nil {
				t.Fatal(err)
			}

			access := testAccess{key: "synthetic-key", deadlines: map[string]time.Time{}}

			step, err := reg.Acquirer().Acquire(t.Context(), r, access)
			if err != scenario.err || step.RetryReason() != scenario.reason || step.FailureCode() != scenario.code {
				t.Fatalf("step failure=%q reason=%q, error=%v", step.FailureCode(), step.RetryReason(), err)
			}

			if scenario.err != nil || scenario.reason != "" {
				if !step.Artifact().IsZero() {
					t.Fatal("a failed request produced evidence")
				}
			} else if !bytes.Equal(step.Artifact().Content(), []byte(scenario.body)) ||
				access.deadlines[clientPacing].Before(r.Now.Add(70*time.Second)) ||
				access.deadlines[accountPacing].Before(r.Now.Add(80*time.Second)) {
				t.Fatal("successful quota response lost evidence or a cooldown")
			}

			switch scenario.status {
			case http.StatusUnauthorized:
				if !access.deadlines[clientPacing].Equal(retryAt) {
					t.Fatal("authorization retry did not honor the later deadline")
				}
			case http.StatusForbidden:
				if access.deadlines[clientPacing].Before(r.Now.Add(15 * time.Minute)) {
					t.Fatal("missing minimum authorization cooldown")
				}
			case http.StatusServiceUnavailable:
				if !step.NextEligibleAt().Equal(retryAt) {
					t.Fatal("outage did not honor Retry-After")
				}
			case http.StatusNotFound:
				var cursor scan
				if json.Unmarshal(step.State().Payload(), &cursor) != nil || cursor.Verified {
					t.Fatal("removed account did not invalidate scan verification")
				}
			}
		})
	}
}

func TestNormalizeSupportedFundsAndRejectAmbiguousRows(t *testing.T) {
	fund := strings.ReplaceAll(equityRow, `"code":"cs"`, `"code":"et"`)
	fund = strings.ReplaceAll(fund, `"raw_symbol":"SYN"`, `"symbol":"SYN.TO","figi_code":"BBG000000001"`)
	fund = strings.Replace(fund, `"code":"USD"},"exchange"`, `"code":"CAD"},"exchange"`, 1)
	future := strings.ReplaceAll(equityRow, `"code":"cs"`, `"code":"fut"`)
	future = strings.ReplaceAll(future, "activity-a", "activity-future")
	badUnits := strings.ReplaceAll(equityRow, "activity-a", "activity-units")
	badUnits = strings.ReplaceAll(badUnits, `"units":1.230000000000000001`, `"units":"1.23"`)
	badOption := strings.ReplaceAll(optionRow, `"SELL_TO_CLOSE"`, `"BUY_TO_CLOSE"`)

	artifact, err := provider.NewArtifact(
		format,
		"application/json",
		confirmedRequest(t).Now,
		[]byte(pageBody(0, 4, fund, future, badUnits, badOption)),
	)
	if err != nil {
		t.Fatal(err)
	}

	reg, err := Registration(Options{})
	if err != nil {
		t.Fatal(err)
	}

	sink := &testClaims{}

	result, err := reg.Normalizer().Normalize(
		t.Context(), artifact.WithSourceAccount(confirmedRequest(t).SourceAccount), sink,
	)
	if err != nil || result.Activities() != 1 || result.Rejected() != 3 {
		t.Fatalf("fund result=%+v, error=%v", result.Counts(), err)
	}

	trade, err := sink.observations[0].Trade()
	if err != nil {
		t.Fatal(err)
	}

	i := trade.Fields().Instrument
	identifiers := i.Identifiers()
	if i.Kind() != source.Fund || i.Ticker() != "SYN.TO" || i.Venue() != "" ||
		len(identifiers) != 1 || identifiers[0].Kind() != source.VenueFIGI {
		t.Fatalf("unexpected instrument facts: %+v", i.Fields())
	}
}
