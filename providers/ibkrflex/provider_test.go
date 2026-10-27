// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package ibkrflex

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"go.trippwill.dev/wake/provider"
	"go.trippwill.dev/wake/source"
)

type testAccess struct {
	token    []byte
	now      time.Time
	next     time.Time
	progress string
}

func (a *testAccess) WithSecret(ctx context.Context, _ string, fn func([]byte) error) error {
	return fn(a.token)
}

func (a *testAccess) Report(_ context.Context, p provider.Progress) error {
	a.progress = p.Code()
	return nil
}

func (a *testAccess) ReservePacing(
	_ context.Context,
	_ string,
	_ provider.PacingScope,
) (provider.Reservation, error) {
	if a.next.After(a.now) {
		return provider.NewReservation(false, a.next)
	}

	a.next = a.now.Add(6 * time.Second)

	return provider.NewReservation(true, a.next)
}

func (a *testAccess) DelayPacing(_ context.Context, _ string, _ provider.PacingScope, next time.Time) error {
	if next.After(a.next) {
		a.next = next
	}

	return nil
}

type testClaims struct {
	trades   []source.Trade
	rejected []string
	failure  error
}

func (c *testClaims) PutTrade(_ context.Context, t source.Trade) error {
	if c.failure != nil {
		return c.failure
	}

	c.trades = append(c.trades, t)

	return nil
}

func (c *testClaims) PutInstrument(context.Context, source.InstrumentObservation) error {
	return c.failure
}

func (c *testClaims) Reject(_ context.Context, _, reason string) error {
	if c.failure != nil {
		return c.failure
	}

	c.rejected = append(c.rejected, reason)

	return nil
}

func TestConfirmationCoverageAndExactTerms(t *testing.T) {
	r, err := Registration(Options{})
	if err != nil {
		t.Fatal(err)
	}

	if len(r.Descriptor().Streams()) != 1 || r.Descriptor().Streams()[0].ID() != Confirmations {
		t.Fatal("exposed deferred Activity capability")
	}

	for _, file := range []string{"initial.xml", "overlap.xml"} {
		//nolint:gosec // Fixed repository fixture names.
		body, e := os.ReadFile("../../testdata/ibkrflex/confirmations/" + file)
		if e != nil {
			t.Fatal(e)
		}

		artifact, e := provider.NewArtifact(ConfirmationFormat, "application/xml", time.Now(), body)
		if e != nil {
			t.Fatal(e)
		}

		claims := &testClaims{}

		result, e := r.Normalizer().Normalize(context.Background(), artifact, claims)
		if e != nil {
			t.Fatal(e)
		}

		expected := 3
		if file == "overlap.xml" {
			expected = 2
		}

		if len(claims.trades) != expected || result.Rejected() == 0 {
			t.Fatal("coverage", file, result.Counts())
		}

		for _, trade := range claims.trades {
			fields := trade.Fields()
			if fields.Quantity.Value() == "" || fields.Price.Amount() == "" ||
				fields.Account.Authority() != "ibkr" ||
				fields.Event.Kind() != "execution-id" ||
				fields.EventAliases[0].Kind() != "trade-id" {
				t.Fatal("lost qualified terms", fields)
			}
		}

		fail := &testClaims{failure: provider.ErrConflict}
		if _, e = r.Normalizer().
			Normalize(context.Background(), artifact, fail); !errors.Is(
			e,
			provider.ErrConflict,
		) {
			t.Fatal("sink storage failure became row rejection", e)
		}
	}
}

