// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

//go:build browser

package admin

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

func TestBrowserRefreshPreservesFocusAcquiredDuringFetch(t *testing.T) {
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

	script, err := assets.ReadFile("assets/app.js")
	if err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/app.js" {
			w.Header().Set("Content-Type", "text/javascript")
			_, _ = w.Write(script)

			return
		}

		w.Header().Set("Content-Type", "text/html")
		// Resolve the response body only after browser focus moves into the region.
		_, _ = fmt.Fprint(
			w,
			`<!doctype html><html><body><div data-refresh="/region"><a id="target" href="#">Keep focus</a></div><script>window.EventSource=class{addEventListener(name,fn){window.triggerRefresh=fn;document.body.dataset.triggerReady="true";}close(){}};window.fetch=async()=>({ok:true,redirected:false,text:()=>new Promise(resolve=>{document.body.dataset.bodyPending="true";window.deliver=()=>resolve('<a id="replacement" href="#">New content</a>');})});</script><script src="/app.js"></script></body></html>`,
		)
	}))
	defer server.Close()

	opts := append(chromedp.DefaultExecAllocatorOptions[:], chromedp.ExecPath(browser))

	allocator, stop := chromedp.NewExecAllocator(context.Background(), opts...)
	defer stop()

	ctx, cancel := chromedp.NewContext(allocator)
	defer cancel()

	if err = chromedp.Run(ctx); err != nil {
		t.Fatal(err)
	}

	ctx, deadline := context.WithTimeout(ctx, 20*time.Second)
	defer deadline()

	var focused bool

	err = chromedp.Run(
		ctx,
		chromedp.Navigate(server.URL),
		chromedp.WaitVisible("#target"),
		chromedp.Poll(`document.body.dataset.triggerReady==='true'`, nil, chromedp.WithPollingMutation()),
		chromedp.Evaluate(`void window.triggerRefresh()`, nil),
		chromedp.Poll(`document.body.dataset.bodyPending==='true'`, nil, chromedp.WithPollingMutation()),
		chromedp.Evaluate(`document.querySelector("#target").focus()`, nil),
		chromedp.Evaluate(`window.deliver()`, nil),
		chromedp.Evaluate(
			`document.activeElement.id==='target' && !document.querySelector('#replacement')`,
			&focused,
		),
	)

	if err != nil || !focused {
		t.Fatal("refresh removed newly focused link", focused, err)
	}
}
