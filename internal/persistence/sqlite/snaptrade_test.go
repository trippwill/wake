// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/credential"
	"go.trippwill.dev/wake/internal/host"
	"go.trippwill.dev/wake/provider"
	"go.trippwill.dev/wake/providers/snaptrade"
	"go.trippwill.dev/wake/source"
)

func TestSnapTradeResumesRetainedPageAndPacingAfterStoreRestart(t *testing.T) {
	var (
		mu                 sync.Mutex
		offsets            []string
		positionsResponse  atomic.Int32
		initialSyncPending atomic.Bool
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
			status := ""
			if initialSyncPending.Load() {
				status = `,"sync_status":{"positions":null}`
			}

			_, _ = fmt.Fprintf(
				w,
				`{"results":[{"id":"account-a","connection_id":"auth-a","institution_id":"broker-a","display_name":"Fidelity fixture","kind":"investment"%s}]}`,
				status,
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
		case "/accounts/account-a/positions/all":
			query := r.URL.Query()
			mac := hmac.New(sha256.New, []byte("synthetic-secret"))
			_, _ = fmt.Fprintf(mac, `{"content":null,"path":%q,"query":%q}`, r.URL.Path, r.URL.RawQuery)
			wantSignature := base64.StdEncoding.EncodeToString(mac.Sum(nil))

			if r.Method != http.MethodGet || len(query) != 2 || query.Get("clientId") != "synthetic-client" ||
				query.Get("timestamp") == "" || r.Header.Get("Signature") != wantSignature {
				t.Error("invalid signed positions request")
			}

			switch positionsResponse.Load() {
			case 0:
				w.Header().Set("Retry-After", "90")
				w.WriteHeader(http.StatusTooManyRequests)
			case 1:
				_, _ = io.WriteString(
					w,
					`{"results":[{"instrument":{"kind":"bond","id":"bond-a","symbol":"BOND","raw_symbol":"BOND"},"units":"12.3400"},{"instrument":{"kind":"other","id":"unclassified-a","symbol":"SYNXX","raw_symbol":"SYNXX","description":"Synthetic cash fund","currency":"USD"},"units":"42.5600","cash_equivalent":true}],"data_freshness":{"as_of":"2026-10-20T15:00:00.1200Z"}}`,
				)
			case 2:
				_, _ = io.WriteString(w, `{"results":[]}`)
			case 3:
				_, _ = io.WriteString(w, `{"results":[],"data_freshness":{"as_of":"2026-10-20T15:00:00.1200Z"}}`)
			case 4:
				w.Header().Set("X-RateLimit-Account-Remaining", "0")
				w.Header().Set("X-RateLimit-Account-Reset", "10800")
				_, _ = io.WriteString(w, `{"results":[],"data_freshness":{"as_of":"2026-10-21T15:00:00.1200Z"}}`)
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

		s.registrations = []provider.Registration{registration}

		h, err := host.New(ctx, s, envelope, nil, clock, nil, registration)
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

	// Emulate an existing confirmed account from before the positions stream shipped.
	if _, err = s.db.ExecContext(
		ctx,
		`DELETE FROM scheduled WHERE stream='positions'; DELETE FROM work WHERE stream='positions'`,
	); err != nil {
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
		if activity.AccountID != account.ID() || activity.Trade.Price.Amount != "1.1200" ||
			activity.Trade.TradeKind == nil || *activity.Trade.TradeKind != "reported-trade" {
			t.Fatalf("retained activity lost its scope or terms: %+v", activity)
		}

		why, err := s.Explanation(ctx, "activity", item.Resource.ID, item.Resource.Revision)
		if err != nil || !slices.ContainsFunc(why.Fields, func(field application.PublishedFieldDetail) bool {
			return field.Path == "/net_cash_with_billable" && field.Reason == application.FieldNotReported &&
				field.Expectation != nil && *field.Expectation == provider.NotExpected && field.Interpretation != nil
		}) {
			t.Fatalf("expected a source reporting explanation: %+v %v", why.Fields, err)
		}
	}

	activityKey := host.WorkKey{Connection: id, Stream: "activities", Operation: "acquire"}

	before, err := s.Checkpoint(ctx, activityKey)
	if err != nil {
		t.Fatal(err)
	}

	if err = s.ScheduleMissingAcquisition(ctx, "snaptrade", "positions"); err != nil {
		t.Fatal(err)
	}

	after, err := s.Checkpoint(ctx, activityKey)
	if err != nil || after.Revision != before.Revision || !after.Next.Equal(before.Next) {
		t.Fatalf("upgrade altered trade checkpoint: %+v %v", after, err)
	}

	positionsKey := host.WorkKey{Connection: id, Stream: "positions", Operation: "acquire"}

	initialSyncPending.Store(true)
	advance(time.Second)
	advance(time.Second)

	pending, err := s.Checkpoint(ctx, positionsKey)
	if err != nil || pending.Acknowledged.Phase() != provider.ProgressWaiting ||
		!pending.Next.Equal(now.Add(time.Hour)) {
		t.Fatalf("initial sync did not defer positions: %+v %v", pending, err)
	}

	initialSyncPending.Store(false)
	advance(time.Hour)
	advance(time.Second)
	advance(15 * time.Second)

	retry, err := s.Checkpoint(ctx, positionsKey)
	if err != nil || retry.Failure != "snaptrade-throttled" || retry.Next.Before(now.Add(90*time.Second)) {
		t.Fatalf("missing positions retry: %+v %v", retry, err)
	}

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
	if err = s.ScheduleMissingAcquisition(ctx, "snaptrade", "positions"); err != nil {
		t.Fatal(err)
	}

	resumed, err := s.Checkpoint(ctx, positionsKey)
	if err != nil || resumed.Revision != retry.Revision || resumed.State.IsZero() ||
		!resumed.Next.Equal(retry.Next) {
		t.Fatalf("restart changed the retry: %+v %v", resumed, err)
	}

	read := func() application.PositionsView {
		t.Helper()

		got, err := s.Positions(ctx, account.ID(), application.SnapshotCondition{})
		if err != nil {
			t.Fatal(err)
		}

		return got.View
	}

	positionsResponse.Store(1)
	advance(retry.Next.Sub(now))

	first := read()
	if first.Status != "available" || first.AsOf == nil || first.AsOf.Basis != source.PositionSourceFetched ||
		len(first.Positions) != 2 {
		t.Fatalf("lost normalized positions: %+v", first)
	}

	for _, quantity := range []string{"12.3400", "42.5600"} {
		if !slices.ContainsFunc(first.Positions, func(row application.PublishedPosition) bool {
			return row.Quantity.Unit == "units" && row.Quantity.Value == quantity
		}) {
			t.Fatalf("missing exact reported position quantity %s", quantity)
		}
	}

	poll, err := s.Checkpoint(ctx, positionsKey)
	if err != nil || !poll.Next.Equal(now.Add(24*time.Hour)) {
		t.Fatalf("positions default polling deadline: %+v %v", poll, err)
	}

	if err = h.UpdatePolling(ctx, id, positionsKey.Stream, "2h"); err != nil {
		t.Fatal(err)
	}

	poll, err = s.Checkpoint(ctx, positionsKey)
	if err != nil || !poll.Next.Equal(now.Add(2*time.Hour)) {
		t.Fatalf("positions override did not reschedule poll: %+v %v", poll, err)
	}

	// Retry only the positions stream to exercise distinct receipts without advancing the trade scan.
	for _, scenario := range []struct {
		mode           int32
		status, reason string
	}{
		{1, "available", ""},
		{2, "available", ""},
		{3, "unavailable", "conflict"},
		{4, "available", ""},
	} {
		positionsResponse.Store(scenario.mode)

		if err = h.Retry(ctx, positionsKey); err != nil {
			t.Fatal(err)
		}

		advance(time.Second)
		advance(time.Second)
		advance(15 * time.Second)

		got := read()
		if got.Status != scenario.status ||
			(scenario.reason != "" && (got.Reason == nil || *got.Reason != scenario.reason)) ||
			(scenario.mode <= 2 && got.Version != first.Version) ||
			(scenario.mode == 4 && (got.Positions == nil || len(got.Positions) != 0)) {
			t.Fatalf("mode %d: unexpected view %+v", scenario.mode, got)
		}
	}

	paced, err := s.Checkpoint(ctx, positionsKey)
	if err != nil || paced.Next.Before(now.Add(3*time.Hour)) {
		t.Fatalf("positions poll ignored the upstream pacing deadline: %+v %v", paced, err)
	}

	if err = h.UpdatePolling(ctx, id, positionsKey.Stream, "1h"); err != nil {
		t.Fatal(err)
	}

	poll, err = s.Checkpoint(ctx, positionsKey)
	if err != nil || !poll.Next.Equal(paced.Next) {
		t.Fatalf("positions override moved poll ahead of pacing: %+v %v", poll, err)
	}

	h.Close()
	srv.Close()

	t.Run("attended migration preserves explanations offline", func(t *testing.T) {
		verifyUpgradeExplanation(t, s, testProfile(path), list.Items[0].Resource.ID)
	})
}

func verifyUpgradeExplanation(t *testing.T, s *Store, profile Profile, id string) {
	t.Helper()

	ctx := t.Context()

	before, err := explainCurrent(ctx, s, "activity", id)
	if err != nil {
		t.Fatal(err)
	}

	if err = s.Close(); err != nil {
		t.Fatal(err)
	}

	registration, err := snaptrade.Registration(snaptrade.Options{})
	if err != nil {
		t.Fatal(err)
	}

	profile.Registrations = []provider.Registration{registration}
	baseline := fixtureUpgrade(true, false, false)
	baseline.migrations[0][0].sql += `
INSERT INTO publishing_dirty SELECT kind,id FROM published_resources WHERE kind='activity' ON CONFLICT DO NOTHING;`
	plan := previewFixture(t, profile, baseline)

	migrated, err := openStore(ctx, profile, nil, baseline, upgradeOptions{confirm: plan.ID})
	if err != nil {
		t.Fatal(err)
	}

	if err = migrated.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := openStore(ctx, profile, nil, baseline, upgradeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()

	after, err := explainCurrent(ctx, reopened, "activity", id)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("migration changed the activity explanation", before, after, err)
	}
}
