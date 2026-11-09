// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package snaptrade

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.trippwill.dev/wake/provider"
	"go.trippwill.dev/wake/source"
)

const (
	authBody     = `[{"id":"auth-a","disabled":false,"brokerage":{"id":"broker-a","slug":"INTERACTIVE-BROKERS-FLEX"}},{"id":"auth-b","disabled":true,"brokerage":{"id":"broker-b","slug":"FIDELITY"}}]`
	accountsBody = `{"results":[{"id":"account-a","connection_id":"auth-a","institution_id":"broker-a","institution_account_id":"brokerage-account","display_name":"Test broker account","kind":"investment"},{"id":"account-b","connection_id":"auth-b","institution_id":"broker-b","display_name":"Disabled broker account","kind":"investment"}]}`
	equityRow    = `{"id":"activity-a","type":"BUY","units":1.230000000000000001,"price":4.56700,"fee":0.000,"amount":-7.00,"currency":{"code":"USD"},"trade_date":"2026-10-20T15:30:00.1200Z","settlement_date":"2026-10-21T00:00:00Z","symbol":{"id":"stock-a","raw_symbol":"SYN","type":{"code":"cs"},"currency":{"code":"USD"},"exchange":{"mic_code":"XNYS"}},"external_reference_id":"group-ref"}`
	optionRow    = `{"id":"activity-b","type":"SELL","option_type":"SELL_TO_CLOSE","units":-2,"price":0.12500,"currency":{"code":"USD"},"trade_date":"2026-10-20","option_symbol":{"id":"option-b","ticker":"SYN   261218P00240000","option_type":"PUT","strike_price":240.00,"expiration_date":"2026-12-18T00:00:00Z","is_mini_option":true,"underlying_symbol":{"id":"stock-a","raw_symbol":"SYN","type":{"code":"cs"},"currency":{"code":"USD"}}}}`
	excludedRow  = `{"id":"activity-c","type":"DIVIDEND"}`
)

func pageBody(offset, total int, rows ...string) string {
	return fmt.Sprintf(
		`{"data":[%s],"pagination":{"offset":%d,"limit":1000,"total":%d}}`,
		strings.Join(rows, ","),
		offset,
		total,
	)
}

type testAccess struct {
	key       string
	deadlines map[string]time.Time
}

func (a testAccess) WithSecret(_ context.Context, key string, fn func([]byte) error) error {
	if key != "consumer_key" {
		return provider.ErrUnauthorized
	}

	return fn([]byte(a.key))
}
func (testAccess) Report(context.Context, provider.Progress) error { return nil }
func (testAccess) ReservePacing(context.Context, string, provider.PacingScope) (provider.Reservation, error) {
	return provider.NewReservation(true, time.Unix(1, 0))
}

func (testAccess) ReservePacingTogether(
	context.Context,
	...provider.PacingBudget,
) (provider.Reservation, error) {
	return provider.NewReservation(true, time.Unix(1, 0))
}

func (a testAccess) DelayPacing(
	_ context.Context,
	policy string,
	_ provider.PacingScope,
	until time.Time,
) error {
	a.deadlines[policy] = until

	return nil
}

type testClaims struct {
	provider.Claims
	observations []source.ActivityObservation
	positions    []source.PositionSetObservation
	rejected     []string
}

func (s *testClaims) PutPositionSet(_ context.Context, claim source.PositionSetObservation) error {
	s.positions = append(s.positions, claim)
	return nil
}

func (s *testClaims) PutActivity(_ context.Context, claim source.ActivityObservation) error {
	s.observations = append(s.observations, claim)

	return nil
}

func (s *testClaims) Reject(_ context.Context, location, _ string) error {
	s.rejected = append(s.rejected, location)

	return nil
}

