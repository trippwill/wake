// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

//go:build browser

package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/credential"
	"go.trippwill.dev/wake/internal/testbrowser"
	"go.trippwill.dev/wake/internal/transport/admin"
	"go.trippwill.dev/wake/providers/ibkrflex"
	"go.trippwill.dev/wake/providers/snaptrade"
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
	realizedSent := make(chan struct{}, 1)

	var (
		reportCalls    atomic.Int64
		keyedReference atomic.Bool
	)

	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Logf("upstream %s at %s", r.URL.Path, now())

		switch r.URL.Path {
		case "/authorizations",
			"/accounts/all",
			"/accounts/synthetic-snaptrade/activities",
			"/accounts/synthetic-snaptrade/positions/all":
			if r.Method != http.MethodGet || r.URL.Query().Get("clientId") != "synthetic-personal-client" ||
				r.Header.Get("Signature") == "" {
				t.Error("missing SnapTrade Personal authentication")
				http.Error(w, "invalid access", http.StatusUnauthorized)

				return
			}

			switch r.URL.Path {
			case "/authorizations":
				_, _ = io.WriteString(
					w,
					`[{"id":"auth-a","disabled":false,"brokerage":{"id":"fidelity-us","slug":"FIDELITY"}}]`,
				)
			case "/accounts/all":
				_, _ = io.WriteString(
					w,
					`{"results":[{"id":"synthetic-snaptrade","connection_id":"auth-a","institution_id":"fidelity-us","kind":"investment","display_name":"Fidelity fixture"}]}`,
				)
			case "/accounts/synthetic-snaptrade/positions/all":
				_, _ = io.WriteString(
					w,
					`{"results":[{"instrument":{"kind":"bond","id":"bond-position","symbol":"SYNBOND","raw_symbol":"SYNBOND"},"units":"12.3400","cost_basis":"14.2300","currency":"USD"}],"data_freshness":{"as_of":"2026-10-20T15:00:00.1200Z"}}`,
				)
			default:
				_, _ = io.WriteString(
					w,
					`{"data":[{"id":"fill-1","type":"BUY","units":0.125,"price":19.12345678900001,"trade_date":"2026-10-20T00:00:00.000Z","currency":{"code":"USD"},"symbol":{"id":"fixture","raw_symbol":"SYN","type":{"code":"cs"}}}],"pagination":{"offset":0,"limit":1000,"total":1}}`,
				)
			}
		case "/SendRequest":
			if r.URL.Query().Get("q") == "456" && r.URL.Query().Get("t") == "synthetic-token" {
				realizedSent <- struct{}{}

				_, _ = fmt.Fprint(
					w,
					`<FlexStatementResponse><Status>Success</Status><ReferenceCode>realized-generation</ReferenceCode></FlexStatementResponse>`,
				)

				return
			}

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
			if r.URL.Query().Get("q") == "realized-generation" {
				_, _ = fmt.Fprint(
					w,
					`<FlexQueryResponse type="AF"><FlexStatements><FlexStatement accountId="U-SYNTHETIC" fromDate="2026-10-20" toDate="2026-10-21"><Trades><Trade accountId="U-SYNTHETIC" assetCategory="STK" conid="123" tradeID="trade-1" ibExecID="fill-1" transactionType="ExchTrade" levelOfDetail="EXECUTION" quantity="0.125" currency="USD" tradeDate="2026-10-20" reportDate="2026-10-21" fifoPnlRealized="12.34000000000000001" cost="-100.0000"/></Trades><Transfers><Transfer transactionID="transfer-browser" assetCategory="STK" subCategory="COMMON" conid="123" currency="USD" levelOfDetail="TRANSFER" type="ACATS" direction="IN" quantity="3.00000000001" date="2026-10-20" transferPrice="100.0000"/></Transfers><CashTransactions><CashTransaction transactionID="cash-browser" levelOfDetail="DETAIL" type="Dividends" amount="7.89000000001" currency="USD" dateTime="2026-10-20" description="Synthetic browser dividend"/></CashTransactions><OpenPositions/></FlexStatement></FlexStatements></FlexQueryResponse>`,
				)

				return
			}

			reportCalls.Add(1)

			_, _ = fmt.Fprint(
				w,
				`<FlexQueryResponse type="TCF"><FlexStatements><FlexStatement accountId="U-SYNTHETIC" fromDate="2026-10-20" toDate="2026-10-21"><TradeConfirms><TradeConfirm accountId="U-SYNTHETIC" assetCategory="BOND" transactionType="ExchTrade" levelOfDetail="EXECUTION" origTradeID="debt-trade" code="Ca"/><TradeConfirm accountId="U-SYNTHETIC" assetCategory="CASH" symbol="EUR.USD" description="EUR.USD" transactionType="ExchTrade" levelOfDetail="EXECUTION" execID="fx-fill" buySell="SELL" quantity="-10.123456789" price="1.2345678901" currency="USD" tradeDate="2026-10-20" netCash="12.00" commission="-0.25" commissionCurrency="GBP" regFINRATradingActivityFee="-0.00100"/><TradeConfirm accountId="U-SYNTHETIC" assetCategory="BOND" description="Synthetic debt" conid="debt-1" tradeID="debt-trade" transactionType="ExchTrade" levelOfDetail="EXECUTION" execID="debt-fill" buySell="BUY" quantity="10000" price="99.123456789" currency="USD" tradeDate="2026-10-20" accruedInt="-12.3456789"/><TradeConfirm accountId="U-SYNTHETIC" assetCategory="OPT" symbol="SYN OUTCOME" description="Reported option" conid="option-1" tradeID="assignment-1" transactionType="BookTrade" levelOfDetail="EXECUTION" quantity="2" tradeDate="2026-10-20" code="A"/><TradeConfirm accountId="U-SYNTHETIC" assetCategory="OPT" symbol="SYN OPT" description="Reported option" conid="option-1" underlyingConid="123" underlyingSymbol="SYN" transactionType="ExchTrade" levelOfDetail="EXECUTION" execID="option-fill" tradeID="option-trade" buySell="SELL" quantity="2" price="1.2345678901" currency="USD" tradeDate="2026-10-20" strike="100.001" expiry="2026-11-20" putCall="P" multiplier="10"/><TradeConfirm accountId="U-SYNTHETIC" assetCategory="STK" symbol="SYN" description="Reported equity" conid="123" isin="US0000000001" transactionType="ExchTrade" levelOfDetail="EXECUTION" execID="fill-1" tradeID="trade-1" buySell="BUY" quantity="0.125" price="400.123456789" currency="USD" dateTime="2026-10-20;09:30:00.120 EDT" tradeDate="2026-10-20" amount="50.015432098625" netCash="-50.265432098625"/></TradeConfirms></FlexStatement><FlexStatement accountId="U-OTHER" fromDate="2026-10-20" toDate="2026-10-21"><TradeConfirms/></FlexStatement></FlexStatements></FlexQueryResponse>`,
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
			done <- run(daemonContext, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), sourceOptions{IBKR: ibkrflex.Options{HTTPClient: upstream.Client(), BaseURL: upstream.URL}, SnapTrade: snaptrade.Options{HTTPClient: upstream.Client(), BaseURL: upstream.URL}, ReferenceClient: upstream.Client(), ReferenceURL: upstream.URL + "/mapping", Now: now}, func(phase string) {
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
			var body, location, animations string

			_ = chromedp.Run(
				ctx,
				chromedp.Location(&location),
				chromedp.Text("body", &body, chromedp.ByQuery),
				chromedp.Evaluate(
					`JSON.stringify({phase:globalThis.wakeCapturePhase,visibility:document.visibilityState,animations:document.getAnimations().map(a=>({name:a.animationName,type:a.constructor.name,timeline:a.timeline?.constructor.name,play:a.playState,pending:a.pending,rate:a.playbackRate,current:String(a.currentTime),timing:a.effect?.getComputedTiming()}))})`,
					&animations,
				),
			)
			t.Fatal(err, location, body, animations)
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
			captureAction := func(phase string, action chromedp.Action) chromedp.Action {
				return chromedp.ActionFunc(func(step context.Context) error {
					if err := action.Do(step); err != nil {
						return fmt.Errorf("capture %s %s %s: %w", name, appearance.theme, phase, err)
					}

					return nil
				})
			}

			check(
				emulation.SetDeviceMetricsOverride(appearance.width, 1000, 1, false),
				testbrowser.EmulateSystemTheme(appearance.theme),
			)

			// Capture the settled theme, including finite CSS control transitions.
			check(captureAction("settling", testbrowser.SettleAnimations()))

			var png []byte
			check(captureAction("screenshot", chromedp.FullScreenshot(&png, 90)))
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
			testbrowser.EmulateSystemTheme("light"),
		)
	}

	check(
		emulation.SetDeviceMetricsOverride(1440, 1000, 1, false),
		newDocument(chromedp.Navigate(origin+"/login")),
		chromedp.WaitVisible("#unlock", chromedp.ByQuery),
		chromedp.SendKeys("#unlock", "synthetic-browser-unlock", chromedp.ByQuery),
	)
	navigate(chromedp.Click(`button[type="submit"]`, chromedp.ByQuery))
	check(chromedp.WaitVisible("#main-navigation", chromedp.ByQuery))
	navigate(chromedp.Click(`a[href="/connections"]`, chromedp.ByQuery))
	navigate(chromedp.Click(`a[href="/connections/new"]`, chromedp.ByQuery))
	check(
		chromedp.WaitVisible("#connection-name-ibkr-flex", chromedp.ByQuery),
		chromedp.SendKeys("#connection-name-ibkr-flex", "Synthetic IBKR", chromedp.ByQuery),
		chromedp.SendKeys(`form[action="/connections"] input[name="setting.query_id"]`, "123", chromedp.ByQuery),
		chromedp.SendKeys(
			`form[action="/connections"] input[name="secret.token"]`,
			"synthetic-token",
			chromedp.ByQuery,
		),
	)
	navigate(chromedp.Click(`form[action="/connections"] button[type="submit"]`, chromedp.ByQuery))
	check(
		chromedp.WaitVisible("#workflow-progress", chromedp.ByQuery),
		chromedp.Evaluate(
			`history.state.wakeNavigation.workspace==='/connections'&&history.state.wakeNavigation.trail[0]==='/connections'&&new URL(document.querySelector('[data-return]').href).pathname==='/connections'`,
			&ok,
		),
	)

	if !ok {
		t.Fatal("source creation lost its return context across the form redirect")
	}

	var connectionPath string
	check(chromedp.Location(&connectionPath))
	connectionPath = strings.Split(strings.TrimPrefix(connectionPath, origin), "?")[0]
	check(
		newDocument(chromedp.Navigate(origin+"/connections/new")),
		chromedp.WaitVisible("#connection-name-ibkr-flex", chromedp.ByQuery),
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
	check(chromedp.SendKeys("#connection-name-ibkr-flex", "Unsubmitted draft", chromedp.ByQuery))
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
		chromedp.WaitVisible("#account-name", chromedp.ByQuery),
		chromedp.SendKeys("#account-name", "My confirmed account", chromedp.ByQuery),
		newDocument(chromedp.Click(`button[type="submit"]`, chromedp.ByQuery)),
		chromedp.WaitVisible("#activity-rows", chromedp.ByQuery),
		chromedp.Poll(
			`document.querySelector('[data-grid-ready="true"]')!==null&&!document.querySelector('[data-grid]').hasAttribute('aria-busy')`,
			nil,
		),
	)
	check(
		chromedp.Evaluate(
			`history.state.wakeNavigation.workspace==='/activity'&&history.state.wakeNavigation.trail.length===0&&document.querySelector('.wake-nav-link[aria-current="page"]').getAttribute('href')==='/activity'`,
			&ok,
		),
	)

	if !ok {
		t.Fatal("account confirmation retained Connections context on Activity")
	}

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
		chromedp.WaitVisible("[data-grid-refresh]", chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelector('.tabulator-row')===null`, &ok),
	)

	if !ok {
		t.Fatal("background acquisition replaced activity before explicit refresh")
	}

	check(
		chromedp.Click("[data-grid-refresh]", chromedp.ByQuery),
		chromedp.Poll(
			`document.querySelector('a[href^="/executions/"]') !== null`,
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
	// Acquisition and reference enrichment can produce separate publications.
	check(chromedp.Evaluate(`document.body.textContent.includes('REFERENCE EQUITY')`, &ok))

	if !ok {
		check(
			chromedp.WaitVisible("[data-grid-refresh]", chromedp.ByQuery),
			chromedp.Click("[data-grid-refresh]", chromedp.ByQuery),
			chromedp.Poll(`document.body.textContent.includes('REFERENCE EQUITY')`, nil),
		)
	}

	check(
		newDocument(chromedp.Navigate(origin+"/activity")),
		chromedp.WaitVisible(".tabulator-row", chromedp.ByQuery),
		chromedp.Poll(
			`document.querySelector('.tabulator-col[tabulator-field="date"]').getAttribute('aria-sort')==='descending'`,
			nil,
		),
	)
	check(
		newDocument(chromedp.Navigate(origin+"/instruments")),
		chromedp.WaitVisible(".tabulator-row", chromedp.ByQuery),
		chromedp.Poll(
			`document.querySelector('.tabulator-col[tabulator-field="ticker"]').getAttribute('aria-sort')==='ascending'`,
			nil,
		),
	)
	check(chromedp.Click(`.tabulator-col[tabulator-field="ticker"]`, chromedp.ByQuery))
	check(
		chromedp.Poll(
			`new URL(location.href).searchParams.get('sort')==='ticker'&&new URL(location.href).searchParams.get('dir')==='desc'&&document.querySelector('.tabulator-col[tabulator-field="ticker"]').getAttribute('aria-sort')==='descending'`,
			nil,
		),
	)
	check(
		newDocument(chromedp.Navigate(origin+"/activity")),
		chromedp.WaitVisible(".tabulator-row", chromedp.ByQuery),
	)
	capture("activity")
	check(
		newDocument(chromedp.Navigate(origin+"/activity?q=REFERENCE+EQUITY&sort=price&dir=desc&page=1")),
		chromedp.WaitVisible(".tabulator-row", chromedp.ByQuery),
	)

	navigate(chromedp.Click(
		`//div[contains(@class,'tabulator-row') and contains(.,'REFERENCE EQUITY')]//a[starts-with(@href,'/executions/')][1]`,
		chromedp.BySearch,
	))
	check(
		chromedp.WaitVisible(".wake-card dl", chromedp.ByQuery),
		chromedp.Evaluate(
			`document.body.textContent.includes('2026-10-20T13:30:00.120Z') && document.body.textContent.includes('accepted')`,
			&ok,
		),
	)

	if !ok {
		t.Fatal("execution inspection lost precision")
	}

	var directActivity string
	check(
		chromedp.Evaluate(`location.href.split('?')[0].replace('/executions/','/activity/')`, &directActivity),
	)

	check(
		chromedp.Click(`//summary[text()='Retained observations and acceptance rules']`, chromedp.BySearch),
		newDocument(chromedp.Click(`.tabulator a[href^="/evidence/"]`, chromedp.ByQuery)),
		chromedp.WaitVisible(`//h2[text()='Acquisition context']`, chromedp.BySearch),
		chromedp.Poll(
			`document.querySelector('[data-grid-variant="artifact"]')?.dataset.gridReady==='true'`,
			nil,
		),
		chromedp.Evaluate(
			`document.body.textContent.includes('Synthetic IBKR') && document.body.textContent.includes('Requested coverage') && document.querySelector('#original-content pre')!==null && !document.querySelector('#workspace-content').hasAttribute('hx-trigger') && !new URL(location.href).searchParams.has('location') && document.querySelector('.tabulator-row a[href^="/activity/"]')!==null`,
			&ok,
		),
	)

	if !ok {
		t.Fatal("retained evidence context or snapshot")
	}

	capture("evidence")
	check(
		newDocument(chromedp.Click("[data-return]", chromedp.ByQuery)),
		chromedp.WaitVisible(`//h2[text()='Financial summary']`, chromedp.BySearch),
	)

	check(
		newDocument(
			chromedp.Click(`.wake-card a[href^="/instruments/"]:not([href*="/correct"])`, chromedp.ByQuery),
		),
		chromedp.WaitVisible(`//h2[text()='Instrument identity']`, chromedp.BySearch),
		chromedp.Evaluate(
			`document.querySelector('h1').textContent==='SYN' && document.querySelector('.wake-page-header').textContent.includes('REFERENCE EQUITY') && document.body.textContent.includes('BBG000000002')`,
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
		newDocument(chromedp.Click("[data-return]", chromedp.ByQuery)),
		chromedp.WaitVisible(`//h2[text()='Financial summary']`, chromedp.BySearch),
		newDocument(chromedp.Click("[data-return]", chromedp.ByQuery)),
		chromedp.WaitVisible("#activity-search", chromedp.ByQuery),
		chromedp.WaitVisible(".tabulator-row", chromedp.ByQuery),
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
		chromedp.WaitVisible("#activity-search", chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelector('#activity-search').value==='REFERENCE EQUITY'`, &ok),
	)

	if !ok {
		t.Fatal("list context lost on browser history")
	}

	// Direct instrument links start in Instruments and return through their own
	// detail when inspecting evidence.
	check(
		newDocument(chromedp.Navigate(strings.Split(directInstrument, "?")[0])),
		chromedp.Poll(
			`document.querySelector('[data-grid-ready="true"]')!==null&&!document.querySelector('[data-grid]').hasAttribute('aria-busy')`,
			nil,
		),
		newDocument(chromedp.Click(`.tabulator a[href^="/evidence/"]`, chromedp.ByQuery)),
		chromedp.WaitVisible(`//h2[text()='Acquisition context']`, chromedp.BySearch),
		newDocument(chromedp.Click("[data-return]", chromedp.ByQuery)),
		chromedp.WaitVisible(`//h2[text()='Instrument identity']`, chromedp.BySearch),
		chromedp.Evaluate(
			`new URL(document.querySelector('[data-return]').href).pathname==='/instruments' && document.querySelector('[data-return]').textContent.includes('Instruments')`,
			&ok,
		),
	)

	if !ok {
		t.Fatal("direct instrument acquired a self-referential navigation origin")
	}

	check(
		newDocument(chromedp.Click("[data-return]", chromedp.ByQuery)),
		chromedp.WaitVisible(`[data-grid="instruments"]`, chromedp.ByQuery),
	)

	for _, detail := range []string{directActivity, directInstrument} {
		root := "/activity"
		if strings.Contains(detail, "/instruments/") {
			root = "/instruments"
		}

		check(
			newDocument(chromedp.Navigate(strings.Split(detail, "?")[0]+"/correct?mode=decisions")),
			chromedp.WaitVisible("[data-return]", chromedp.ByQuery),
			newDocument(chromedp.Click("[data-return]", chromedp.ByQuery)),
			chromedp.WaitVisible("[data-return]", chromedp.ByQuery),
			chromedp.Evaluate(
				`new URL(document.querySelector('[data-return]').href).pathname===`+strconv.Quote(
					root,
				)+`&&history.state.wakeNavigation.trail.length===0`,
				&ok,
			),
		)

		if !ok {
			t.Fatal("direct correction created a return loop", detail)
		}

		check(
			newDocument(chromedp.Click("[data-return]", chromedp.ByQuery)),
			chromedp.WaitVisible("h1", chromedp.ByQuery),
			chromedp.Evaluate(`location.pathname===`+strconv.Quote(root), &ok),
		)

		if !ok {
			t.Fatal("direct correction failed to return to its workspace", detail)
		}
	}

	for _, target := range []string{"/review", "/instruments?state=gaps"} {
		check(
			newDocument(chromedp.Navigate(origin+"/")),
			chromedp.WaitVisible("h1", chromedp.ByQuery),
			newDocument(chromedp.Click(`main a[href="`+target+`"]`, chromedp.ByQuery)),
			chromedp.WaitVisible("h1", chromedp.ByQuery),
			chromedp.Evaluate(
				`history.state.wakeNavigation.workspace===location.pathname&&document.querySelector('.wake-nav-link[aria-current="page"]').getAttribute('href')===location.pathname&&(location.pathname!=='/instruments'||new URL(location.href).searchParams.get('state')==='gaps')`,
				&ok,
			),
		)

		if !ok {
			t.Fatal("Overview workspace link lost navigation or filters", target)
		}
	}

	for _, workspace := range []string{"instruments", "review"} {
		root := "/" + workspace + "?q=REFERENCE+EQUITY&sort=name&dir=asc&page=1&size=25"
		check(
			newDocument(chromedp.Navigate(origin+root)),
			chromedp.WaitVisible("h1", chromedp.ByQuery),
			chromedp.Evaluate(
				`(()=>{const link=document.createElement('a');link.id='navigation-proof';link.href=`+strconv.Quote(
					strings.Split(directInstrument, "?")[0],
				)+`;link.textContent='Inspect reference instrument';document.body.append(link)})()`,
				nil,
			),
			newDocument(chromedp.Click("#navigation-proof", chromedp.ByQuery)),
			chromedp.WaitVisible(`//h2[text()='Instrument identity']`, chromedp.BySearch),
			newDocument(chromedp.Click(`.tabulator a[href^="/evidence/"]`, chromedp.ByQuery)),
			chromedp.Poll(`document.querySelector('[data-grid]')?.dataset.gridReady==='true'`, nil),
			newDocument(chromedp.Reload()),
			chromedp.Poll(`document.querySelector('[data-grid]')?.dataset.gridReady==='true'`, nil),
			chromedp.Evaluate(
				`(()=>{const workspace=new URL(document.querySelector('.wake-nav-link[aria-current="page"]').href);return workspace.pathname===`+strconv.Quote(
					"/"+workspace,
				)+`&&document.querySelector('[data-parent-breadcrumb] a').textContent==='Instrument'&&!['origin','parent','page_url'].some(key=>new URL(location.href).searchParams.has(key))})()`,
				&ok,
			),
		)

		if !ok {
			t.Fatal("workspace or return trail lost on report reload", workspace)
		}

		if workspace == "review" {
			var resource string
			check(chromedp.Location(&resource))

			document, closeDocument := chromedp.NewContext(ctx)
			step, stopStep := context.WithTimeout(document, 20*time.Second)
			err := chromedp.Run(
				step,
				newDocument(chromedp.Navigate(resource)),
				chromedp.WaitVisible("[data-return]", chromedp.ByQuery),
				chromedp.Evaluate(
					`history.replaceState({wakeNavigation:{workspace:'https://outside.invalid',trail:['javascript:invalid']}},'');sessionStorage.setItem('wake.navigation.pending','invalid JSON')`,
					nil,
				),
				newDocument(chromedp.Reload()),
				chromedp.WaitVisible("[data-return]", chromedp.ByQuery),
				chromedp.Evaluate(
					`new URL(document.querySelector('[data-return]').href).pathname==='/activity'&&!new URL(document.querySelector('[data-return]').href).search&&document.querySelector('[data-parent-breadcrumb]').hidden`,
					&ok,
				),
			)

			stopStep()
			closeDocument()

			if err != nil || !ok {
				t.Fatal("new tab or malformed history lost the canonical fallback", err, ok)
			}
		}

		check(
			newDocument(chromedp.Click("[data-return]", chromedp.ByQuery)),
			chromedp.WaitVisible(`//h2[text()='Instrument identity']`, chromedp.BySearch),
			newDocument(chromedp.Click("[data-return]", chromedp.ByQuery)),
			chromedp.WaitVisible("h1", chromedp.ByQuery),
			chromedp.Evaluate(
				`(()=>{const q=new URL(location.href).searchParams;return location.pathname===`+strconv.Quote(
					"/"+workspace,
				)+`&&q.get('q')==='REFERENCE EQUITY'&&q.get('size')==='25'})()`,
				&ok,
			),
		)

		if !ok {
			t.Fatal("return to originating workspace lost view parameters", workspace)
		}
	}

	check(
		newDocument(chromedp.Navigate(origin+"/activity")),
		chromedp.WaitVisible("#activity-rows", chromedp.ByQuery),
		chromedp.Poll(
			`document.querySelector('[data-grid-ready="true"]')!==null&&!document.querySelector('[data-grid]').hasAttribute('aria-busy')`,
			nil,
		),
		newDocument(chromedp.Click(
			`//div[contains(@class,'tabulator-row') and contains(.,'Reported option')]//a[starts-with(@href,'/executions/')][1]`,
			chromedp.BySearch,
		)),
		chromedp.WaitVisible(".wake-card dl", chromedp.ByQuery),
		chromedp.Evaluate(
			`document.body.textContent.includes('Quantity (contracts)') && document.body.textContent.includes('100.001 USD') && document.body.textContent.includes('Premium multiplier') && document.body.textContent.includes('Unknown')`,
			&ok,
		),
	)

	if !ok {
		t.Fatal("option inspection")
	}

	check(
		newDocument(
			chromedp.Click(`.wake-card a[href^="/instruments/"]:not([href*="/correct"])`, chromedp.ByQuery),
		),
		chromedp.WaitVisible(`//h2[text()='Instrument identity']`, chromedp.BySearch),
		newDocument(chromedp.Click(`//a[text()='Inspect linked underlier']`, chromedp.BySearch)),
		chromedp.WaitVisible(`//h1[text()='SYN']`, chromedp.BySearch),
		chromedp.Evaluate(
			`document.querySelector('h1').textContent==='SYN' && document.querySelector('.wake-page-header').textContent.includes('REFERENCE EQUITY')`,
			&ok,
		),
	)

	if !ok {
		t.Fatal("option underlier navigation")
	}

	check(
		newDocument(chromedp.Navigate(origin+"/activity")),
		chromedp.WaitVisible("#activity-rows", chromedp.ByQuery),
		chromedp.Poll(
			`document.querySelector('[data-grid-ready="true"]')!==null&&!document.querySelector('[data-grid]').hasAttribute('aria-busy')`,
			nil,
		),
		newDocument(chromedp.Click(
			`//div[contains(@class,"tabulator-row") and contains(.,"Assignment")]//a[starts-with(@href,"/activity/")][1]`,
			chromedp.BySearch,
		)),
		chromedp.WaitVisible(".wake-card dl", chromedp.ByQuery),
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
		chromedp.WaitVisible("#activity-rows", chromedp.ByQuery),
		chromedp.Poll(
			`document.querySelector('[data-grid-ready="true"]')!==null&&!document.querySelector('[data-grid]').hasAttribute('aria-busy')`,
			nil,
		),
		newDocument(chromedp.Click(
			`//div[contains(@class,"tabulator-row") and contains(.,"Synthetic debt")]//a[starts-with(@href,"/executions/")][1]`,
			chromedp.BySearch,
		)),
		chromedp.WaitVisible(".wake-card dl", chromedp.ByQuery),
		chromedp.Evaluate(
			`document.body.textContent.includes('Face amount (nominal)')&&document.body.textContent.includes('-12.3456789 USD')&&document.body.textContent.includes('Reported price')&&document.body.textContent.includes('Trade net cash')&&!document.body.textContent.includes('quotation basis')&&document.body.textContent.includes('Coupon rate (reported)')`,
			&ok,
		),
	)

	if !ok {
		t.Fatal("debt inspection")
	}

	capture("debt")
	check(
		newDocument(chromedp.Click(`a[href*="/correct"]`, chromedp.ByQuery)),
		chromedp.WaitVisible("#decision-scope", chromedp.ByQuery),
		chromedp.Evaluate(`!document.querySelector('#decision-scope option[value="quotation_basis"]')`, &ok),
	)

	if !ok {
		t.Fatal("debt quotation correction remains available")
	}

	check(
		chromedp.SetValue("#decision-scope", "net", chromedp.ByQuery),
		chromedp.Evaluate(
			`document.querySelector('#decision-scope').dispatchEvent(new Event('change',{bubbles:true}))`,
			nil,
		),
		chromedp.SetValue("#decision-value", "-42.123456789", chromedp.ByQuery),
		chromedp.SetValue("#decision-currency", "USD", chromedp.ByQuery),
		chromedp.Focus("#decision-value", chromedp.ByQuery),
		chromedp.Evaluate(
			`document.querySelector('#decision-value').dispatchEvent(new Event('input',{bubbles:true}))`,
			nil,
		),
		chromedp.Poll(`document.querySelector('#decision-feedback').dataset.state==='valid'`, nil),
		chromedp.Evaluate(
			`document.activeElement.id==='decision-value'&&document.querySelector('#decision-value').value==='-42.123456789'`,
			&ok,
		),
	)

	if !ok {
		t.Fatal("debt correction lost draft or focus during preflight")
	}

	check(
		newDocument(chromedp.Navigate(origin+"/activity")),
		chromedp.WaitVisible("#activity-rows", chromedp.ByQuery),
		chromedp.Poll(
			`document.querySelector('[data-grid-ready="true"]')!==null&&!document.querySelector('[data-grid]').hasAttribute('aria-busy')`,
			nil,
		),
		newDocument(chromedp.Click(
			`//div[contains(@class,"tabulator-row") and contains(.,"Convert ")]//a[starts-with(@href,"/activity/")][1]`,
			chromedp.BySearch,
		)),
		chromedp.WaitVisible(".wake-card dl", chromedp.ByQuery),
		chromedp.Evaluate(
			`document.querySelector('h1').textContent==='Convert EUR to USD'&&document.body.textContent.includes('10.123456789 EUR')&&document.body.textContent.includes('Unknown USD')&&document.body.textContent.includes('-0.25 GBP')&&document.body.textContent.includes('Total not reported')&&document.body.textContent.includes('↳ ↳ regFINRATradingActivityFee')&&document.body.textContent.includes('-0.00100 GBP')`,
			&ok,
		),
	)

	if !ok {
		t.Fatal("conversion inspection")
	}

	check(
		newDocument(chromedp.Navigate(origin+"/activity")),
		chromedp.WaitVisible("#activity-rows", chromedp.ByQuery),
		chromedp.Poll(
			`document.querySelector('[data-grid-ready="true"]')!==null&&!document.querySelector('[data-grid]').hasAttribute('aria-busy')`,
			nil,
		),
		newDocument(chromedp.Click(
			`//div[contains(@class,'tabulator-row') and contains(.,'Synthetic debt')]//a[starts-with(@href,'/executions/')][1]`,
			chromedp.BySearch,
		)),
		chromedp.WaitVisible(".wake-card dl", chromedp.ByQuery),
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
		chromedp.WaitVisible("#activity-rows", chromedp.ByQuery),
		chromedp.Poll(
			`document.querySelector('[data-grid-ready="true"]')!==null&&!document.querySelector('[data-grid]').hasAttribute('aria-busy')`,
			nil,
		),
		newDocument(chromedp.Click(
			`//div[contains(@class,'tabulator-row') and contains(.,'Reported option')]//a[starts-with(@href,'/executions/')][1]`,
			chromedp.BySearch,
		)),
		chromedp.WaitVisible(".wake-card dl", chromedp.ByQuery),
		newDocument(chromedp.Click(`a[href*="/correct"]`, chromedp.ByQuery)),
		chromedp.WaitVisible("#decision-scope", chromedp.ByQuery),
		chromedp.SetValue("#decision-scope", "price", chromedp.ByQuery),
		chromedp.Evaluate(
			`document.querySelector('#decision-scope').dispatchEvent(new Event('change',{bubbles:true}))`,
			nil,
		),
		chromedp.SetValue("#decision-value", "2.999000000000001", chromedp.ByQuery),
		chromedp.SetValue("#decision-currency", "USD", chromedp.ByQuery),
		chromedp.SetValue("#decision-rationale", "Browser field correction", chromedp.ByQuery),
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
		newDocument(
			chromedp.Click(`form[aria-label="Correct a field or link"] button[type="submit"]`, chromedp.ByQuery),
		),
		chromedp.WaitVisible(`//li[contains(.,"Browser field correction")]`, chromedp.BySearch),
		chromedp.WaitVisible(`form[aria-label^="Release "]`, chromedp.ByQuery),
		chromedp.Evaluate(
			`document.body.textContent.includes('Active decisions')&&document.body.textContent.includes('Browser field correction')&&document.body.textContent.includes('admin/local/')&&history.state.wakeNavigation.trail.length>0&&new URL(document.querySelector('[data-return]').href).pathname.startsWith('/executions/')&&!new URL(location.href).searchParams.has('origin')&&!new URL(location.href).searchParams.has('parent')`,
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
		newDocument(chromedp.Click(`form[aria-label^="Release "] button`, chromedp.ByQuery)),
		chromedp.WaitVisible(`//p[text()='No active operator decisions.']`, chromedp.BySearch),
		chromedp.Evaluate(`document.querySelectorAll('form[aria-label^="Release "]').length===0`, &ok),
	)

	if !ok {
		t.Fatal("decision release")
	}

	check(
		newDocument(chromedp.Navigate(origin+connectionPath+"?view=reports")),
		chromedp.Click(`//summary[text()='Processing actions']`, chromedp.BySearch),
		newDocument(chromedp.Click(`form[action^="/processing/"] button`, chromedp.ByQuery)),
		chromedp.WaitNotPresent("form[data-submitting]", chromedp.ByQueryAll),
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
		chromedp.WaitVisible("#storage-recovery-title", chromedp.ByQuery),
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
	check(chromedp.WaitVisible("a[data-repair-source]", chromedp.ByQuery))
	navigate(chromedp.Evaluate(`document.querySelector('a[data-repair-source]').click()`, nil))
	check(
		chromedp.WaitVisible(`form[action$="/settings"]`, chromedp.ByQuery),
		chromedp.SetValue(
			`form[action$="/settings"] input[name="connection_name"]`,
			"Recovered IBKR",
			chromedp.ByQuery,
		),
		chromedp.SetValue(`form[action$="/settings"] input[name="setting.query_id"]`, "123", chromedp.ByQuery),
	)
	navigate(chromedp.Click(`form[action$="/settings"] button`, chromedp.ByQuery))
	check(chromedp.WaitVisible("a[data-repair-source]", chromedp.ByQuery))
	navigate(chromedp.Evaluate(`document.querySelector('a[data-repair-source]').click()`, nil))
	check(
		chromedp.WaitVisible(`form[action$="/secrets"]`, chromedp.ByQuery),
		chromedp.SetValue(`input[name="secret.token"]`, "synthetic-token", chromedp.ByQuery),
	)
	navigate(chromedp.Click(`form[action$="/secrets"] button`, chromedp.ByQuery))
	check(
		chromedp.WaitNotPresent(`p[id^="access-missing-"]`, chromedp.ByQueryAll),
		chromedp.SetValue("#openfigi-key", "synthetic-reference-key", chromedp.ByQuery),
	)
	navigate(chromedp.Click(`form[action="/reference/key"] button`, chromedp.ByQuery))
	check(
		chromedp.WaitVisible(`//p[text()='Reference access has been reviewed.']`, chromedp.BySearch),
		chromedp.WaitVisible(`form[action="/recovery/resume"] button:not([disabled])`, chromedp.ByQuery),
	)

	if reportCalls.Load() != 2 {
		t.Fatal("companion recreation contacted upstream", reportCalls.Load())
	}

	keyedReference.Store(true)

	check(
		newDocument(chromedp.Click(`form[action="/recovery/resume"] button`, chromedp.ByQuery)),
		chromedp.WaitVisible("#workflow-progress", chromedp.ByQuery),
		chromedp.WaitNotPresent("#storage-recovery-title", chromedp.ByQueryAll),
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
		chromedp.WaitVisible("#warehouse-reset-notice", chromedp.ByQuery),
		chromedp.Evaluate(
			`document.body.textContent.includes('Recovered IBKR')&&!document.querySelector('p[id^="access-missing-"]')&&!document.body.textContent.includes('Activated account:')`,
			&ok,
		),
	)

	if !ok || reportCalls.Load() != callsBefore {
		t.Fatal("fresh warehouse lost settings or contacted upstream before resume")
	}

	check(
		newDocument(chromedp.Click(`form[action="/recovery/resume"] button`, chromedp.ByQuery)),
		chromedp.WaitNotPresent("#storage-recovery-title", chromedp.ByQueryAll),
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
		chromedp.WaitVisible("#account-name", chromedp.ByQuery),
		chromedp.SetValue("#account-name", "Fresh confirmed account", chromedp.ByQuery),
		newDocument(chromedp.Click(`button[type="submit"]`, chromedp.ByQuery)),
		chromedp.WaitVisible("#activity-rows", chromedp.ByQuery),
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
		chromedp.WaitVisible("[data-grid-refresh]", chromedp.ByQuery),
		chromedp.Click("[data-grid-refresh]", chromedp.ByQuery),
		chromedp.Poll(
			`document.body.textContent.includes('Fresh confirmed account')&&document.body.textContent.includes('400.123456789')`,
			nil,
		),
	)
	// Add the supplementary query through the actual connection form. The same
	// encrypted token acquires and publishes a result without another activity.
	check(
		newDocument(chromedp.Navigate(origin+connectionPath+"?view=access")),
		chromedp.WaitVisible(`input[name="setting.activity_query_id"]`, chromedp.ByQuery),
		chromedp.SendKeys(`input[name="setting.activity_query_id"]`, "456", chromedp.ByQuery),
		newDocument(chromedp.Click(`form[action$="/settings"] button`, chromedp.ByQuery)),
	)
	clock.Add(6)

	select {
	case <-realizedSent:
	case <-time.After(10 * time.Second):
		t.Fatal("configured Activity query did not start")
	}

	clock.Add(10)
	check(
		newDocument(chromedp.Navigate(origin+"/activity?sort=realized&dir=desc")),
		chromedp.Poll(
			`document.querySelector('[data-grid-ready="true"]')!==null&&!document.querySelector('[data-grid]').hasAttribute('aria-busy')`,
			nil,
		),
		chromedp.Evaluate(`document.body.textContent.includes('12.34000000000000001 USD')`, &ok),
	)

	if !ok {
		check(
			chromedp.WaitVisible("[data-grid-refresh]", chromedp.ByQuery),
			chromedp.Click("[data-grid-refresh]", chromedp.ByQuery),
		)
	}

	check(
		chromedp.Poll(
			`document.querySelector('.tabulator-row [tabulator-field="realized"]').textContent==='12.34000000000000001 USD'`,
			nil,
		),
		newDocument(chromedp.Click(`.tabulator-row a[href^="/executions/"]`, chromedp.ByQuery)),
		chromedp.WaitVisible("[data-reported-realized]", chromedp.ByQuery),
		chromedp.Evaluate(
			`document.querySelector('[data-reported-realized]').textContent.includes('-100.0000 USD')`,
			&ok,
		),
	)

	if !ok {
		t.Fatal("reported allocated cost lost precision")
	}

	capture("reported-realized")

	check(
		newDocument(chromedp.Click(`a[href*="/correct"]`, chromedp.ByQuery)),
		newDocument(chromedp.Click(`a[href*="mode=evidence"]`, chromedp.ByQuery)),
		chromedp.Evaluate(
			`(()=>{const select=document.querySelector('#decision-candidate');select.value=Array.from(select.options).find(option=>option.textContent.startsWith('Partial activity evidence')).value;select.dispatchEvent(new Event('change',{bubbles:true}))})()`,
			nil,
		),
		newDocument(chromedp.Click(`form[data-candidate-compare] button`, chromedp.ByQuery)),
		chromedp.WaitVisible(`form[aria-label="Select retained evidence"]`, chromedp.ByQuery),
		chromedp.Evaluate(
			`document.querySelector('form[aria-label="Select retained evidence"] input[name="scope"]').value==='observation'&&document.querySelector('.wake-comparison').textContent.includes('12.34000000000000001 USD')`,
			&ok,
		),
	)

	if !ok {
		t.Fatal("P&L evidence selection used activity-field scope")
	}

	capture("reported-realized-selection")

	check(newDocument(chromedp.Click(`form[aria-label="Select retained evidence"] button`, chromedp.ByQuery)))
	check(chromedp.Evaluate(`!document.body.textContent.includes('Correction not saved')`, &ok))

	if !ok {
		t.Fatal("reported P&L selection failed")
	}

	for _, item := range []struct{ kind, want, scope, value string }{
		{"security-transfer", "3.00000000001", "direction", "out"},
		{"cash", "7.89000000001 USD", "cash_amount", "8.00000000001"},
	} {
		check(
			newDocument(chromedp.Navigate(origin+"/activity?kind="+item.kind)),
			chromedp.WaitVisible(`.tabulator-row a[href^="/activity/"]`, chromedp.ByQuery),
			newDocument(chromedp.Click(`.tabulator-row a[href^="/activity/"]`, chromedp.ByQuery)),
		)
		check(chromedp.Evaluate(`document.body.textContent.includes(`+strconv.Quote(item.want)+`)`, &ok))

		if !ok {
			t.Fatal("movement detail lost reported amount", item.kind)
		}

		capture(item.kind + "-detail")
		check(newDocument(chromedp.Click(`a[href*="/correct"]`, chromedp.ByQuery)))
		check(
			chromedp.SetValue("#decision-scope", item.scope, chromedp.ByQuery),
			chromedp.Evaluate(
				`document.querySelector('#decision-scope').dispatchEvent(new Event('change',{bubbles:true}))`,
				nil,
			),
			chromedp.WaitVisible("#decision-value", chromedp.ByQuery),
			chromedp.SetValue("#decision-value", item.value, chromedp.ByQuery),
			chromedp.Evaluate(
				`document.querySelector('#decision-value').dispatchEvent(new Event('input',{bubbles:true}));document.querySelector('#decision-value').dispatchEvent(new Event('change',{bubbles:true}))`,
				nil,
			),
			chromedp.Poll(`document.querySelector('#decision-feedback').dataset.state==='valid'`, nil),
			newDocument(chromedp.Click(`form[data-correction] button[type="submit"]`, chromedp.ByQuery)),
			chromedp.Evaluate(`!document.body.textContent.includes('Correction not saved')`, &ok),
		)

		if !ok {
			t.Fatal("movement correction failed", item.kind)
		}

		capture(item.kind + "-correction")
	}
	// Add a distinct brokerage account through the normal form while Flex remains active.
	tickCtx, stopClock := context.WithCancel(ctx)
	defer stopClock()

	go func() {
		ticker := time.NewTicker(400 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-tickCtx.Done():
				return
			case <-ticker.C:
				clock.Add(2)
			}
		}
	}()

	check(
		newDocument(chromedp.Navigate(origin+"/connections/new")),
		chromedp.WaitVisible("#connection-name-snaptrade", chromedp.ByQuery),
		chromedp.SetValue("#connection-name-snaptrade", "Synthetic SnapTrade", chromedp.ByQuery),
		chromedp.SetValue(`input[name="setting.client_id"]`, "synthetic-personal-client", chromedp.ByQuery),
		chromedp.SetValue(`input[name="secret.consumer_key"]`, "synthetic-personal-key", chromedp.ByQuery),
		newDocument(
			chromedp.Click(`form:has(#connection-name-snaptrade) button[type="submit"]`, chromedp.ByQuery),
		),
		chromedp.WaitVisible(`a[href*="/confirm"]`, chromedp.ByQuery),
		newDocument(chromedp.Click(`a[href*="/confirm"]`, chromedp.ByQuery)),
		chromedp.SetValue("#account-name", "Confirmed Fidelity account", chromedp.ByQuery),
		newDocument(chromedp.Click(`form[action$="/confirm"] button[type="submit"]`, chromedp.ByQuery)),
	)

	var snapAccount, flexAccount string
	check(
		chromedp.WaitVisible("#activity-account", chromedp.ByQuery),
		chromedp.Poll(`document.querySelector('[data-grid-ready="true"]')!==null`, nil),
		chromedp.Evaluate(
			`Array.from(document.querySelector('#activity-account').options).find(o=>o.text==='Confirmed Fidelity account').value`,
			&snapAccount,
		),
		chromedp.Evaluate(
			`Array.from(document.querySelector('#activity-account').options).find(o=>o.text==='Fresh confirmed account').value`,
			&flexAccount,
		),
	)
	check(
		chromedp.Evaluate(`(()=>{const select=document.querySelector('#activity-account');select.value=`+
			strconv.Quote(snapAccount)+`;select.form.requestSubmit()})()`, nil),
		chromedp.Poll(`new URL(location.href).searchParams.get('account')===`+strconv.Quote(snapAccount), nil),
		chromedp.Poll(
			`(()=>{const refresh=document.querySelector('[data-grid-refresh]');if(refresh&&!refresh.hidden)refresh.click();const rows=document.querySelectorAll('.tabulator-row');return rows.length===1&&rows[0].textContent.includes('19.12345678900001 USD')&&rows[0].textContent.includes('Confirmed Fidelity account')})()`,
			nil,
		),
	)

	check(
		newDocument(chromedp.Click(`.tabulator-row a[href^="/executions/"]`, chromedp.ByQuery)),
		newDocument(chromedp.Click(`a[href*="/correct"]`, chromedp.ByQuery)),
		chromedp.SetValue("#decision-scope", "price", chromedp.ByQuery),
		chromedp.Evaluate(
			`document.querySelector('#decision-scope').dispatchEvent(new Event('change',{bubbles:true}))`,
			nil,
		),
		chromedp.WaitVisible("#decision-value", chromedp.ByQuery),
		chromedp.SetValue("#decision-value", "21.12345678900001", chromedp.ByQuery),
		chromedp.Evaluate(
			`document.querySelector('#decision-value').dispatchEvent(new Event('input',{bubbles:true}));document.querySelector('#decision-value').dispatchEvent(new Event('change',{bubbles:true}))`,
			nil,
		),
		chromedp.Poll(`document.querySelector('#decision-feedback').dataset.state==='valid'`, nil),
		newDocument(chromedp.Click(`form[data-correction] button[type="submit"]`, chromedp.ByQuery)),
		chromedp.Poll(`document.body.textContent.includes('21.12345678900001')`, nil),
	)

	// Switching accounts must replace the rows and their timestamp provenance.
	check(
		newDocument(chromedp.Navigate(origin+"/positions?account="+flexAccount)),
		chromedp.Poll(`document.querySelector('[data-positions-state]').textContent==='Available'`, nil),
		chromedp.Evaluate(`(()=>{const select=document.querySelector('#positions-account');select.value=`+
			strconv.Quote(snapAccount)+`;select.form.requestSubmit()})()`, nil),
		chromedp.Poll(`new URL(location.href).searchParams.get('account')===`+strconv.Quote(snapAccount), nil),
		chromedp.Poll(
			`(()=>{const refresh=document.querySelector('[data-grid-refresh]');if(refresh&&!refresh.hidden)refresh.click();return document.querySelector('[data-positions-account]').textContent==='Confirmed Fidelity account'&&document.querySelector('[data-positions-state]').textContent==='Available'&&document.querySelector('[data-positions-as-of]').textContent.includes('SnapTrade fetch time')&&document.querySelector('.tabulator-table')?.textContent.includes('12.3400 units')&&document.querySelector('.tabulator-row [tabulator-field="cost"]')?.textContent==='14.2300 USD'})()`,
			nil,
		),
	)

	socket := filepath.Join(cfg.Paths.RuntimeDir, "wake.sock")

	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	defer transport.CloseIdleConnections()

	client := &http.Client{Transport: transport, Timeout: time.Second}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://wake/v1/snapshot", nil)
	if err != nil {
		t.Fatal(err)
	}

	reply, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reply.Body.Close() }()

	var snapshot application.FinancialSnapshot
	if err = json.NewDecoder(reply.Body).Decode(&snapshot); err != nil || reply.StatusCode != http.StatusOK {
		t.Fatalf("socket snapshot status=%d err=%v", reply.StatusCode, err)
	}

	accounts := map[string]string{}
	trades := map[string][]string{}
	for _, resource := range snapshot.Resources {
		if resource.Account != nil {
			accounts[resource.ID] = resource.Account.Name
		}

		if activity := resource.Activity; activity != nil && activity.Price != nil {
			trades[activity.AccountID] = append(trades[activity.AccountID], activity.Price.Amount)
		}
	}

	if accounts[flexAccount] != "Fresh confirmed account" ||
		accounts[snapAccount] != "Confirmed Fidelity account" ||
		len(trades[snapAccount]) != 1 ||
		trades[snapAccount][0] != "21.12345678900001" ||
		!slices.Contains(trades[flexAccount], "400.123456789") {
		t.Fatalf("socket account isolation: accounts=%v prices=%v", accounts, trades)
	}

	positionPath := "http://wake/v1/accounts/" + snapAccount + "/positions"

	request, err = http.NewRequestWithContext(ctx, http.MethodGet, positionPath, nil)
	if err != nil {
		t.Fatal(err)
	}

	positions, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = positions.Body.Close() }()

	var view application.PositionsView
	if err = json.NewDecoder(positions.Body).
		Decode(&view); err != nil || positions.StatusCode != http.StatusOK ||
		view.AsOf == nil || view.AsOf.Basis != "source_fetched" || len(view.Positions) != 1 ||
		view.Positions[0].Quantity.Unit != "units" ||
		view.Positions[0].InstrumentID == "" || view.Positions[0].UnitCostBasis == nil ||
		view.Positions[0].UnitCostBasis.Amount != "14.2300" {
		t.Fatalf("socket positions status=%d view=%+v err=%v", positions.StatusCode, view, err)
	}

	conditional := request.Clone(ctx)
	conditional.Header.Set("If-None-Match", positions.Header.Get("ETag"))

	unchanged, err := client.Do(conditional)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = unchanged.Body.Close() }()

	if unchanged.StatusCode != http.StatusNotModified {
		t.Fatalf("socket positions conditional status=%d", unchanged.StatusCode)
	}
}
