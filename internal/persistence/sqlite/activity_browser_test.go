// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

//go:build browser

package persistence

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/testbrowser"
	"go.trippwill.dev/wake/internal/transport/admin"
)

func TestBrowserActivityPerformance(t *testing.T) {
	if os.Getenv("WAKE_ACTIVITY_PERFORMANCE") == "" {
		t.Skip("run mise run performance:activity-browser")
	}

	browser := os.Getenv("WAKE_BROWSER")
	if browser == "" {
		for _, name := range []string{"brave-browser", "chromium", "google-chrome"} {
			if path, err := exec.LookPath(name); err == nil {
				browser = path
				break
			}
		}
	}

	if browser == "" {
		t.Fatal("Chromium required; set WAKE_BROWSER")
	}

	const count = 20000

	mode := os.Getenv("WAKE_ACTIVITY_PERFORMANCE")
	kindBenchmark := mode == "kind" || mode == "ingest"

	var s *Store

	if kindBenchmark {
		s = seedActivityKinds(t, count)
	} else {
		s = seedPerformanceRead(t, count)
		// Use distinct prices and quantities and varied dates. Insert the exact
		// search marker into 1% of prices to exercise a selective search.
		if _, err := s.db.ExecContext(t.Context(), `
			UPDATE published_resources SET snapshot=CAST(json_set(CAST(snapshot AS TEXT),
			 '$.activity.price.amount', CASE WHEN CAST(substr(id,13) AS INTEGER)%100=0 THEN '17.123456789123456789'
			  ELSE printf('%d.%06d',(CAST(substr(id,13) AS INTEGER)*7919)%1831, (CAST(substr(id,13) AS INTEGER)*4231)%1000000) END,
			 '$.activity.quantity.value',printf('%d.%05d',(CAST(substr(id,13) AS INTEGER)*503)%421, CAST(substr(id,13) AS INTEGER)),
			 '$.activity.date',printf('2026-%02d-%02d',1+CAST(substr(id,13) AS INTEGER)%10,1+CAST(substr(id,13) AS INTEGER)%27)) AS BLOB)
			WHERE kind='activity' AND id LIKE 'performance-%'
		`); err != nil {
			t.Fatal(err)
		}
	}

	var ingesting, overlapped atomic.Bool

	origin, cookie := activityPerformanceServer(t, s, func(r *http.Request) {
		if r.URL.Path == "/grids/activity" && ingesting.Load() {
			overlapped.Store(true)
		}
	})

	opts := append(slices.Clone(chromedp.DefaultExecAllocatorOptions[:]), chromedp.ExecPath(browser))

	allocator, stop := chromedp.NewExecAllocator(t.Context(), opts...)
	defer stop()

	tab, closeTab := chromedp.NewContext(allocator)
	defer closeTab()

	ctx, cancel := context.WithTimeout(tab, 5*time.Minute)
	defer cancel()

	run := func(actions ...chromedp.Action[chromedp.Void]) {
		t.Helper()

		if err := chromedp.Do(ctx, actions...); err != nil {
			t.Fatal(err)
		}
	}

	const ready = `document.querySelector('[data-grid="activity"][data-grid-ready="true"]:not([aria-busy]) .tabulator-row:not(.tabulator-group)') !== null`

	initialPage := origin + "/activity?size=50"
	if mode == "ingest" {
		initialPage += "&sort=" + os.Getenv("WAKE_ACTIVITY_PERFORMANCE_SELECTION") + "&dir=desc"
	}

	run(
		testbrowser.Command(
			network.SetCookie,
			network.SetCookieParams{Name: cookie.Name, Value: cookie.Value, URL: origin, HTTPOnly: new(true)},
		),
		chromedp.Navigate(initialPage),
		chromedp.Poll[chromedp.Void](ready, chromedp.WithPollingTimeout(10*time.Second)),
	)

	// Timestamp the event handled by the grid. A new request must remove the
	// ready marker before Tabulator reinstalls it. Two animation frames after
	// rows exist allow the browser a paint opportunity before recording time.
	measure := func(eventName, selector string, action chromedp.Action[chromedp.Void]) float64 {
		t.Helper()
		run(chromedp.Evaluate[chromedp.Void](`(() => {
		  const grid = document.querySelector('[data-grid="activity"]');
		  delete grid.dataset.activitySample;
		  document.addEventListener(` + fmt.Sprintf("%q", eventName) + `, (event) => {
            window.activityTarget = event.target.outerHTML.slice(0, 150);
            if (!event.target.closest(` + fmt.Sprintf("%q", selector) + `)) return;
		    const start = performance.now();
		    let requested = false;
		    const observer = new MutationObserver(() => {
		      requested ||= !grid.hasAttribute('data-grid-ready') || grid.hasAttribute('aria-busy');
		      if (!requested || !(` + ready + `)) return;
		      observer.disconnect();
		      requestAnimationFrame(() => requestAnimationFrame(() => {
		        grid.dataset.activitySample = String(performance.now() - start);

		      }));
		    });
		    observer.observe(grid, {attributeFilter:['data-grid-ready','aria-busy']});
		  }, {capture:true, once:true});
		})()`))

		var elapsed float64
		if err := chromedp.Do(
			ctx,
			action,
			testbrowser.Poll(
				`Number(document.querySelector('[data-grid="activity"]').dataset.activitySample)`,
				&elapsed,
				chromedp.WithPollingMutation(),
				chromedp.WithPollingTimeout(10*time.Second),
			),
		); err != nil {
			var state string

			_ = chromedp.Do(ctx, testbrowser.Evaluate(`JSON.stringify({
			 url: location.href, target: window.activityTarget,
			 ready: `+ready+`, sample: document.querySelector('[data-grid="activity"]').dataset.activitySample,
			 status: document.querySelector('[data-grid-status]')?.textContent
			})`, &state))
			t.Fatalf("%s: %v (%s)", selector, err, state)
		}

		return elapsed
	}

	samples := 30
	if configured := os.Getenv("WAKE_ACTIVITY_PERFORMANCE_SAMPLES"); configured != "" {
		var err error

		samples, err = strconv.Atoi(configured)
		if err != nil || samples < 1 {
			t.Fatal("invalid activity sample count", configured)
		}
	}

	report := func(name string, durations []float64) {
		t.Helper()
		t.Logf("activity-browser path=%s rows=%d samples_ms=%v", name, count, durations)
		slices.Sort(durations)
		p95 := durations[(95*len(durations)+99)/100-1]

		t.Logf("activity-browser path=%s n=%d p50_ms=%.1f p95_ms=%.1f",
			name, len(durations), durations[(len(durations)-1)/2], p95)

		if mode == "ingest" && p95 > 150 {
			t.Errorf("%s p95 %.1f ms exceeds the 150 ms target", name, p95)
		}
	}

	if mode == "ingest" {
		measureActivityIngestion(t, s, run, measure, report, samples, count, &ingesting, &overlapped)

		return
	}

	if kindBenchmark {
		measureActivityKinds(t, run, measure, report, samples, count)

		return
	}

	click := func(selector string) float64 {
		return measure("click", selector, chromedp.Click(chromedp.CSS(selector)))
	}

	fields := []string{"date", "quantity", "price"}
	dateOnly := os.Getenv("WAKE_ACTIVITY_PERFORMANCE_SELECTION") == "date"

	if dateOnly {
		fields = fields[:1]
	}

	for _, field := range fields {
		selector := fmt.Sprintf(`.tabulator-col[tabulator-field="%s"]`, field)
		click(selector) // warm the path before collecting samples

		durations := make([]float64, samples)
		for i := range durations {
			durations[i] = click(selector)
		}

		report("sort-"+field, durations)
	}

	if dateOnly {
		return
	}

	searches := []struct{ name, value string }{
		{"search-broad", "USD"},
		{"search-selective", "17.123456789123456789"},
	}
	searchTimes := make([][]float64, len(searches))
	for i := range samples + 1 {
		for j, search := range searches {
			run(chromedp.SetValue(chromedp.CSS("#activity-search"), search.value))

			duration := click(`[data-list-filters] button[type="submit"]`)
			if i > 0 {
				searchTimes[j] = append(searchTimes[j], duration)
			}
		}
	}

	for i, search := range searches {
		report(search.name, searchTimes[i])
	}
}

