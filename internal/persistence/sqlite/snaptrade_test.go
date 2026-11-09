// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/credential"
	"go.trippwill.dev/wake/internal/host"
	"go.trippwill.dev/wake/provider"
	"go.trippwill.dev/wake/providers/snaptrade"
)

func TestSnapTradeResumesRetainedPageAndPacingAfterStoreRestart(t *testing.T) {
	var (
		mu      sync.Mutex
		offsets []string
	)

	row := func(id string) string {
		return fmt.Sprintf(
			`{"id":%q,"type":"BUY","units":1.001,"price":1.1200,"trade_date":"2026-10-20","currency":{"code":"USD"},"symbol":{"id":"stock-a","raw_symbol":"SYN","type":{"code":"cs"}}}`,
			id,
		)
	}

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/authorizations":
			_, _ = io.WriteString(
				w,
				`[{"id":"auth-a","disabled":false,"brokerage":{"id":"broker-a","slug":"FIDELITY"}}]`,
			)
		case "/accounts/all":
			_, _ = io.WriteString(
				w,
				`{"results":[{"id":"account-a","connection_id":"auth-a","institution_id":"broker-a","display_name":"Fidelity fixture","kind":"investment"}]}`,
			)
		case "/accounts/account-a/activities":
			if date := r.URL.Query().Get("endDate"); date != "2026-10-21" {
				t.Errorf("resumed scan changed its end date to %q", date)
			}

			offset := r.URL.Query().Get("offset")

			mu.Lock()

			offsets = append(offsets, offset)
			mu.Unlock()

			switch offset {
			case "0":
				_, _ = fmt.Fprintf(
					w,
					`{"data":[%s,%s],"pagination":{"offset":0,"limit":1000,"total":3}}`,
					row("activity-a"),
					row("activity-b"),
				)
			case "1":
				_, _ = fmt.Fprintf(
					w,
					`{"data":[%s,%s],"pagination":{"offset":1,"limit":1000,"total":3}}`,
					row("activity-b"),
					row("activity-c"),
				)
			default:
				t.Error("unexpected activity offset", offset)
				http.NotFound(w, r)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	s, path := testStore(t)
	ctx := t.Context()
	now := time.Date(2026, 10, 21, 23, 59, 45, 0, time.UTC)
	clock := func() time.Time { return now }
	envelope := testEnvelope(t)

	makeHost := func(s *Store) *host.Host {
		t.Helper()

		registration, err := snaptrade.Registration(snaptrade.Options{HTTPClient: srv.Client(), BaseURL: srv.URL})
		if err != nil {
			t.Fatal(err)
		}

		h, err := host.New(ctx, s, envelope, nil, clock, registration)
		if err != nil {
			t.Fatal(err)
		}

		return h
	}

	h := makeHost(s)
	defer h.Close()

	w := application.New(s, h, nil, clock, nil)

	advance := func(delay time.Duration) {
		t.Helper()

		now = now.Add(delay)
		if err := w.RunOnce(ctx, now); err != nil {
			t.Fatal(err)
		}
	}

	settings, err := provider.NewSettings(map[string]string{"client_id": "synthetic-client"})
	if err != nil {
		t.Fatal(err)
	}

	key := credential.NewSecret([]byte("synthetic-secret"))
	defer key.Clear()

	id, err := w.CreateConnection(
		ctx,
		"SnapTrade fixture",
		"snaptrade",
		settings,
		map[string]*credential.Secret{"consumer_key": key},
	)
	if err != nil {
		t.Fatal(err)
	}

	advance(0)
	advance(2 * time.Second)

	query, _ := application.NewListQuery(application.ListOptions{}, application.SourceScope)

	detail, err := w.SourceDetail(ctx, id, query)
	if err != nil || len(detail.Connection.Candidates) != 1 {
		t.Fatalf("discovery: %+v %v", detail, err)
	}

	candidate := detail.Connection.Candidates[0]
	accountDetails, _ := application.NewAccountDetails("Fidelity fixture", "")

	account, err := w.Confirm(ctx, id, candidate.Artifact, candidate.Candidate.Reference(), accountDetails)
	if err != nil {
		t.Fatal(err)
	}

	advance(2 * time.Second)
	advance(2 * time.Second)
	advance(2 * time.Second)

	if rows := tableCount(t, s, "executions"); rows != 2 {
		t.Fatalf("first page published %d activities", rows)
	}

	customerDeadline := now.Add(time.Second)

	h.Close()

	if err = s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = Open(ctx, testProfile(path), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	h = makeHost(s)
	defer h.Close()

	w = application.New(s, h, nil, clock, nil)

	advance(10 * time.Second)

	mu.Lock()
	got := slices.Clone(offsets)
	mu.Unlock()

	if !slices.Equal(got, []string{"0"}) {
		t.Fatalf("restart bypassed account pacing: %v", got)
	}

	customer, err := provider.NewPacingScope([]byte("synthetic-client"))
	if err != nil {
		t.Fatal(err)
	}

	deadline, err := pacingDeadline(ctx, s.queries, "snaptrade", "customer", customer.Digest())
	if err != nil || !deadline.Equal(customerDeadline) {
		t.Fatalf("account pacing consumed a customer allowance: %v %v", deadline, err)
	}

	// A different connection on the same client has used the customer allowance.
	// The account becomes ready first and must remain ready when the client cools.
	if err = s.Delay(ctx, "snaptrade", "customer", customer.Digest(), now.Add(7*time.Second)); err != nil {
		t.Fatal(err)
	}

	advance(5 * time.Second)

	mu.Lock()
	got = slices.Clone(offsets)
	mu.Unlock()

	if !slices.Equal(got, []string{"0"}) {
		t.Fatalf("scan bypassed shared customer pacing: %v", got)
	}

	advance(2 * time.Second)

	mu.Lock()
	got = slices.Clone(offsets)
	mu.Unlock()

	if !slices.Equal(got, []string{"0", "1"}) {
		t.Fatalf("restart lost the retained page boundary: %v", got)
	}

	query, _ = application.NewListQuery(
		application.ListOptions{AccountID: account.ID()},
		application.ActivityScope,
	)

	list, err := w.ActivityList(ctx, query)
	if err != nil || len(list.Items) != 3 || list.Matched != 3 {
		t.Fatalf("overlapping row changed publication multiplicity: %+v %v", list, err)
	}

	for _, item := range list.Items {
		activity := item.Resource.Activity
		if activity.AccountID != account.ID() || activity.Price.Amount != "1.1200" ||
			activity.TradeKind == nil || *activity.TradeKind != "reported-trade" {
			t.Fatalf("retained activity lost its scope or terms: %+v", activity)
		}
	}
}
