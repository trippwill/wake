// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

//go:build browser

package admin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/testbrowser"
	"go.trippwill.dev/wake/source"
)

func (s positionsReadStore) InstrumentDetail(
	_ context.Context,
	id string,
) (application.InstrumentDetail, error) {
	reported, err := source.NewInstrument(
		source.InstrumentInput{Kind: source.Equity, Name: "Synthetic equity", Ticker: "SYN"},
	)
	if err != nil {
		return application.InstrumentDetail{}, err
	}

	instrument, err := application.NewInstrument(
		id,
		"Synthetic equity",
		"SYN",
		"equity",
		"provisional",
		"",
		"",
		reported,
		"fixture",
		1,
		application.AutomaticSelection,
		application.ReferenceMetadata{},
	)

	return application.InstrumentDetail{Instrument: instrument}, err
}

func (s positionsReadStore) ObservationList(
	context.Context,
	string,
	string,
	application.ListQuery,
) (application.ObservationList, error) {
	return application.ObservationList{PageInfo: application.PageInfo{Page: 1, PageSize: 50}}, nil
}

func TestBrowserPositionsInspectionAndRefresh(t *testing.T) {
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

	data := positionsFixture(t)

	var (
		revision  atomic.Int32
		holdShort atomic.Bool
	)

	shortStarted := make(chan struct{})

	store := positionsReadStore{
		workspaceStore: workspaceStore{view: fixtureView{Account: data.Account}},
		read: func(query application.ListQuery) (application.PositionsList, error) {
			out := data

			out.Items = append([]application.PositionListItem(nil), data.Items...)
			out.Page = query.Options().Page
			out.PageSize = query.Options().PageSize
			switch revision.Load() {
			case 1:
				out.Items[0].Name = "Renamed equity" // Reference labels can change without a positions revision.
			case 2:
				out.Version = "positions-2"
				out.Status = "unavailable"
				out.Reason = new("incomplete")
				out.AsOf = &application.PositionTime{Date: "2026-11-05"}
				out.Items = nil
				out.Total = 0
				out.Matched = 0
			case 3:
				out.Version = "positions-3"
				out.AsOf = &application.PositionTime{Date: "2026-11-06", At: new("2026-11-06T10:12:13.123400+01:00")}
				out.Items = nil
				out.Total = 0
				out.Matched = 0
			case 4:
				return application.PositionsList{}, errors.New("synthetic read failure")
			}

			opts := query.Options()
			out.Items = slices.DeleteFunc(out.Items, func(item application.PositionListItem) bool {
				return opts.Kind != "" && item.Kind != opts.Kind ||
					opts.State != "" && item.Position.Side != opts.State ||
					!strings.Contains(strings.ToLower(item.Name), opts.Search)
			})
			out.Matched = int64(len(out.Items))

			return out, nil
		},
	}
	server := httptest.NewUnstartedServer(nil)
	origin := "http://" + server.Listener.Addr().String()

	dir := t.TempDir()
	if err := SetUnlock(dir, []byte("synthetic-browser-positions")); err != nil {
		t.Fatal(err)
	}

	auth, _, err := Open(dir, "local", origin)
	if err != nil {
		t.Fatal(err)
	}
	defer auth.Close()

	stopping := make(chan struct{})

	handler, err := NewHandler(
		auth,
		Dependencies{Workflow: application.New(store, nil, nil, nil, nil), Stopping: stopping},
	)
	if err != nil {
		t.Fatal(err)
	}

	server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/grids/positions" && r.URL.Query().Get("state") == "short" && holdShort.Swap(false) {
			close(shortStarted)
			<-r.Context().Done()

			return
		}

		handler.ServeHTTP(w, r)
	})
	server.Start()
	defer func() { close(stopping); server.Close() }()

	session := httptest.NewRecorder()
	auth.issue(session)
	cookie := session.Result().Cookies()[0]

	allocator, stop := chromedp.NewExecAllocator(
		context.Background(),
		append(chromedp.DefaultExecAllocatorOptions[:], chromedp.ExecPath(browser))...)
	defer stop()

	ctx, cancel := chromedp.NewContext(allocator)
	defer cancel()

	ctx, deadline := context.WithTimeout(ctx, 90*time.Second)
	defer deadline()

	check := func(actions ...chromedp.Action) {
		t.Helper()

		if err := chromedp.Run(ctx, actions...); err != nil {
			t.Fatal(err)
		}
	}
	poll := func(expression string) {
		t.Helper()
		check(chromedp.Poll(expression, nil, chromedp.WithPollingTimeout(8*time.Second)))
	}

	check(network.Enable(), chromedp.ActionFunc(func(ctx context.Context) error {
		return network.SetCookie(cookie.Name, cookie.Value).WithURL(origin).WithHTTPOnly(true).Do(ctx)
	}), testbrowser.NewDocument(chromedp.Navigate(origin+"/positions")))
	poll(
		`document.querySelector('[data-positions-state]')?.textContent==='Available' && document.querySelectorAll('.tabulator-row').length===2 && document.querySelector('[data-positions-message]').hidden && document.querySelector('[data-positions-message]').textContent===''`,
	)
	poll(
		`document.querySelector('[data-positions-as-of]').textContent==='2026-11-04' && document.querySelector('.tabulator-table').textContent.includes('9007199254740993.0000000000001 shares') && document.querySelector('.tabulator-table').textContent.includes('0.0000 USD') && document.querySelector('.tabulator-table').textContent.includes('Not provided') && document.querySelector('.tabulator-table').textContent.includes('Short')`,
	)
	poll(
		`document.querySelector('.tabulator-row a[href="/instruments/equity"]').textContent==='SYN' && document.querySelector('.tabulator-row [tabulator-field="name"]').textContent.includes('Synthetic equity')`,
	)
	// Legacy column preferences must preserve layout without restoring obsolete headings.
	check(chromedp.Evaluate(`(()=>{
 const columns=window.Tabulator.findTable('.tabulator')[0].getColumnLayout();
 const cost=columns.find(column=>column.field==='cost');
 cost.title='Reported cost basis';cost.width=246;
 columns.splice(columns.indexOf(cost),1);columns.splice(2,0,cost);
 localStorage.setItem('tabulator-wake-positions-columns',JSON.stringify(columns));
})()`, nil), testbrowser.NewDocument(chromedp.Reload()))
	poll(`document.querySelector('[data-grid]').dataset.gridReady==='true'`)
	poll(
		`document.querySelector('.tabulator-col[tabulator-field="cost"] .tabulator-col-title')?.textContent==='Unit cost basis'`,
	)

	var legacyLayoutRestored bool
	check(
		chromedp.Evaluate(
			`(()=>{const table=window.Tabulator.findTable('.tabulator')[0];return table.getColumn('cost').getWidth()===246 && table.getColumnLayout()[2].field==='cost'})()`,
			&legacyLayoutRestored,
		),
	)

	if !legacyLayoutRestored {
		t.Fatal("updated heading discarded the saved column width or order")
	}

	check(chromedp.Click("[data-grid-reset]", chromedp.ByQuery))
	check(chromedp.Focus(".tabulator-cell details summary", chromedp.ByQuery), chromedp.KeyEvent(kb.Enter))
	poll(
		`document.querySelector('.tabulator-cell details').open && document.querySelector('.tabulator-cell details summary').textContent==='Call · 2027-01-15' && document.querySelector('.tabulator-cell details').textContent.includes('123.45000 USD') && document.querySelector('.tabulator-cell details dd:last-child').getBoundingClientRect().right<=document.querySelector('.tabulator-cell details').getBoundingClientRect().right+1`,
	)

	capture := func(name string) {
		t.Helper()

		folder := os.Getenv("WAKE_SCREENSHOT_DIR")
		if folder == "" {
			return
		}

		var png []byte
		check(testbrowser.SettleAnimations(), chromedp.FullScreenshot(&png, 90))
		//nolint:gosec // Caller-selected synthetic screenshot directory.
		if err := os.MkdirAll(folder, 0o700); err != nil {
			t.Fatal(err)
		}
		//nolint:gosec // Fixed capture names in the caller-selected test output directory.
		if err := os.WriteFile(filepath.Join(folder, name+".png"), png, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, theme := range []string{"light", "dark"} {
		for _, size := range []struct{ width, height int64 }{{1366, 900}, {390, 844}} {
			check(
				chromedp.EmulateViewport(size.width, size.height),
				testbrowser.EmulateSystemTheme(theme),
			)
			poll(`document.documentElement.scrollWidth<=innerWidth`)
			capture(fmt.Sprintf("positions-available-%s-%d", theme, size.width))
		}
	}

	check(
		chromedp.EmulateViewport(1366, 900),
		chromedp.Click(".tabulator-cell details summary", chromedp.ByQuery),
	)
	// A linked instrument keeps the canonical URL and returns to the originating grid entry.
	check(testbrowser.NewDocument(chromedp.Click("a[href='/instruments/equity']", chromedp.ByQuery)))
	poll(
		`location.pathname==='/instruments/equity' && document.querySelector('[data-return]').textContent==='Return to Positions' && document.querySelector('.wake-nav-link[aria-current="page"]').getAttribute('href')==='/positions'`,
	)
	poll(
		`document.querySelector('h1').textContent==='SYN' && document.querySelector('.wake-page-header').textContent.includes('Synthetic equity')`,
	)
	check(testbrowser.NewDocument(chromedp.Click("[data-return]", chromedp.ByQuery)))
	poll(
		`location.pathname==='/positions' && document.querySelector('[data-positions-state]')?.textContent==='Available'`,
	)
	// An unchanged check preserves the operator's open terms and focus.
	check(
		chromedp.Focus(".tabulator-cell details summary", chromedp.ByQuery),
		chromedp.KeyEvent(kb.Enter),
		chromedp.Evaluate(`document.dispatchEvent(new Event('sse:refresh'))`, nil),
	)
	poll(
		`document.querySelector('[data-grid-refresh]').hidden && !document.querySelector('[data-grid]').hasAttribute('aria-busy') && document.querySelector('.tabulator-cell details').open`,
	)
	revision.Store(1)
	check(chromedp.Evaluate(`document.dispatchEvent(new Event('sse:refresh'))`, nil))
	poll(`!document.querySelector('[data-grid-refresh]').hidden`)
	poll(
		`document.querySelector('.tabulator-table').textContent.includes('Synthetic equity') && !document.querySelector('.tabulator-table').textContent.includes('Renamed equity')`,
	)
	check(chromedp.Click("[data-grid-refresh]", chromedp.ByQuery))
	poll(
		`document.querySelector('.tabulator-table').textContent.includes('Renamed equity') && document.querySelector('[data-grid-refresh]').hidden`,
	)
	// A newer unusable set replaces both metadata and rows only when applied.
	revision.Store(2)
	check(chromedp.Evaluate(`document.dispatchEvent(new Event('sse:refresh'))`, nil))
	poll(
		`!document.querySelector('[data-grid-refresh]').hidden && document.querySelector('[data-positions-as-of]').textContent==='2026-11-04'`,
	)
	check(chromedp.Click("[data-grid-refresh]", chromedp.ByQuery))
	poll(
		`document.querySelector('[data-positions-state]').textContent==='Unavailable' && !document.querySelector('[data-positions-message]').hidden && document.querySelector('[data-positions-as-of]').textContent==='2026-11-05' && document.querySelectorAll('.tabulator-row').length===0 && document.querySelector('[data-positions-message]').textContent.includes('complete position set')`,
	)

	for _, theme := range []string{"light", "dark"} {
		check(
			chromedp.EmulateViewport(390, 844),
			testbrowser.EmulateSystemTheme(theme),
		)
		capture("positions-unavailable-" + theme + "-390")
	}

	revision.Store(3)
	check(chromedp.Evaluate(`document.dispatchEvent(new Event('sse:refresh'))`, nil))
	poll(`!document.querySelector('[data-grid-refresh]').hidden`)
	check(chromedp.Evaluate(`document.querySelector('[data-grid-refresh]').click()`, nil))
	poll(
		`document.querySelector('[data-positions-state]').textContent==='Available' && document.querySelector('[data-positions-message]').textContent==='No open positions were reported.' && !document.querySelector('.tabulator-placeholder') && document.querySelector('[data-positions-as-of]').textContent.includes('10:12:13.123400+01:00')`,
	)
	revision.Store(4)
	check(chromedp.Evaluate(`document.dispatchEvent(new Event('sse:refresh'))`, nil))
	poll(
		`!document.querySelector('[data-grid-retry]').hidden && document.querySelector('[data-positions-state]').textContent==='Available' && document.querySelector('[data-grid-status]').textContent.includes('retry')`,
	)
	revision.Store(0)
	check(chromedp.Evaluate(`document.querySelector('[data-grid-retry]').click()`, nil))
	poll(`!document.querySelector('[data-grid-refresh]').hidden`)
	check(chromedp.Evaluate(`document.querySelector('[data-grid-refresh]').click()`, nil))
	poll(
		`document.querySelectorAll('.tabulator-row').length===2 && document.querySelector('[data-positions-as-of]').textContent==='2026-11-04'`,
	)

	// Category selections compose, supersede slow reads and preserve unsubmitted text.
	check(
		chromedp.SendKeys("#positions-search", "synthetic", chromedp.ByQuery),
		chromedp.KeyEvent(kb.Enter),
	)
	poll(
		`new URLSearchParams(location.search).get('q')==='synthetic' && document.querySelector('[data-grid]').dataset.gridReady==='true'`,
	)

	changeCategory := func(id, value string) {
		t.Helper()
		check(chromedp.Evaluate(fmt.Sprintf(`(()=>{
 const select=document.getElementById(%q);
 select.focus();
 select.value=%q;
 select.dispatchEvent(new Event('change',{bubbles:true}));
})()`, id, value), nil))
	}

	check(chromedp.Evaluate(`document.querySelector('#positions-search').value='unsent draft'`, nil))
	holdShort.Store(true)
	changeCategory("positions-side", "short")

	select {
	case <-shortStarted:
	case <-time.After(8 * time.Second):
		t.Fatal("live Side change did not request filtered positions")
	}

	poll(
		`document.querySelector('[data-grid-status]').textContent==='Updating filters…' && document.querySelectorAll('.tabulator-row').length===2 && !new URLSearchParams(location.search).has('state')`,
	)
	changeCategory("positions-kind", "option")
	poll(`(()=>{
 const params=new URLSearchParams(location.search);
 return params.get('state')==='short' && params.get('kind')==='option' && params.get('q')==='synthetic' && params.get('page')==='1' &&
  document.querySelector('#positions-search').value==='unsent draft' && document.activeElement.id==='positions-kind' &&
  document.querySelector('[data-grid-status]').textContent==='1 of 2 results' && document.querySelectorAll('.tabulator-row').length===1 &&
  document.querySelector('.tabulator-row').textContent.includes('Synthetic option');
})()`)

	check(chromedp.Evaluate(`history.back()`, nil))
	poll(
		`!new URLSearchParams(location.search).has('state') && document.querySelector('#positions-side').value==='' && document.querySelector('#positions-kind').value==='' && document.querySelector('#positions-search').value==='synthetic' && document.querySelectorAll('.tabulator-row').length===2`,
	)
	check(chromedp.Evaluate(`history.forward()`, nil))
	poll(
		`document.querySelector('#positions-side').value==='short' && document.querySelector('#positions-kind').value==='option' && document.querySelectorAll('.tabulator-row').length===1`,
	)

	revision.Store(4)
	changeCategory("positions-side", "long")
	poll(
		`!document.querySelector('[data-grid-retry]').hidden && new URLSearchParams(location.search).get('state')==='short' && document.querySelectorAll('.tabulator-row').length===1`,
	)
	revision.Store(0)
	check(chromedp.Click("[data-grid-retry]", chromedp.ByQuery))
	poll(
		`new URLSearchParams(location.search).get('state')==='long' && document.querySelector('[data-grid-retry]').hidden && document.querySelector('[data-positions-message]').textContent.includes('match these filters') && document.querySelectorAll('.tabulator-row').length===0`,
	)
}
