// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

//go:build browser

package admin

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
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

	var revision atomic.Int32

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

			if query.Options().Search != "" {
				out.Items = nil
				out.Matched = 0
			}

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

	server.Config.Handler = handler
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
		`document.querySelector('.tabulator-col[tabulator-field="cost"] .tabulator-col-title')?.textContent==='Total basis'`,
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
		`document.querySelector('.tabulator-cell details').open && document.querySelector('.tabulator-cell details').textContent.includes('123.45000 USD') && document.querySelector('.tabulator-cell details dd:last-child').getBoundingClientRect().right<=document.querySelector('.tabulator-cell details').getBoundingClientRect().right+1`,
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
				chromedp.SetValue("[data-theme-preference]", theme, chromedp.ByQuery),
				chromedp.Evaluate(
					`document.querySelector('[data-theme-preference]').dispatchEvent(new Event('change',{bubbles:true}))`,
					nil,
				),
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
			chromedp.SetValue("[data-theme-preference]", theme, chromedp.ByQuery),
			chromedp.Evaluate(
				`document.querySelector('[data-theme-preference]').dispatchEvent(new Event('change',{bubbles:true}))`,
				nil,
			),
		)
		capture("positions-unavailable-" + theme + "-390")
	}

	revision.Store(3)
	check(chromedp.Evaluate(`document.dispatchEvent(new Event('sse:refresh'))`, nil))
	poll(`!document.querySelector('[data-grid-refresh]').hidden`)
	check(chromedp.Evaluate(`document.querySelector('[data-grid-refresh]').click()`, nil))
	poll(
		`document.querySelector('[data-positions-state]').textContent==='Available' && document.querySelector('[data-positions-message]').textContent==='No open positions were reported.' && document.querySelector('[data-positions-as-of]').textContent.includes('10:12:13.123400+01:00')`,
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
}