var activityKindSizes = []struct {
	kind string
	size int
}{
	{"trade", 12000},
	{"cash", 5000},
	{"security-transfer", 2000},
	{"lifecycle", 900},
	{"conversion", 100},
}

func seedActivityKinds(t *testing.T, total int) *Store {
	t.Helper()

	s := coverageStore(t)
	lifecycle := strings.Replace(
		syntheticOption,
		`transactionType="ExchTrade"`,
		`transactionType="BookTrade" code="A"`,
		1,
	)
	coverageReport(t, s, "mixed-kinds", equityRow("U1", "kind-trade", "123.45")+lifecycle+syntheticFX)
	acquireRealized(t, s, movementReport("Transfers", transferRow))
	acquireRealized(t, s, movementReport("CashTransactions", cashRow))

	ctx := t.Context()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = tx.Rollback() }()

	for _, family := range activityKindSizes {
		var template string
		if err = tx.QueryRowContext(ctx, "SELECT id FROM executions WHERE kind=?", family.kind).
			Scan(&template); err != nil {
			t.Fatal(family.kind, err)
		}

		// Keep one real projection per family, then copy only its read-model rows.
		result, err := tx.ExecContext(ctx, `
WITH RECURSIVE n(i) AS (SELECT 0 UNION ALL SELECT i+1 FROM n WHERE i+1<?)
INSERT INTO executions(revision,kind,id,account_id,instrument_id,listing_id,accepted,status,review_reasons,selection_authority,rule)
SELECT e.revision,e.kind,printf('kind-%s-%08d',e.kind,n.i),e.account_id,e.instrument_id,e.listing_id,e.accepted,
 e.status,e.review_reasons,e.selection_authority,e.rule
FROM n JOIN executions e ON e.id=?`, family.size-1, template)
		if err != nil {
			t.Fatal(family.kind, err)
		}

		if affected, err := result.RowsAffected(); err != nil || affected != int64(family.size-1) {
			t.Fatal(family.kind, affected, err)
		}

		if _, err = tx.ExecContext(ctx, `
INSERT INTO published_resources(kind,id,revision,snapshot,explanation)
SELECT 'activity',e.id,p.revision,CAST(json_set(CAST(p.snapshot AS TEXT),'$.id',e.id,
 '$.activity.date',printf('2026-%02d-%02d',1+e.rowid%10,1+e.rowid%27)) AS BLOB),p.explanation
FROM executions e JOIN published_resources p ON p.kind='activity' AND p.id=?
WHERE e.kind=? AND e.id<>?`, template, family.kind, template); err != nil {
			t.Fatal(family.kind, err)
		}
	}

	var published int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM published_resources WHERE kind='activity'").
		Scan(&published); err != nil || published != total {
		t.Fatal(published, err)
	}

	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}

	return s
}

