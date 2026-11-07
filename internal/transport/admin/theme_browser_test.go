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
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/a-h/templ"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
	"go.trippwill.dev/wake/internal/config"
	"go.trippwill.dev/wake/internal/testbrowser"
)

func TestBrowserThemePreference(t *testing.T) {
	browser := os.Getenv("WAKE_BROWSER")
	if browser == "" {
		for _, name := range []string{"brave-browser", "chromium", "google-chrome"} {
			if candidate, err := exec.LookPath(name); err == nil {
				browser = candidate
				break
			}
		}
	}

	if browser == "" {
		t.Fatal("Chromium required; set WAKE_BROWSER")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /assets/", serveAsset)
	// The token deliberately matches a CSS selector as text in hidden metadata.
	mux.Handle(
		"GET /theme",
		templ.Handler(Overview("synthetic-h1-token", "/system/configuration", Status{}, config.Config{})),
	)

	server := httptest.NewServer(mux)
	defer server.Close()

	allocator, stop := chromedp.NewExecAllocator(context.Background(),
		append(chromedp.DefaultExecAllocatorOptions[:], chromedp.ExecPath(browser))...)
	defer stop()

	ctx, cancel := chromedp.NewContext(allocator)
	defer cancel()

	ctx, deadline := context.WithTimeout(ctx, 20*time.Second)
	defer deadline()

	var gzipAssets atomic.Int64
	chromedp.ListenTarget(ctx, func(event any) {
		if response, ok := event.(*network.EventResponseReceived); ok &&
			strings.Contains(response.Response.URL, "/assets/") &&
			response.Response.Headers["Content-Encoding"] == "gzip" {
			gzipAssets.Add(1)
		}
	})

	if err := chromedp.Run(ctx,
		network.Enable(),
		testbrowser.NewDocument(chromedp.Navigate(server.URL+"/theme")),
		chromedp.WaitVisible("h1", chromedp.ByQuery),
		chromedp.WaitVisible("[data-theme-preference]", chromedp.ByQuery),
	); err != nil {
		t.Fatal("theme page", err)
	}

	if gzipAssets.Load() == 0 {
		t.Fatal("browser did not receive any compressed assets")
	}

	for _, preference := range []string{"dark", "light"} {
		ready := `document.documentElement.dataset.theme===` + strconv.Quote("wake-"+preference) +
			` && document.querySelector('[data-theme-preference]').value===` + strconv.Quote(preference) +
			` && localStorage.getItem('wake.theme')===` + strconv.Quote(preference)
		if err := chromedp.Run(
			ctx,
			chromedp.SetValue("[data-theme-preference]", preference, chromedp.ByQuery),
			chromedp.Evaluate(
				`document.querySelector('[data-theme-preference]').dispatchEvent(new Event('change',{bubbles:true}))`,
				nil,
			),
			chromedp.Poll(ready, nil),
			testbrowser.NewDocument(chromedp.Reload()),
			chromedp.WaitVisible("h1", chromedp.ByQuery),
			chromedp.Poll(ready, nil),
		); err != nil {
			t.Fatalf("choose %s and retain it after reload: %v", preference, err)
		}
	}
}