func TestPersonalDiscoveryAndResumedHistory(t *testing.T) {
	const secret = "synthetic-personal-secret"

	var mode atomic.Int32

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()

		canonical := fmt.Sprintf(`{"content":null,"path":%q,"query":%q}`, r.URL.Path, r.URL.RawQuery)
		mac := hmac.New(sha256.New, []byte(secret))
		_, _ = io.WriteString(mac, canonical)
		if r.Method != http.MethodGet || q.Get("clientId") != "local+client" || q.Get("timestamp") == "" ||
			q.Has(
				"userId",
			) || q.Has("userSecret") || r.Header.Get("Signature") != base64.StdEncoding.EncodeToString(mac.Sum(nil)) {
			t.Error("request differs from signed Personal GET")

			w.WriteHeader(http.StatusUnauthorized)

			return
		}

		switch r.URL.Path {
		case "/authorizations":
			_, _ = io.WriteString(w, authBody)
		case "/accounts/all":
			_, _ = io.WriteString(w, accountsBody)
		case "/accounts/account-a/activities":
			if q.Get("endDate") != "2026-10-21" || q.Get("type") != "BUY,SELL" || q.Get("limit") != "1000" ||
				q.Has("startDate") {
				t.Error("unexpected activities request")
			}

			switch q.Get("offset") {
			case "0":
				_, _ = io.WriteString(w, pageBody(0, 3, equityRow, optionRow))
			case "1":
				switch mode.Load() {
				case 1:
					w.Header().Set("Retry-After", "20")
					w.Header().Set("X-RateLimit-Reset", "80")
					w.Header().Set("X-RateLimit-Account-Reset", "90")
					w.WriteHeader(http.StatusTooManyRequests)
					_, _ = io.WriteString(w, secret)
				case 2:
					_, _ = io.WriteString(w, pageBody(1, 3, equityRow, excludedRow))
				default:
					_, _ = io.WriteString(w, pageBody(1, 3, optionRow, excludedRow))
				}
			default:
				t.Error("unexpected activity offset")
				http.NotFound(w, r)
			}
		default:
			t.Error("unexpected upstream endpoint")
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	options := Options{HTTPClient: srv.Client(), BaseURL: srv.URL}

	reg, err := Registration(options)
	if err != nil {
		t.Fatal(err)
	}

	settings, err := provider.NewSettings(map[string]string{"client_id": "local+client"})
	if err != nil {
		t.Fatal(err)
	}

	r := provider.Request{
		Stream:   "activities",
		Settings: settings,
		Now:      time.Date(2026, 10, 21, 16, 0, 0, 0, time.UTC),
	}
	access := testAccess{key: secret, deadlines: map[string]time.Time{}}
	if _, err = reg.Acquirer().Acquire(t.Context(), r, access); err != provider.ErrUnauthorized {
		t.Fatalf("unconfirmed acquisition: %v", err)
	}

	first, err := reg.Discoverer().Discover(t.Context(), r, access)
	if err != nil || !bytes.Equal(first.Step().Artifact().Content(), []byte(authBody)) ||
		first.Step().State().IsZero() {
		t.Fatalf("authorization discovery: %v", err)
	}

	r.State = first.Step().State()
	last, err := reg.Discoverer().Discover(t.Context(), r, access)
	if err != nil || len(last.Candidates()) != 1 || !last.Step().CandidateSnapshot() ||
		!bytes.Equal(last.Step().Artifact().Content(), []byte(accountsBody)) {
		t.Fatalf("account discovery: %v", err)
	}

	r.State, r.SourceAccount = provider.State{}, last.Candidates()[0].Reference()

	ibkrPositions := r
	ibkrPositions.Stream = positionsID

	skipped, err := reg.Acquirer().Acquire(t.Context(), ibkrPositions, access)
	if err != nil || !skipped.Artifact().IsZero() || !skipped.State().IsZero() ||
		!skipped.PollCompleted() || !skipped.NextEligibleAt().Equal(r.Now) {
		t.Fatalf("IBKR positions stream should remain idle: %+v %v", skipped, err)
	}

	acquire := func(reg provider.Registration) provider.Step {
		t.Helper()

		step, err := reg.Acquirer().Acquire(t.Context(), r, access)
		if err != nil {
			t.Fatal(err)
		}

		r.State = step.State()

		return step
	}

	_ = acquire(reg)
	_ = acquire(reg)

	page := acquire(reg)
	if page.State().IsZero() || page.Coverage().RequestedFrom() != "" ||
		page.Coverage().RequestedTo() != "2026-10-21" || page.Coverage().ReportedTo() != "" {
		t.Fatal("expected a partial scan with only a requested upper bound")
	}

	restarted, err := Registration(options)
	if err != nil {
		t.Fatal(err)
	}

	r.Now = r.Now.Add(15 * time.Second)

	mode.Store(1)

	throttle := acquire(restarted)
	want := throttle.NextEligibleAt()
	if throttle.RetryReason() != provider.RetryThrottled || want.Before(r.Now.Add(90*time.Second)) ||
		!access.deadlines[clientPacing].Equal(want) || !access.deadlines[accountPacing].Equal(want) {
		t.Fatalf("throttle=%+v deadlines=%v", throttle, access.deadlines)
	}

	mode.Store(2)

	r.Now = want
	resume := r.State

	moved := acquire(restarted)
	if moved.FailureCode() != "snaptrade-history-shifted" || !moved.Artifact().IsZero() {
		t.Fatal("shifted history advanced the scan")
	}

	mode.Store(0)

	r.State = resume
	r.Now = moved.NextEligibleAt()
	done := acquire(restarted)
	if !done.State().IsZero() || !done.PollCompleted() || !done.NextEligibleAt().Equal(r.Now) {
		t.Fatal("completed history did not schedule a daily scan")
	}

	sink := &testClaims{}
	for _, artifact := range []provider.Artifact{page.Artifact(), done.Artifact()} {
		if _, err = reg.Normalizer().
			Normalize(t.Context(), artifact.WithSourceAccount(r.SourceAccount), sink); err != nil {
			t.Fatal(err)
		}
	}

	if len(sink.observations) != 3 || len(sink.rejected) != 1 || sink.rejected[0] != "/data/1" {
		t.Fatalf("unexpected observations or diagnostics: %d %v", len(sink.observations), sink.rejected)
	}

	trade, err := sink.observations[0].Trade()
	if err != nil {
		t.Fatal(err)
	}

	f := trade.Fields()
	if f.Kind != source.ReportedTrade || f.Quantity.Value() != "1.230000000000000001" ||
		f.Price.Amount() != "4.56700" ||
		len(f.Charges) != 1 ||
		f.Charges[0].Total().Amount() != "0.000" ||
		f.Net.Amount() != "" ||
		f.ExecutedAt.String() != "2026-10-20T15:30:00.1200Z" ||
		f.SettlementDate.String() != "2026-10-21" ||
		!f.EventDistinct ||
		f.Event.Value() != "activity-a" ||
		len(f.EventAliases) != 0 {
		t.Fatalf("equity terms=%+v", f)
	}

	option, err := sink.observations[1].Trade()
	if err != nil {
		t.Fatal(err)
	}

	terms := option.Fields().Instrument.Option().Fields()
	if option.Fields().PositionEffect != source.Closing || terms.Right != source.Put ||
		terms.Strike.Amount() != "240.00" || terms.Multiplier.Value() != "10" || len(terms.Deliverables) != 0 {
		t.Fatalf("option terms=%+v", terms)
	}

	fidelityRef, err := (accountScope{Broker: fidelity}).reference("account-fidelity")
	if err != nil {
		t.Fatal(err)
	}

	fidelitySink := &testClaims{}
	_, err = reg.Normalizer().
		Normalize(t.Context(), page.Artifact().WithSourceAccount(fidelityRef), fidelitySink)
	if err != nil || len(fidelitySink.observations) != 2 {
		t.Fatalf("Fidelity replay: %v", err)
	}

	trade, err = fidelitySink.observations[0].Trade()
	if err != nil || !trade.Fields().ExecutedAt.IsZero() || trade.Fields().TradeDate.String() != "2026-10-20" {
		t.Fatalf("Fidelity date: %v", err)
	}
}

func TestUnsafeResponsesAreNeverRetained(t *testing.T) {
	for _, body := range []string{
		`{"error":"secret-key"}`, `{"error":"\u0073ecret-key"}`,
		`{"id":"a","ID":"b"}`, `{}[]`,
	} {
		t.Run(body, func(t *testing.T) {
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, body)
			}))
			defer srv.Close()

			reg, err := Registration(Options{HTTPClient: srv.Client(), BaseURL: srv.URL})
			if err != nil {
				t.Fatal(err)
			}

			settings, err := provider.NewSettings(map[string]string{"client_id": "local"})
			if err != nil {
				t.Fatal(err)
			}

			result, err := reg.Discoverer().Discover(t.Context(),
				provider.Request{
					Stream:   streamID,
					Settings: settings,
					Now:      time.Now(),
				}, testAccess{key: "secret-key"})
			if err != provider.ErrMalformed || !result.Step().Artifact().IsZero() {
				t.Fatalf("unsafe response: %v", err)
			}
		})
	}
}