func measureActivityKinds(
	t *testing.T,
	run func(...chromedp.Action[chromedp.Void]),
	measure func(string, string, chromedp.Action[chromedp.Void]) float64,
	report func(string, []float64),
	samples, total int,
) {
	t.Helper()

	const selector = "#activity-kind"

	kinds := append(slices.Clone(activityKindSizes), struct {
		kind string
		size int
	}{"", total})

	only := os.Getenv("WAKE_ACTIVITY_PERFORMANCE_SELECTION")

	for _, kind := range kinds {
		name := kind.kind
		if name == "" {
			name = "all"
		}

		if only != "" && only != name {
			continue
		}

		durations := make([]float64, 0, samples)
		for i := range samples + 1 {
			// Start from a different kind before each timed selection.
			previous := ""
			if kind.kind == "" {
				previous = "trade"
			}

			for _, value := range []string{previous, kind.kind} {
				var selected string
				run(testbrowser.Capture(&selected, chromedp.Value(chromedp.CSS(selector))))

				if selected == value {
					continue
				}

				elapsed := measure("change", selector, selectActivityKind(value))
				if value == kind.kind && i > 0 {
					durations = append(durations, elapsed)
				}
			}

			var rendered []string
			run(testbrowser.Evaluate(`[
 new URL(location.href).searchParams.get('kind') || '',
 document.querySelector('[data-grid-status]').textContent
]`, &rendered))

			wantStatus := fmt.Sprintf("%d of %d results", kind.size, total)
			if !slices.Equal(rendered, []string{kind.kind, wantStatus}) {
				t.Fatalf("kind %q expected %q, got %v", kind.kind, wantStatus, rendered)
			}
		}

		report("kind-"+name, durations)
	}
}

func selectActivityKind(value string) chromedp.Action[chromedp.Void] {
	return chromedp.Evaluate[chromedp.Void](fmt.Sprintf(`(() => {
 const select = document.querySelector('#activity-kind');
 select.value = %q;
 select.dispatchEvent(new Event('change', {bubbles: true}));
})()`, value))
}

func activityPerformanceServer(t *testing.T, s *Store, observe func(*http.Request)) (string, *http.Cookie) {
	t.Helper()

	server := httptest.NewUnstartedServer(nil)
	t.Cleanup(server.Close)
	origin := "http://" + server.Listener.Addr().String()

	dir := t.TempDir()

	const unlock = "synthetic-activity-benchmark"
	if err := admin.SetUnlock(dir, []byte(unlock)); err != nil {
		t.Fatal(err)
	}

	auth, enabled, err := admin.Open(dir, "local", origin)
	if err != nil || !enabled {
		t.Fatal(enabled, err)
	}

	t.Cleanup(auth.Close)

	stopping := make(chan struct{})
	t.Cleanup(func() { close(stopping) })

	handler, err := admin.NewHandler(auth, admin.Dependencies{
		Stopping: stopping,
		Workflow: application.New(s, nil, nil, nil, nil),
	})
	if err != nil {
		t.Fatal(err)
	}

	server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		observe(r)
		handler.ServeHTTP(w, r)
	})
	server.Start()

	client := server.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	form := url.Values{"unlock": {unlock}}

	req, err := http.NewRequestWithContext(
		t.Context(),
		http.MethodPost,
		origin+"/login",
		strings.NewReader(form.Encode()),
	)
	if err != nil {
		t.Fatal(err)
	}

	req.Header.Set("Origin", origin)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = resp.Body.Close() }()

	cookies := resp.Cookies()
	if resp.StatusCode != http.StatusSeeOther || len(cookies) != 1 {
		t.Fatal("activity benchmark login failed", resp.StatusCode)
	}

	return origin, cookies[0]
}
