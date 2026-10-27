// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

//go:build browser

package daemon

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

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

	var reportCalls atomic.Int64

	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Logf("upstream %s at %s", r.URL.Path, now())

		switch r.URL.Path {
		case "/SendRequest":
			sent <- r.URL.Query().Get("p")

			_, _ = fmt.Fprint(
				w,
				`<FlexStatementResponse><Status>Success</Status><ReferenceCode>generation</ReferenceCode></FlexStatementResponse>`,
			)
		case "/GetStatement":
			reportCalls.Add(1)

			_, _ = fmt.Fprint(
				w,
				`<FlexQueryResponse type="TCF"><FlexStatements><FlexStatement accountId="U-SYNTHETIC" fromDate="2026-10-20" toDate="2026-10-21"><TradeConfirms><TradeConfirm accountId="U-SYNTHETIC" assetCategory="STK" symbol="SYN" description="Reported equity" conid="123" isin="US0000000001" transactionType="ExchTrade" levelOfDetail="EXECUTION" execID="fill-1" tradeID="trade-1" buySell="BUY" quantity="0.125" price="400.123456789" currency="USD" dateTime="2026-10-20;09:30:00.120 EDT" tradeDate="2026-10-20" amount="50.015432098625" netCash="-50.265432098625"/></TradeConfirms></FlexStatement><FlexStatement accountId="U-OTHER" fromDate="2026-10-20" toDate="2026-10-21"><TradeConfirms/></FlexStatement></FlexStatements></FlexQueryResponse>`,
			)
		case "/mapping":
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

	daemonContext, stopDaemon := context.WithCancel(context.Background())
	defer stopDaemon()

	ready := make(chan struct{})
	done := make(chan error, 1)
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

	defer func() {
		stopDaemon()

		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(10 * time.Second):
			t.Error("workflow shutdown did not join")
		}
	}()

	opts := append(chromedp.DefaultExecAllocatorOptions[:], chromedp.ExecPath(browser))

	allocator, stopBrowser := chromedp.NewExecAllocator(context.Background(), opts...)
	defer stopBrowser()

	ctx, cancel := chromedp.NewContext(allocator)
	defer cancel()

	ctx, deadline := context.WithTimeout(ctx, 60*time.Second)
	defer deadline()

	var ok bool

	origin := cfg.Admin.Origin()
	check := func(actions ...chromedp.Action) {
		t.Helper()

		if err := chromedp.Run(ctx, actions...); err != nil {
			t.Fatal(err)
		}
	}
	check(
		chromedp.Navigate(origin+"/login"),
		chromedp.WaitVisible("#unlock"),
		chromedp.SendKeys("#unlock", "synthetic-browser-unlock"),
		chromedp.Click(`button[type="submit"]`),
		chromedp.WaitVisible("#main-navigation"),
		chromedp.Click(`a[href="/connections"]`),
		chromedp.WaitVisible("#connection-name"),
		chromedp.SendKeys("#connection-name", "Synthetic IBKR"),
		chromedp.SendKeys("#query-id", "123"),
		chromedp.SendKeys("#flex-token", "synthetic-token"),
		chromedp.Click(`form[action="/connections"] button[type="submit"]`),
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
		chromedp.Poll(`document.querySelector('a[href*="account=U-SYNTHETIC"]') !== null`, nil),
		chromedp.Evaluate(
			`document.querySelector('#connection-name').value==='Unsubmitted draft' && document.body.textContent.includes('Unknown') && !document.body.textContent.includes('Activated account:')`,
			&ok,
		),
	)

	if !ok {
		t.Fatal("discovery metadata or active form lost")
	}

	check(
		chromedp.Evaluate(`document.querySelector('a[href*="account=U-SYNTHETIC"]').click()`, nil),
		chromedp.WaitVisible("#account-name"),
		chromedp.SendKeys("#account-name", "My confirmed account"),
		chromedp.Click(`button[type="submit"]`),
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
		chromedp.Evaluate(`document.querySelector('a[href^="/executions/"]').click()`, nil),
		chromedp.WaitVisible("dl"),
		chromedp.Evaluate(
			`document.body.textContent.includes('2026-10-20T13:30:00.120Z') && document.body.textContent.includes('accepted')`,
			&ok,
		),
	)

	if !ok {
		t.Fatal("execution inspection lost precision")
	}

	check(
		chromedp.Click(`a[href^="/instruments/"]`),
		chromedp.WaitVisible("h1"),
		chromedp.Evaluate(
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

	if reportCalls.Load() != 2 {
		t.Fatal("discovery did not stop after its bounded report", reportCalls.Load())
	}
}
