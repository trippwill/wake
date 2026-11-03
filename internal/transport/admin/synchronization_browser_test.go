// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

//go:build browser

package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/a-h/templ"
	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/chromedp"
	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/testbrowser"
	"go.trippwill.dev/wake/source"
)

func TestBrowserSynchronizationSummary(t *testing.T) {
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

	ref, _ := source.NewReference("synthetic", "account", "", "one")
	details, _ := application.NewAccountDetails("Synthetic account", "USD")

	account, err := application.NewAccount("one", "source", "synthetic", ref, details, "fixture")
	if err != nil {
		t.Fatal(err)
	}

	view := fixtureOverview(fixtureView{Account: account, Connections: []application.ConnectionView{{
		ID: "source", Name: "ibkr-flex", CanAcquire: true,
		Work: []application.WorkStatus{
			{Stream: "activity", Operation: "acquire", Progress: "retained", Scheduled: true},
			{Stream: "confirmations", Operation: "acquire", Progress: "retained", Scheduled: true},
		},
	}}})
	mux := http.NewServeMux()
	mux.Handle("GET /assets/", http.FileServerFS(assets))
	mux.Handle(
		"GET /",
		templ.Handler(SectionShell("Overview", "synthetic-csrf", "Overview", DashboardOverview(view))),
	)

	server := httptest.NewServer(mux)
	defer server.Close()

	allocator, stop := chromedp.NewExecAllocator(context.Background(),
		append(chromedp.DefaultExecAllocatorOptions[:], chromedp.ExecPath(browser))...)
	defer stop()

	ctx, cancel := chromedp.NewContext(allocator)
	defer cancel()

	ctx, deadline := context.WithTimeout(ctx, 45*time.Second)
	defer deadline()

	if err = chromedp.Run(ctx, testbrowser.NewDocument(chromedp.Navigate(server.URL)),
		chromedp.WaitVisible("#synchronization-title", chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}

	for _, theme := range []string{"light", "dark"} {
		for _, width := range []int64{1280, 768, 390, 320} {
			var valid bool
			if err = chromedp.Run(ctx,
				emulation.SetDeviceMetricsOverride(width, 900, 1, false),
				chromedp.Evaluate(`document.documentElement.dataset.theme=`+strconv.Quote("wake-"+theme), nil),
				chromedp.Evaluate(`(()=>{
					const rows=Array.from(document.querySelectorAll('.wake-sync-work > div'));
					return document.documentElement.scrollWidth<=innerWidth+1&&rows.length===2&&
						rows.map(row=>row.querySelector('dt').textContent.trim()).join(',')==='Activity,Confirmations'&&
						rows.every((row,index)=>{
							const label=row.querySelector('dt').getBoundingClientRect(),status=row.querySelector('dd').getBoundingClientRect();
							return row.querySelector('dd').textContent==='Evidence retained'&&label.right<=status.left&&
								(index===0||rows[index-1].getBoundingClientRect().bottom<=row.getBoundingClientRect().top);
						})&&document.querySelector('.wake-source-summary a').getAttribute('href')==='/connections/source?view=status';
				})()`, &valid),
			); err != nil || !valid {
				t.Fatalf("%s at %dpx: stream rows overlap, overflow or lose their meaning: %v", theme, width, err)
			}

			if dir := os.Getenv("WAKE_SCREENSHOT_DIR"); dir != "" && (width == 1280 || width == 390) {
				var png []byte
				if err = chromedp.Run(
					ctx,
					chromedp.Screenshot(`section[aria-labelledby="synchronization-title"]`, &png, chromedp.ByQuery),
				); err != nil {
					t.Fatal(err)
				}
				//nolint:gosec // Caller-selected synthetic screenshot directory and fixed filenames.
				if err = os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				//nolint:gosec // Caller-selected synthetic screenshot directory and fixed filenames.
				if err = os.WriteFile(
					filepath.Join(dir, "synchronization-"+theme+"-"+strconv.FormatInt(width, 10)+".png"),
					png,
					0o600,
				); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
}