func TestBoundedProtocolGenerationAndRepair(t *testing.T) {
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	code := "1019"
	calls := 0

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++

		if strings.HasSuffix(r.URL.Path, "SendRequest") {
			if r.URL.Query().Get("p") != "365" {
				t.Error("initial range", r.URL.Query())
			}

			_, _ = fmt.Fprint(
				w,
				`<FlexStatementResponse><Status>Success</Status><ReferenceCode>generation</ReferenceCode></FlexStatementResponse>`,
			)
		} else {
			_, _ = fmt.Fprintf(
				w,
				`<FlexStatementResponse><Status>Fail</Status><ErrorCode>%s</ErrorCode><ErrorMessage>private text must not escape</ErrorMessage></FlexStatementResponse>`,
				code,
			)
		}
	}))
	defer server.Close()

	reg, err := Registration(Options{HTTPClient: server.Client(), BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}

	settings, _ := provider.NewSettings(map[string]string{"name": "test", "query_id": "123"})
	access := &testAccess{token: []byte("synthetic-token"), now: now}
	request := provider.Request{Stream: Confirmations, Settings: settings, Now: now}

	step, err := reg.Acquirer().Acquire(context.Background(), request, access)
	if err != nil || step.State().IsZero() || access.progress != "generating" {
		t.Fatal("generation not retained", err)
	}

	request.State = step.State()
	request.Now = now.Add(time.Second)

	access.now = request.Now
	step, err = reg.Acquirer().Acquire(context.Background(), request, access)
	if err != nil || calls != 1 || access.progress != "paced" {
		t.Fatal("pacing", err, calls)
	}

	request.State = step.State()
	request.Now = now.Add(10 * time.Second)

	access.now = request.Now
	step, err = reg.Acquirer().Acquire(context.Background(), request, access)
	if err != nil || step.FailureCode() != "" || access.progress != "ibkr-1019" {
		t.Fatal("generation polling", err)
	}

	state, err := readContinuation(step.State(), "123")
	if err != nil || state.Reference != "generation" {
		t.Fatal("lost polling continuation", err)
	}

	code = "1018"
	request.State = step.State()

	request.Now = request.Now.Add(20 * time.Second)
	access.now = request.Now
	step, err = reg.Acquirer().Acquire(context.Background(), request, access)
	if err != nil || step.FailureCode() != "ibkr-1018" ||
		step.NextEligibleAt().Before(request.Now.Add(time.Minute)) {
		t.Fatal("shared cooldown", step, err)
	}

	code = "1012"

	request.State = step.State()
	request.Now = step.NextEligibleAt()
	access.now = request.Now
	if _, err = reg.Acquirer().
		Acquire(context.Background(), request, access); !errors.Is(
		err,
		provider.ErrUnauthorized,
	) {
		t.Fatal("credential failure", err)
	}

	saved, _ := provider.NewState("unknown", []byte(`{}`))

	request.State = saved
	if _, err = reg.Acquirer().
		Acquire(context.Background(), request, access); !errors.Is(
		err,
		provider.ErrUnavailable,
	) {
		t.Fatal("reset unknown continuation", err)
	}
}

func TestUntrustedXMLAndTimestampLimits(t *testing.T) {
	for _, body := range []string{`<!DOCTYPE a><a/>`, `<a x="1" x="2"/>`, `<a/><b/>`, strings.Repeat("<a>", 65) + strings.Repeat("</a>", 65), `<a xmlns="unexpected"/>`} {
		if err := walk(context.Background(), []byte(body), nil); err == nil {
			t.Fatal("accepted unsafe XML", body)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := walk(ctx, []byte(`<a/>`), nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}

	for _, value := range []string{"2026-10-20;09:30:00.1234567890 EDT", "2026-07-01;09:30:00 EST", "2026-10-20;09:30:00 UNKNOWN", "2026-10-20;09:30:00"} {
		if _, err := parseReportedTimestamp(value); err == nil {
			t.Fatal("invented time or precision", value)
		}
	}
}

func TestCatchupAndUnavailableRanges(t *testing.T) {
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	widened := continuation{Query: "123", Started: true, LastTo: "2026-10-10"}
	widened.choose(now, Confirmations)

	if widened.Period <= 2 || widened.Period > 365 {
		t.Fatal("did not widen after downtime", widened)
	}

	state := continuation{Query: "123", Started: true, LastTo: "2024-01-01"}
	state.choose(now, Confirmations)

	if state.Current == nil || len(state.Queue) == 0 {
		t.Fatal("no dated catchup", state)
	}

	if !state.Current.valid() {
		t.Fatal("unbounded catchup window")
	}

	state.Current = &window{"2026-10-20", "2026-10-20"}
	state.unavailable(now)

	if len(state.Missing) != 1 || state.Missing[0].Window.From != "2026-10-20" {
		t.Fatal("lost unavailable gap")
	}

	step, err := state.step(provider.Artifact{}, now)
	if err != nil {
		t.Fatal(err)
	}

	restored, err := readContinuation(step.State(), "123")
	if err != nil || len(restored.Missing) != 1 {
		t.Fatal("restart lost unavailable gap", err)
	}

	restored.choose(now, Confirmations)

	if restored.Period != 2 || !restored.Recent {
		t.Fatal("older unavailable gaps blocked recent work", restored)
	}

	state = continuation{Query: "123", Started: true}
	for day := 0; day < 366; day++ {
		date := civil(now).AddDate(0, 0, -day).Format("2006-01-02")
		state.Current = &window{date, date}
		state.unavailable(now)
	}

	if len(state.Missing) != 2 {
		t.Fatal("unavailable range compaction", len(state.Missing))
	}

	for _, missing := range state.Missing {
		if !missing.Window.valid() {
			t.Fatal("unbounded unavailable retry", missing)
		}
	}
}
