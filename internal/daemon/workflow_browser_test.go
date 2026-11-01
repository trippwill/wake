// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

//go:build browser

package daemon

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"go.trippwill.dev/wake/internal/credential"
	"go.trippwill.dev/wake/internal/testbrowser"
	"go.trippwill.dev/wake/internal/transport/admin"
	"go.trippwill.dev/wake/providers/ibkrflex"
)

func TestBrowserAccountWorkflow(t *testing.T) {
	browser := os.Getenv("WAKE_BROWSER")
	if browser == "" {
		for _, name := range []string{"brave-browser", "chromium", "google-chrome"} {
			if p, err := exec.LookPath(name); err == nil {
				browser = p
				break
			}
		}
	}

	if browser == "" {
		t.Fatal("Chromium required; set WAKE_BROWSER")
	}

	var clock atomic.Int64
	clock.Store(time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC).Unix())

	now := func() time.Time { return time.Unix(clock.Load(), 0).UTC() }
	sent := make(chan string, 4)

	var (
		reportCalls    atomic.Int64
		keyedReference atomic.Bool
	)

	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Logf("upstream %s at %s", r.URL.Path, now())

		switch r.URL.Path {
		case "/SendRequest":
			if r.URL.Query().Get("t") != "synthetic-token" || r.URL.Query().Get("q") != "123" {
				t.Error("retained provider credential or settings missing")
				http.Error(w, "invalid access", http.StatusUnauthorized)

				return
			}

			sent <- r.URL.Query().Get("p")

			_, _ = fmt.Fprint(
				w,
				`<FlexStatementResponse><Status>Success</Status><ReferenceCode>generation</ReferenceCode></FlexStatementResponse>`,
			)
		case "/GetStatement":
			reportCalls.Add(1)

			_, _ = fmt.Fprint(
				w,
				`<FlexQueryResponse type="TCF"><FlexStatements><FlexStatement accountId="U-SYNTHETIC" fromDate="2026-10-20" toDate="2026-10-21"><TradeConfirms><TradeConfirm accountId="U-SYNTHETIC" assetCategory="BOND" transactionType="ExchTrade" levelOfDetail="EXECUTION" origTradeID="debt-trade" code="Ca"/><TradeConfirm accountId="U-SYNTHETIC" assetCategory="CASH" symbol="EUR.USD" description="EUR.USD" transactionType="ExchTrade" levelOfDetail="EXECUTION" execID="fx-fill" buySell="SELL" quantity="-10.123456789" price="1.2345678901" currency="USD" tradeDate="2026-10-20" netCash="12.00" commission="-0.25" commissionCurrency="GBP"/><TradeConfirm accountId="U-SYNTHETIC" assetCategory="BOND" description="Synthetic debt" conid="debt-1" tradeID="debt-trade" transactionType="ExchTrade" levelOfDetail="EXECUTION" execID="debt-fill" buySell="BUY" quantity="10000" price="99.123456789" currency="USD" tradeDate="2026-10-20" accruedInt="-12.3456789"/><TradeConfirm accountId="U-SYNTHETIC" assetCategory="OPT" symbol="SYN OUTCOME" description="Reported option" conid="option-1" tradeID="assignment-1" transactionType="BookTrade" levelOfDetail="EXECUTION" quantity="2" tradeDate="2026-10-20" code="A"/><TradeConfirm accountId="U-SYNTHETIC" assetCategory="OPT" symbol="SYN OPT" description="Reported option" conid="option-1" underlyingConid="123" underlyingSymbol="SYN" transactionType="ExchTrade" levelOfDetail="EXECUTION" execID="option-fill" tradeID="option-trade" buySell="SELL" quantity="2" price="1.2345678901" currency="USD" tradeDate="2026-10-20" strike="100.001" expiry="2026-11-20" putCall="P" multiplier="10"/><TradeConfirm accountId="U-SYNTHETIC" assetCategory="STK" symbol="SYN" description="Reported equity" conid="123" isin="US0000000001" transactionType="ExchTrade" levelOfDetail="EXECUTION" execID="fill-1" tradeID="trade-1" buySell="BUY" quantity="0.125" price="400.123456789" currency="USD" dateTime="2026-10-20;09:30:00.120 EDT" tradeDate="2026-10-20" amount="50.015432098625" netCash="-50.265432098625"/></TradeConfirms></FlexStatement><FlexStatement accountId="U-OTHER" fromDate="2026-10-20" toDate="2026-10-21"><TradeConfirms/></FlexStatement></FlexStatements></FlexQueryResponse>`,
			)
		case "/mapping":
			if keyedReference.Load() && r.Header.Get("X-OPENFIGI-APIKEY") != "synthetic-reference-key" {
				t.Error("retained reference credential missing")
				http.Error(w, "invalid access", http.StatusUnauthorized)

				return
			}

			_, _ = fmt.Fprint(
				w,
				`[{"data":[{"figi":"BBG000000001","shareClassFIGI":"BBG000000002","name":"REFERENCE EQUITY","marketSector":"Equity"}]}]`,
			)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	cfg := shellConfig(t)
	cfg.Credentials.MasterKeyPath = filepath.Join(cfg.Paths.ConfigDir, "master.key")
	if err := credential.CreateMasterKey(cfg.Credentials.MasterKeyPath); err != nil {
		t.Fatal(err)
	}

	if err := admin.SetUnlock(cfg.Paths.ConfigDir, []byte("synthetic-browser-unlock")); err != nil {
		t.Fatal(err)
	}

	var (
		stopDaemon context.CancelFunc
		done       chan error
	)

	startDaemon := func() {
		//nolint:gosec // stopCurrent cancels and joins each daemon before restart and on test cleanup.
		daemonContext, cancelDaemon := context.WithCancel(context.Background())
		stopDaemon = cancelDaemon

		ready := make(chan struct{})
		done = make(chan error, 1)
		go func() {
			done <- run(daemonContext, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), sourceOptions{IBKR: ibkrflex.Options{HTTPClient: upstream.Client(), BaseURL: upstream.URL}, ReferenceClient: upstream.Client(), ReferenceURL: upstream.URL + "/mapping", Now: now}, func(phase string) {
				if phase == "ready" {
					close(ready)
				}
			})
		}()

		select {
		case <-ready:
		case err := <-done:
			t.Fatal(err)
		case <-time.After(10 * time.Second):
			t.Fatal("daemon startup stalled")
		}
	}
	stopCurrent := func() {
		stopDaemon()

		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("workflow shutdown did not join")
		}
	}

	startDaemon()

	defer func() { stopCurrent() }()

	opts := append(chromedp.DefaultExecAllocatorOptions[:], chromedp.ExecPath(browser))

	allocator, stopBrowser := chromedp.NewExecAllocator(context.Background(), opts...)
	defer stopBrowser()

	ctx, cancel := chromedp.NewContext(allocator)
	defer cancel()

	ctx, deadline := context.WithTimeout(ctx, 180*time.Second)
	defer deadline()

	// The session owns the browser; a per-step deadline must not close it.
	if err := chromedp.Run(ctx); err != nil {
		t.Fatal(err)
	}

	var ok bool

	origin := cfg.Admin.Origin()
	check := func(actions ...chromedp.Action) {
		t.Helper()

		step, cancelStep := context.WithTimeout(ctx, 20*time.Second)
		defer cancelStep()

		if err := chromedp.Run(step, actions...); err != nil {
			var body, location string

			_ = chromedp.Run(ctx, chromedp.Location(&location), chromedp.Text("body", &body))
			t.Fatal(err, location, body)
		}
	}
	newDocument := testbrowser.NewDocument
	navigate := func(action chromedp.Action) { t.Helper(); check(newDocument(action)) }

	capture := func(name string) {
		t.Helper()

		dir := os.Getenv("WAKE_SCREENSHOT_DIR")
		if dir == "" {
			return
		}

		for _, appearance := range []struct {
			theme string
			width int64
		}{{"light", 1440}, {"dark", 390}} {
			check(
				emulation.SetDeviceMetricsOverride(appearance.width, 1000, 1, false),
				chromedp.Evaluate(
					`(()=>{const control=document.querySelector('[data-theme-preference]');control.value=`+strconv.Quote(
						appearance.theme,
					)+`;control.dispatchEvent(new Event('change',{bubbles:true}))})()`,
					nil,
				),
			)

			// Capture the settled theme, including finite CSS control transitions.
			check(chromedp.Evaluate(
				`new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve))).then(()=>Promise.all(document.getAnimations().filter(a=>a.effect?.getTiming().iterations!==Infinity).map(a=>a.finished.catch(()=>{}))))`,
				nil,
				func(p *runtime.EvaluateParams) *runtime.EvaluateParams { return p.WithAwaitPromise(true) },
			))

			var png []byte
			check(chromedp.FullScreenshot(&png, 90))
			//nolint:gosec // Caller-selected synthetic screenshot directory.
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			//nolint:gosec // Caller-selected synthetic screenshot directory and fixed names.
			if err := os.WriteFile(
				filepath.Join(dir, "ia-"+name+"-"+appearance.theme+".png"),
				png,
				0o600,
			); err != nil {
				t.Fatal(err)
			}
		}

		check(
			emulation.SetDeviceMetricsOverride(1440, 1000, 1, false),
			chromedp.Evaluate(
				`(()=>{const control=document.querySelector('[data-theme-preference]');control.value='light';control.dispatchEvent(new Event('change',{bubbles:true}))})()`,
				nil,
			),
		)
	}

	check(
		emulation.SetDeviceMetricsOverride(1440, 1000, 1, false),
		newDocument(chromedp.Navigate(origin+"/login")),
		chromedp.WaitVisible("#unlock"),
		chromedp.SendKeys("#unlock", "synthetic-browser-unlock"),
	)
	navigate(chromedp.Click(`button[type="submit"]`))
	check(chromedp.WaitVisible("#main-navigation"))
	navigate(chromedp.Click(`a[href="/connections"]`))
	navigate(chromedp.Click(`a[href="/connections/new"]`))
	check(
		chromedp.WaitVisible("#connection-name-ibkr-flex"),
		chromedp.SendKeys("#connection-name-ibkr-flex", "Synthetic IBKR"),
		chromedp.SendKeys(`form[action="/connections"] input[name="setting.query_id"]`, "123"),
		chromedp.SendKeys(`form[action="/connections"] input[name="secret.token"]`, "synthetic-token"),
	)
	navigate(chromedp.Click(`form[action="/connections"] button[type="submit"]`))
	check(chromedp.WaitVisible("#workflow-progress"))

	var connectionPath string
	check(chromedp.Location(&connectionPath))
	connectionPath = strings.Split(strings.TrimPrefix(connectionPath, origin), "?")[0]
	check(
		newDocument(chromedp.Navigate(origin+"/connections/new")),
		chromedp.WaitVisible("#connection-name-ibkr-flex"),
	)

	select {
	case period := <-sent:
		if period != "365" {
			t.Fatal("discovery period", period)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no discovery request")
	}
	// A background status refresh must preserve an unfinished connection form.
	check(chromedp.SendKeys("#connection-name-ibkr-flex", "Unsubmitted draft"))
	clock.Add(10)
	check(
		chromedp.Evaluate(`window.htmx.trigger(document.querySelector('#workspace-content'),'sse:refresh')`, nil),
		chromedp.Evaluate(
			`document.querySelector('#connection-name-ibkr-flex').value==='Unsubmitted draft' && document.body.dataset.liveState==='connected'`,
			&ok,
		),
	)

	if !ok {
		t.Fatal("discovery metadata or active form lost")
	}

	check(
		newDocument(chromedp.Navigate(origin+connectionPath+"?view=status")),
		chromedp.Poll(`document.querySelector('a[href*="account=U-SYNTHETIC"]') !== null`, nil),
		chromedp.Evaluate(`document.querySelector('a[href*="account=U-SYNTHETIC"]').click()`, nil),
		chromedp.WaitVisible("#account-name"),
		chromedp.SendKeys("#account-name", "My confirmed account"),
		newDocument(chromedp.Click(`button[type="submit"]`)),
		chromedp.WaitVisible("#activity-rows"),
		chromedp.Poll(
			`document.querySelector('[data-grid-ready="true"]')!==null&&!document.querySelector('[data-grid]').hasAttribute('aria-busy')`,
			nil,
		),
	)
	clock.Add(6)

	select {
	case period := <-sent:
		if period != "365" {
			t.Fatal("authorized initial period", period)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no authorized request")
	}

	clock.Add(10)
	check(
		chromedp.Poll(
			`document.querySelector('a[href^="/executions/"]') !== null && document.body.textContent.includes('REFERENCE EQUITY')`,
			nil,
		),
		chromedp.Evaluate(
			`document.body.textContent.includes('0.125') && document.body.textContent.includes('400.123456789') && document.body.textContent.includes('My confirmed account')`,
			&ok,
		),
	)

	if !ok {
		t.Fatal("missing exact financial view")
	}

	check(
		newDocument(chromedp.Navigate(origin+"/activity")),
		chromedp.WaitVisible(".tabulator-row"),
		chromedp.Poll(
			`document.querySelector('.tabulator-col[tabulator-field="date"]').getAttribute('aria-sort')==='descending'`,
			nil,
		),
	)
	check(
		newDocument(chromedp.Navigate(origin+"/activity?view=instruments")),
		chromedp.WaitVisible(".tabulator-row"),
		chromedp.Poll(
			`document.querySelector('.tabulator-col[tabulator-field="name"]').getAttribute('aria-sort')==='ascending'`,
			nil,
		),
	)
	check(chromedp.Click(`.tabulator-col[tabulator-field="name"]`))
	check(
		chromedp.Poll(
			`new URL(location.href).searchParams.get('sort')==='name'&&new URL(location.href).searchParams.get('dir')==='desc'&&document.querySelector('.tabulator-col[tabulator-field="name"]').getAttribute('aria-sort')==='descending'`,
			nil,
		),
	)
	check(newDocument(chromedp.Navigate(origin+"/activity")), chromedp.WaitVisible(".tabulator-row"))
	capture("activity")
	check(
		newDocument(chromedp.Navigate(origin+"/activity?q=REFERENCE+EQUITY&sort=price&dir=desc&page=1")),
		chromedp.WaitVisible(".tabulator-row"),
	)

	navigate(chromedp.Click(
		`//div[contains(@class,'tabulator-row') and contains(.,'REFERENCE EQUITY')]//a[starts-with(@href,'/executions/')][1]`,
		chromedp.BySearch,
	))
	check(
		chromedp.WaitVisible(".wake-card dl"),
		chromedp.Evaluate(
			`document.body.textContent.includes('2026-10-20T13:30:00.120Z') && document.body.textContent.includes('accepted')`,
			&ok,
		),
	)

	if !ok {
		t.Fatal("execution inspection lost precision")
	}

	check(
		chromedp.Click(`//summary[text()='Retained observations and acceptance rules']`, chromedp.BySearch),
		newDocument(chromedp.Click(`.tabulator a[href^="/evidence/"]`)),
		chromedp.WaitVisible(`//h2[text()='Acquisition context']`, chromedp.BySearch),
		chromedp.Evaluate(
			`document.body.textContent.includes('Synthetic IBKR') && document.body.textContent.includes('Requested coverage') && document.querySelector('#original-content pre')!==null && !document.querySelector('#workspace-content').hasAttribute('hx-trigger')`,
			&ok,
		),
	)

	if !ok {
		t.Fatal("retained evidence context or snapshot")
	}

	capture("evidence")
	check(
		newDocument(chromedp.Click("[data-return]")),
		chromedp.WaitVisible(`//h2[text()='Financial summary']`, chromedp.BySearch),
	)

	check(
		newDocument(chromedp.Click(`.wake-card a[href^="/instruments/"]:not([href*="/correct"])`)),
		chromedp.WaitVisible(`//h2[text()='Instrument identity']`, chromedp.BySearch),
		chromedp.Evaluate(
			`document.querySelector('h1').textContent==='REFERENCE EQUITY' && document.body.textContent.includes('BBG000000002')`,
			&ok,
		),
	)

	if !ok {
		t.Fatal("reference inspection")
	}

	var directInstrument string
	check(chromedp.Location(&directInstrument))
	capture("instrument")
	check(
		newDocument(chromedp.Click("[data-return]")),
		chromedp.WaitVisible(`//h2[text()='Financial summary']`, chromedp.BySearch),
		newDocument(chromedp.Click("[data-return]")),
		chromedp.WaitVisible("#activity-search"),
		chromedp.WaitVisible(".tabulator-row"),
		chromedp.Poll(
			`document.activeElement.closest('[data-record]')?.dataset.record===new URL(location.href).searchParams.get('selected')`,
			nil,
		),
		chromedp.Evaluate(
			`document.querySelector('#activity-search').value==='REFERENCE EQUITY' && new URL(location.href).searchParams.get('sort')==='price' && new URL(location.href).searchParams.has('selected') && window.Tabulator.findTable('.tabulator')[0].getDataCount('active')===1`,
			&ok,
		),
	)

	if !ok {
		var debug string

		_ = chromedp.Run(
			ctx,
			chromedp.Evaluate(
				`JSON.stringify({url:location.href,filter:document.querySelector('#activity-search')?.value,rows:document.querySelectorAll('.tabulator-row').length,count:window.Tabulator.findTable('.tabulator')[0]?.getDataCount('active')})`,
				&debug,
			),
		)
		t.Fatal("list filter, sort or selection lost on return", debug)
	}

	check(
		chromedp.NavigateBack(),
		chromedp.WaitVisible(`//h2[text()='Financial summary']`, chromedp.BySearch),
		chromedp.NavigateForward(),
		chromedp.WaitVisible("#activity-search"),
		chromedp.Evaluate(`document.querySelector('#activity-search').value==='REFERENCE EQUITY'`, &ok),
	)

	if !ok {
		t.Fatal("list context lost on browser history")
	}

	// A directly opened detail has no list-origin parameter. Evidence navigation
	// must still return through the detail to Activity rather than a self-loop.
	check(
		newDocument(chromedp.Navigate(strings.Split(directInstrument, "?")[0])),
		chromedp.Poll(
			`document.querySelector('[data-grid-ready="true"]')!==null&&!document.querySelector('[data-grid]').hasAttribute('aria-busy')`,
			nil,
		),
		newDocument(chromedp.Click(`.tabulator a[href^="/evidence/"]`)),
		chromedp.WaitVisible(`//h2[text()='Acquisition context']`, chromedp.BySearch),
		newDocument(chromedp.Click("[data-return]")),
		chromedp.WaitVisible(`//h2[text()='Instrument identity']`, chromedp.BySearch),
		chromedp.Evaluate(`new URL(document.querySelector('[data-return]').href).pathname==='/activity'`, &ok),
	)

	if !ok {
		t.Fatal("direct instrument acquired a self-referential navigation origin")
	}

	check(newDocument(chromedp.Click("[data-return]")), chromedp.WaitVisible("#activity-rows"))

	check(
		newDocument(chromedp.Navigate(origin+"/activity")),
		chromedp.WaitVisible("#activity-rows"),
		chromedp.Poll(
			`document.querySelector('[data-grid-ready="true"]')!==null&&!document.querySelector('[data-grid]').hasAttribute('aria-busy')`,
			nil,
		),
		newDocument(chromedp.Click(
			`//div[contains(@class,'tabulator-row') and contains(.,'Reported option')]//a[starts-with(@href,'/executions/')][1]`,
			chromedp.BySearch,
		)),
		chromedp.WaitVisible(".wake-card dl"),
		chromedp.Evaluate(
			`document.body.textContent.includes('Quantity (contracts)') && document.body.textContent.includes('100.001 USD') && document.body.textContent.includes('Premium multiplier') && document.body.textContent.includes('Unknown')`,
			&ok,
		),
	)

	if !ok {
		t.Fatal("option inspection")
	}

	check(
		newDocument(chromedp.Click(`.wake-card a[href^="/instruments/"]:not([href*="/correct"])`)),
		chromedp.WaitVisible(`//h2[text()='Instrument identity']`, chromedp.BySearch),
		newDocument(chromedp.Click(`//a[text()='Inspect linked underlier']`, chromedp.BySearch)),
		chromedp.WaitVisible(`//h1[text()='REFERENCE EQUITY']`, chromedp.BySearch),
		chromedp.Evaluate(`document.querySelector('h1').textContent==='REFERENCE EQUITY'`, &ok),
	)

	if !ok {
		t.Fatal("option underlier navigation")
	}

	check(
		newDocument(chromedp.Navigate(origin+"/activity")),
		chromedp.WaitVisible("#activity-rows"),
		chromedp.Poll(
			`document.querySelector('[data-grid-ready="true"]')!==null&&!document.querySelector('[data-grid]').hasAttribute('aria-busy')`,
			nil,
		),
		newDocument(chromedp.Click(
			`//div[contains(@class,"tabulator-row") and contains(.,"Assignment")]//a[starts-with(@href,"/activity/")][1]`,
			chromedp.BySearch,
		)),
		chromedp.WaitVisible(".wake-card dl"),
		chromedp.Evaluate(
			`document.querySelector('h1').textContent.startsWith('Assignment') && document.body.textContent.includes('assignment-1') && document.body.textContent.includes('No explicit lifecycle relationship reported')`,
			&ok,
		),
	)

	if !ok {
		t.Fatal("lifecycle inspection")
	}

	check(
		newDocument(chromedp.Navigate(origin+"/activity")),
		chromedp.WaitVisible("#activity-rows"),
		chromedp.Poll(
			`document.querySelector('[data-grid-ready="true"]')!==null&&!document.querySelector('[data-grid]').hasAttribute('aria-busy')`,
			nil,
		),
		newDocument(chromedp.Click(
			`//div[contains(@class,"tabulator-row") and contains(.,"Synthetic debt")]//a[starts-with(@href,"/executions/")][1]`,
			chromedp.BySearch,
		)),
		chromedp.WaitVisible(".wake-card dl"),
		chromedp.Evaluate(
			`document.body.textContent.includes('Quantity (nominal)')&&document.body.textContent.includes('-12.3456789 USD')&&document.body.textContent.includes('quotation basis unknown')&&document.body.textContent.includes('Coupon rate (reported)')`,
			&ok,
		),
	)

	if !ok {
		t.Fatal("debt inspection")
	}

	check(
		newDocument(chromedp.Navigate(origin+"/activity")),
		chromedp.WaitVisible("#activity-rows"),
		chromedp.Poll(
			`document.querySelector('[data-grid-ready="true"]')!==null&&!document.querySelector('[data-grid]').hasAttribute('aria-busy')`,
			nil,
		),
		newDocument(chromedp.Click(
			`//div[contains(@class,"tabulator-row") and contains(.,"Convert ")]//a[starts-with(@href,"/activity/")][1]`,
			chromedp.BySearch,
		)),
		chromedp.WaitVisible(".wake-card dl"),
		chromedp.Evaluate(
			`document.querySelector('h1').textContent==='Convert EUR to USD'&&document.body.textContent.includes('10.123456789 EUR')&&document.body.textContent.includes('Unknown USD')&&document.body.textContent.includes('-0.25 GBP')`,
			&ok,
		),
	)

	if !ok {
		t.Fatal("conversion inspection")
	}

	check(
		newDocument(chromedp.Navigate(origin+"/activity")),
		chromedp.WaitVisible("#activity-rows"),
		chromedp.Poll(
			`document.querySelector('[data-grid-ready="true"]')!==null&&!document.querySelector('[data-grid]').hasAttribute('aria-busy')`,
			nil,
		),
		newDocument(chromedp.Click(
			`//div[contains(@class,'tabulator-row') and contains(.,'Synthetic debt')]//a[starts-with(@href,'/executions/')][1]`,
			chromedp.BySearch,
		)),
		chromedp.WaitVisible(".wake-card dl"),
		chromedp.Evaluate(
			`document.body.textContent.includes('Source corrections')&&document.body.textContent.includes('Cancellation')&&document.body.textContent.includes('applied')&&document.body.textContent.includes('cancelled')`,
			&ok,
		),
	)

	if !ok {
		t.Fatal("correction status/history")
	}

	var submitted string

	check(
		newDocument(chromedp.Navigate(origin+"/activity")),
		chromedp.WaitVisible("#activity-rows"),
		chromedp.Poll(
			`document.querySelector('[data-grid-ready="true"]')!==null&&!document.querySelector('[data-grid]').hasAttribute('aria-busy')`,
			nil,
		),
		newDocument(chromedp.Click(
			`//div[contains(@class,'tabulator-row') and contains(.,'Reported option')]//a[starts-with(@href,'/executions/')][1]`,
			chromedp.BySearch,
		)),
		chromedp.WaitVisible(".wake-card dl"),
		newDocument(chromedp.Click(`a[href*="/correct"]`)),
		chromedp.WaitVisible("#decision-scope"),
		chromedp.SetValue("#decision-scope", "price"),
		chromedp.Evaluate(
			`document.querySelector('#decision-scope').dispatchEvent(new Event('change',{bubbles:true}))`,
			nil,
		),
		chromedp.SetValue("#decision-value", "2.999000000000001"),
		chromedp.SetValue("#decision-currency", "USD"),
		chromedp.SetValue("#decision-rationale", "Browser field correction"),
		chromedp.Evaluate(
			`document.querySelector('#decision-value').dispatchEvent(new Event('input',{bubbles:true}))`,
			nil,
		),
		chromedp.Poll(`document.querySelector('#decision-feedback').dataset.state==='valid'`, nil),
	)
	capture("correction")
	check(
		chromedp.Evaluate(
			`new URLSearchParams(new FormData(document.querySelector('form[aria-label="Correct a field or link"]'))).toString()`,
			&submitted,
		),
		newDocument(chromedp.Click(`form[aria-label="Correct a field or link"] button[type="submit"]`)),
		chromedp.WaitVisible(`//li[contains(.,"Browser field correction")]`, chromedp.BySearch),
		chromedp.WaitVisible(`form[aria-label^="Release "]`),
		chromedp.Evaluate(
			`document.body.textContent.includes('Active decisions')&&document.body.textContent.includes('Browser field correction')&&document.body.textContent.includes('admin/local/')`,
			&ok,
		),
	)

	if !ok {
		t.Fatal("correction form outcome/attribution")
	}

	var status int
	check(
		chromedp.Evaluate(
			`fetch(location.pathname,{method:'POST',headers:{'Content-Type':'application/x-www-form-urlencoded'},body:`+strconv.Quote(
				submitted,
			)+`}).then(r=>r.status)`,
			&status,
			func(p *runtime.EvaluateParams) *runtime.EvaluateParams { return p.WithAwaitPromise(true) },
		),
	)

	if status != http.StatusOK {
		t.Fatal("duplicate submission failed", status)
	}

	check(
		chromedp.Evaluate(
			`(()=>{const p=new URLSearchParams(`+strconv.Quote(
				submitted,
			)+`);p.set('submission',document.querySelector('input[name="submission"]').value);p.set('value','3');return fetch(location.pathname,{method:'POST',headers:{'Content-Type':'application/x-www-form-urlencoded'},body:p}).then(r=>r.status)})()`,
			&status,
			func(p *runtime.EvaluateParams) *runtime.EvaluateParams { return p.WithAwaitPromise(true) },
		),
	)

	if status != http.StatusConflict {
		t.Fatal("stale correction form accepted", status)
	}

	check(
		newDocument(chromedp.Click(`form[aria-label^="Release "] button`)),
		chromedp.WaitVisible(`//p[text()='No active operator decisions.']`, chromedp.BySearch),
		chromedp.Evaluate(`document.querySelectorAll('form[aria-label^="Release "]').length===0`, &ok),
	)

	if !ok {
		t.Fatal("decision release")
	}

	check(
		newDocument(chromedp.Navigate(origin+connectionPath+"?view=reports")),
		chromedp.Click(`//summary[text()='Processing actions']`, chromedp.BySearch),
		newDocument(chromedp.Click(`form[action^="/processing/"] button`)),
		chromedp.WaitNotPresent("form[data-submitting]"),
	)

	check(
		newDocument(chromedp.Navigate(origin+"/activity")),
		chromedp.Poll(`document.querySelectorAll(".tabulator-row").length===5`, nil),
		chromedp.Evaluate(
			`document.querySelectorAll('.tabulator-row').length===5&&document.body.textContent.includes('400.123456789')&&document.body.textContent.includes('cancelled')`,
			&ok,
		),
	)

	if !ok {
		t.Fatal("recovery duplicated or lost financial views")
	}

	if reportCalls.Load() != 2 {
		t.Fatal("discovery did not stop after its bounded report", reportCalls.Load())
	}
	// Recover access independently using normal authenticated forms.
	stopCurrent()

	for _, path := range []string{cfg.Storage.ConnectionsPath} {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}

	clock.Add(86400)
	startDaemon()
	check(
		newDocument(chromedp.Navigate(origin+"/activity")),
		chromedp.Poll(`document.querySelectorAll(".tabulator-row").length===5`, nil),
		chromedp.Evaluate(
			`document.querySelectorAll('.tabulator-row').length===5&&document.body.textContent.includes('400.123456789')`,
			&ok,
		),
	)

	if !ok {
		t.Fatal("warehouse-only inspection lost history")
	}

	check(
		newDocument(chromedp.Navigate(origin+"/connections/recovery")),
		chromedp.WaitVisible("#storage-recovery-title"),
	)
	capture("recovery")
	check(
		chromedp.Evaluate(
			`document.body.textContent.includes('Access configuration is missing or incomplete')`,
			&ok,
		),
	)

	if !ok {
		t.Fatal("missing access not visible")
	}
	// The resume action cannot grant authority before access and reference review.
	check(chromedp.Evaluate(`document.querySelector('form[action="/recovery/resume"] button').disabled`, &ok))

	if !ok {
		t.Fatal("resume enabled before access prerequisites")
	}

	check(
		chromedp.Evaluate(
			`fetch('/recovery/resume',{method:'POST',headers:{'Content-Type':'application/x-www-form-urlencoded'},body:new URLSearchParams(new FormData(document.querySelector('form[action="/recovery/resume"]')))}).then(r=>r.status)`,
			&status,
			func(p *runtime.EvaluateParams) *runtime.EvaluateParams { return p.WithAwaitPromise(true) },
		),
	)

	if status != http.StatusBadRequest {
		t.Fatal("premature resume accepted", status)
	}

	navigate(chromedp.Navigate(origin + "/connections/recovery"))
	check(chromedp.WaitVisible("a[data-repair-source]"))
	navigate(chromedp.Evaluate(`document.querySelector('a[data-repair-source]').click()`, nil))
	check(
		chromedp.WaitVisible(`form[action$="/settings"]`),
		chromedp.SetValue(`form[action$="/settings"] input[name="connection_name"]`, "Recovered IBKR"),
		chromedp.SetValue(`form[action$="/settings"] input[name="setting.query_id"]`, "123"),
	)
	navigate(chromedp.Click(`form[action$="/settings"] button`))
	check(chromedp.WaitVisible("a[data-repair-source]"))
	navigate(chromedp.Evaluate(`document.querySelector('a[data-repair-source]').click()`, nil))
	check(
		chromedp.WaitVisible(`form[action$="/secrets"]`),
		chromedp.SetValue(`input[name="secret.token"]`, "synthetic-token"),
	)
	navigate(chromedp.Click(`form[action$="/secrets"] button`))
	check(
		chromedp.WaitNotPresent(`p[id^="access-missing-"]`),
		chromedp.SetValue("#openfigi-key", "synthetic-reference-key"),
	)
	navigate(chromedp.Click(`form[action="/reference/key"] button`))
	check(
		chromedp.WaitVisible(`//p[text()='Reference access has been reviewed.']`, chromedp.BySearch),
		chromedp.WaitVisible(`form[action="/recovery/resume"] button:not([disabled])`),
	)

	if reportCalls.Load() != 2 {
		t.Fatal("companion recreation contacted upstream", reportCalls.Load())
	}

	keyedReference.Store(true)

	check(
		newDocument(chromedp.Click(`form[action="/recovery/resume"] button`)),
		chromedp.WaitVisible("#workflow-progress"),
		chromedp.WaitNotPresent("#storage-recovery-title"),
	)

	select {
	case <-sent:
	case <-time.After(10 * time.Second):
		t.Fatal("explicit resume did not restore upstream work")
	}
	// Reset financial history through the offline command, retaining usable credentials.
	stopCurrent()
	//nolint:gosec // Synthetic credentials in this test's private directory.
	credentialsBefore, err := os.ReadFile(cfg.Storage.ConnectionsPath)
	if err != nil {
		t.Fatal(err)
	}

	result, err := ResetStorage(t.Context(), cfg, "", ResetWarehouse, true, nil)
	if err != nil || result.DryRun {
		t.Fatal("warehouse reset failed", result, err)
	}
	//nolint:gosec // Synthetic credentials in this test's private directory.
	credentialsAfter, err := os.ReadFile(cfg.Storage.ConnectionsPath)
	if err != nil || !bytes.Equal(credentialsBefore, credentialsAfter) {
		t.Fatal("warehouse reset changed credentials", err)
	}

	callsBefore := reportCalls.Load()

	clock.Add(86400)
	startDaemon()
	check(
		newDocument(chromedp.Navigate(origin+"/connections/recovery")),
		chromedp.WaitVisible("#warehouse-reset-notice"),
		chromedp.Evaluate(
			`document.body.textContent.includes('Recovered IBKR')&&!document.querySelector('p[id^="access-missing-"]')&&!document.body.textContent.includes('Activated account:')`,
			&ok,
		),
	)

	if !ok || reportCalls.Load() != callsBefore {
		t.Fatal("fresh warehouse lost settings or contacted upstream before resume")
	}

	check(
		newDocument(chromedp.Click(`form[action="/recovery/resume"] button`)),
		chromedp.WaitNotPresent("#storage-recovery-title"),
	)

	select {
	case period := <-sent:
		if period != "365" {
			t.Fatal("fresh discovery period", period)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("retained credentials did not start rediscovery")
	}

	clock.Add(10)
	check(
		newDocument(chromedp.Navigate(origin+connectionPath+"?view=status")),
		chromedp.Poll(`document.querySelector('a[href*="account=U-SYNTHETIC"]')!==null`, nil),
		chromedp.Evaluate(`!document.body.textContent.includes('Activated account:')`, &ok),
	)

	if !ok || reportCalls.Load() != callsBefore+1 {
		t.Fatal("rediscovery did not stop for confirmation")
	}

	check(
		chromedp.Evaluate(`document.querySelector('a[href*="account=U-SYNTHETIC"]').click()`, nil),
		chromedp.WaitVisible("#account-name"),
		chromedp.SetValue("#account-name", "Fresh confirmed account"),
		newDocument(chromedp.Click(`button[type="submit"]`)),
		chromedp.WaitVisible("#activity-rows"),
		chromedp.Poll(
			`document.querySelector('[data-grid-ready="true"]')!==null&&!document.querySelector('[data-grid]').hasAttribute('aria-busy')`,
			nil,
		),
	)
	clock.Add(6)

	select {
	case <-sent:
	case <-time.After(10 * time.Second):
		t.Fatal("reconfirmed account did not start acquisition")
	}

	clock.Add(10)
	check(
		chromedp.Poll(
			`document.body.textContent.includes('Fresh confirmed account')&&document.body.textContent.includes('400.123456789')`,
			nil,
		),
	)
}
