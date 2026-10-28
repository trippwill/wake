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
	"sync/atomic"
	"testing"
	"time"

	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"go.trippwill.dev/wake/internal/credential"
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
	navigationClick := func(selector string, options ...chromedp.QueryOption) chromedp.Action {
		return chromedp.ActionFunc(func(step context.Context) error {
			_, err := chromedp.RunResponse(step, chromedp.Click(selector, options...))
			return err
		})
	}
	// Refresh may replace the document. Evaluate each attempt in the current
	// document instead of retaining an isolated-world polling context across it.
	pollDocument := func(expression string, result *bool) chromedp.Action {
		return chromedp.ActionFunc(func(step context.Context) error {
			ticker := time.NewTicker(50 * time.Millisecond)
			defer ticker.Stop()

			for {
				var matched bool
				if err := chromedp.Run(step, chromedp.Evaluate(expression, &matched)); err == nil && matched {
					if result != nil {
						*result = true
					}

					return nil
				}

				select {
				case <-step.Done():
					return step.Err()
				case <-ticker.C:
				}
			}
		})
	}
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
	check(
		emulation.SetDeviceMetricsOverride(1440, 1000, 1, false),
		chromedp.Navigate(origin+"/login"),
		chromedp.WaitVisible("#unlock"),
		chromedp.SendKeys("#unlock", "synthetic-browser-unlock"),
		navigationClick(`button[type="submit"]`),
		chromedp.WaitVisible("#main-navigation"),
		navigationClick(`a[href="/connections"]`),
		chromedp.WaitVisible("#connection-name"),
		chromedp.SendKeys("#connection-name", "Synthetic IBKR"),
		chromedp.SendKeys("#query-id", "123"),
		chromedp.SendKeys("#flex-token", "synthetic-token"),
		navigationClick(`form[action="/connections"] button[type="submit"]`),
		chromedp.WaitVisible(`form[action^="/connections/"][action$="/settings"]`),
		chromedp.WaitVisible("#connection-name"),
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
	check(chromedp.SendKeys("#connection-name", "Unsubmitted draft"))
	clock.Add(10)
	check(
		pollDocument(`document.querySelector('a[href*="account=U-SYNTHETIC"]') !== null`, nil),
		pollDocument(
			`document.querySelector('#connection-name').value==='Unsubmitted draft' && document.body.textContent.includes('Unknown') && !document.body.textContent.includes('Activated account:')`,
			&ok,
		),
	)

	if !ok {
		t.Fatal("discovery metadata or active form lost")
	}

	check(
		navigationClick(`a[href*="account=U-SYNTHETIC"]`),
		chromedp.WaitVisible("#account-name"),
		chromedp.SendKeys("#account-name", "My confirmed account"),
		navigationClick(`button[type="submit"]`),
		chromedp.WaitVisible("#activity-rows"),
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
		pollDocument(
			`document.querySelector('a[href^="/executions/"]') !== null && document.body.textContent.includes('REFERENCE EQUITY')`,
			nil,
		),
		pollDocument(
			`document.body.textContent.includes('0.125') && document.body.textContent.includes('400.123456789') && document.body.textContent.includes('My confirmed account')`,
			&ok,
		),
	)

	if !ok {
		t.Fatal("missing exact financial view")
	}

	check(
		navigationClick(`a[href^="/executions/"]`),
		chromedp.WaitVisible("dl"),
		pollDocument(
			`document.body.textContent.includes('2026-10-20T13:30:00.120Z') && document.body.textContent.includes('accepted')`,
			&ok,
		),
	)

	if !ok {
		t.Fatal("execution inspection lost precision")
	}

	check(
		navigationClick(`a[href^="/instruments/"]:not([href$="/correct"])`),
		chromedp.WaitVisible("h1"),
		pollDocument(
			`document.querySelector('h1').textContent==='REFERENCE EQUITY' && document.body.textContent.includes('BBG000000002')`,
			&ok,
		),
	)

	if !ok {
		t.Fatal("reference inspection")
	}

	if dir := os.Getenv("WAKE_SCREENSHOT_DIR"); dir != "" {
		var png []byte
		check(chromedp.FullScreenshot(&png, 90))

		//nolint:gosec // Caller-selected synthetic screenshot directory.
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}

		//nolint:gosec // Caller-selected synthetic screenshot path.
		if err := os.WriteFile(filepath.Join(dir, "m2-instrument.png"), png, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	check(
		chromedp.Navigate(origin+"/activity"),
		chromedp.WaitVisible("#activity-rows"),
		navigationClick(
			`//tr[contains(.,"Reported option")]//a[starts-with(@href,"/executions/")][1]`,
			chromedp.BySearch,
		),
		chromedp.WaitVisible("dl"),
		pollDocument(
			`document.body.textContent.includes('Quantity (contracts)') && document.body.textContent.includes('100.001 USD') && document.body.textContent.includes('Premium multiplier') && document.body.textContent.includes('Unknown')`,
			&ok,
		),
	)

	if !ok {
		t.Fatal("option inspection")
	}

	check(
		navigationClick(`a[href^="/instruments/"]:not([href$="/correct"])`),
		chromedp.WaitVisible("h1"),
		navigationClick(`a[href^="/instruments/"]:not([href$="/correct"])`),
		chromedp.WaitVisible("h1"),
		pollDocument(`document.querySelector('h1').textContent==='REFERENCE EQUITY'`, &ok),
	)

	if !ok {
		t.Fatal("option underlier navigation")
	}

	check(
		chromedp.Navigate(origin+"/activity"),
		chromedp.WaitVisible("#activity-rows"),
		navigationClick(
			`//tr[contains(.,"assignment")]//a[starts-with(@href,"/activity/")][1]`,
			chromedp.BySearch,
		),
		chromedp.WaitVisible("dl"),
		pollDocument(
			`document.querySelector('h1').textContent==='assignment' && document.body.textContent.includes('assignment-1') && document.body.textContent.includes('No explicit lifecycle relationship reported')`,
			&ok,
		),
	)

	if !ok {
		t.Fatal("lifecycle inspection")
	}

	check(
		chromedp.Navigate(origin+"/activity"),
		chromedp.WaitVisible("#activity-rows"),
		navigationClick(
			`//tr[contains(.,"Synthetic debt")]//a[starts-with(@href,"/executions/")][1]`,
			chromedp.BySearch,
		),
		chromedp.WaitVisible("dl"),
		pollDocument(
			`document.body.textContent.includes('Quantity (nominal)')&&document.body.textContent.includes('-12.3456789 USD')&&document.body.textContent.includes('quotation basis unknown')&&document.body.textContent.includes('Coupon rate (reported)')`,
			&ok,
		),
	)

	if !ok {
		t.Fatal("debt inspection")
	}

	check(
		chromedp.Navigate(origin+"/activity"),
		chromedp.WaitVisible("#activity-rows"),
		navigationClick(
			`//tr[contains(.,"Currency conversion")]//a[starts-with(@href,"/activity/")][1]`,
			chromedp.BySearch,
		),
		chromedp.WaitVisible("dl"),
		pollDocument(
			`document.querySelector('h1').textContent==='Currency conversion'&&document.body.textContent.includes('10.123456789 EUR')&&document.body.textContent.includes('Unknown USD')&&document.body.textContent.includes('-0.25 GBP')`,
			&ok,
		),
	)

	if !ok {
		t.Fatal("conversion inspection")
	}

	check(
		chromedp.Navigate(origin+"/activity"),
		chromedp.WaitVisible("#activity-rows"),
		pollDocument(
			`document.body.textContent.includes('Source corrections')&&document.body.textContent.includes('cancellation')&&document.body.textContent.includes('applied')&&document.body.textContent.includes('cancelled')&&document.querySelector('a[href^="/activity/"]')!==null`,
			&ok,
		),
	)

	if !ok {
		t.Fatal("correction status/history")
	}

	var submitted string
	check(
		chromedp.Navigate(origin+"/activity"),
		chromedp.WaitVisible("#activity-rows"),
		navigationClick(
			`//tr[contains(.,"Reported option")]//a[starts-with(@href,"/executions/")][1]`,
			chromedp.BySearch,
		),
		chromedp.WaitVisible("dl"),
		navigationClick(`a[href$="/correct"]`),
		chromedp.WaitVisible("#decision-scope"),
		chromedp.SetValue("#decision-scope", "price"),
		chromedp.SetValue("#decision-value", "2.999000000000001"),
		chromedp.SetValue("#decision-currency", "USD"),
		chromedp.SetValue("#decision-rationale", "Browser field correction"),
		chromedp.Evaluate(
			`new URLSearchParams(new FormData(document.querySelector('form[aria-label="Correct a field or link"]'))).toString()`,
			&submitted,
		),
		navigationClick(`form[aria-label="Correct a field or link"] button`),
		chromedp.WaitVisible(`//li[contains(.,"Browser field correction")]`, chromedp.BySearch),
		chromedp.WaitVisible("#decision-scope"),
		pollDocument(
			`document.body.textContent.includes('Active decision')&&document.body.textContent.includes('Browser field correction')&&document.body.textContent.includes('admin/local/')`,
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
		navigationClick(`form[aria-label^="Release "] button`),
		chromedp.WaitVisible(`//li[contains(.,"release")]`, chromedp.BySearch),
		chromedp.WaitVisible("#decision-scope"),
		pollDocument(`!document.body.textContent.includes('Active decision')`, &ok),
	)

	if !ok {
		t.Fatal("decision release")
	}

	check(
		chromedp.Navigate(origin+"/connections"),
		chromedp.WaitVisible("#workflow-progress"),
		navigationClick(`form[action^="/processing/"] button`),
		chromedp.WaitVisible("#workflow-progress"),
		chromedp.WaitVisible(`form[action="/recovery/rebuild"] button`),
		navigationClick(`form[action="/recovery/rebuild"] button`),
		chromedp.WaitVisible(`//section[@id='workflow-progress']//p[contains(.,'Rebuild ')]`, chromedp.BySearch),
		pollDocument(
			`Array.from(document.querySelectorAll('#workflow-progress p')).some(p=>p.textContent.includes('Rebuild ')&&p.textContent.includes('complete'))`,
			nil,
		),
		pollDocument(`document.body.textContent.includes('Processing complete')`, &ok),
	)

	if !ok {
		t.Fatal("processing/rebuild browser recovery")
	}

	check(
		chromedp.Navigate(origin+"/activity"),
		chromedp.WaitVisible("#activity-rows"),
		pollDocument(
			`document.querySelectorAll('tbody tr').length===5&&document.body.textContent.includes('400.123456789')&&document.body.textContent.includes('cancelled')`,
			&ok,
		),
	)

	if !ok {
		t.Fatal("recovery duplicated or lost financial views")
	}

	if reportCalls.Load() != 2 {
		t.Fatal("discovery did not stop after its bounded report", reportCalls.Load())
	}
	// Recover from losing both companions using only normal authenticated forms.
	stopCurrent()

	for _, path := range []string{cfg.Storage.ConnectionsPath, cfg.Storage.OperationalPath} {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}

	clock.Add(86400)
	startDaemon()
	check(
		chromedp.Navigate(origin+"/activity"),
		chromedp.WaitVisible("#activity-rows"),
		pollDocument(
			`document.querySelectorAll('tbody tr').length===5&&document.body.textContent.includes('400.123456789')`,
			&ok,
		),
	)

	if !ok {
		t.Fatal("warehouse-only inspection lost history")
	}

	check(
		chromedp.Navigate(origin+"/connections"),
		chromedp.WaitVisible("#storage-recovery-title"),
		pollDocument(
			`document.body.textContent.includes('Access configuration is missing or incomplete')`,
			&ok,
		),
	)

	if !ok {
		t.Fatal("missing access not visible")
	}
	// The resume action cannot grant authority before access and reference review.
	check(
		navigationClick(`form[action="/recovery/resume"] button`),
		chromedp.WaitVisible(`[role="alert"]`),
	)
	check(
		chromedp.Navigate(origin+"/connections"),
		chromedp.WaitVisible("#storage-recovery-title"),
		chromedp.SetValue(
			`form[action$="/settings"] input[name="name"]`,
			"Recovered IBKR",
		),
		chromedp.SetValue(`form[action$="/settings"] input[name="query_id"]`, "123"),
		navigationClick(`form[action$="/settings"] button`),
		chromedp.WaitVisible(`//h2[text()="Recovered IBKR"]`, chromedp.BySearch),
		chromedp.SetValue(
			`input[id^="token-"]`,
			"synthetic-token",
		),
		navigationClick(`form[action$="/token"] button`),
		chromedp.WaitNotPresent(`p[id^="access-missing-"]`),
		chromedp.SetValue("#openfigi-key", "synthetic-reference-key"),
		navigationClick(`form[action="/reference/key"] button`),
		chromedp.WaitNotPresent("#reference-review-required"),
	)

	if reportCalls.Load() != 2 {
		t.Fatal("companion recreation contacted upstream", reportCalls.Load())
	}

	keyedReference.Store(true)

	check(
		navigationClick(`form[action="/recovery/resume"] button`),
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
		chromedp.Navigate(origin+"/connections"),
		chromedp.WaitVisible("#warehouse-reset-notice"),
		pollDocument(
			`document.body.textContent.includes('Recovered IBKR')&&!document.querySelector('p[id^="access-missing-"]')&&!document.body.textContent.includes('Activated account:')`,
			&ok,
		),
	)

	if !ok || reportCalls.Load() != callsBefore {
		t.Fatal("fresh warehouse lost settings or contacted upstream before resume")
	}

	check(
		navigationClick(`form[action="/recovery/resume"] button`),
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
		pollDocument(`document.querySelector('a[href*="account=U-SYNTHETIC"]')!==null`, nil),
		pollDocument(`!document.body.textContent.includes('Activated account:')`, &ok),
	)

	if !ok || reportCalls.Load() != callsBefore+1 {
		t.Fatal("rediscovery did not stop for confirmation")
	}

	check(
		navigationClick(`a[href*="account=U-SYNTHETIC"]`),
		chromedp.WaitVisible("#account-name"),
		chromedp.SetValue("#account-name", "Fresh confirmed account"),
		navigationClick(`button[type="submit"]`),
		chromedp.WaitVisible("#activity-rows"),
	)
	clock.Add(6)

	select {
	case <-sent:
	case <-time.After(10 * time.Second):
		t.Fatal("reconfirmed account did not start acquisition")
	}

	clock.Add(10)
	check(
		pollDocument(
			`document.body.textContent.includes('Fresh confirmed account')&&document.body.textContent.includes('400.123456789')`,
			nil,
		),
	)
}
