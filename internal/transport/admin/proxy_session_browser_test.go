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
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"go.trippwill.dev/wake/internal/testbrowser"
)

func TestBrowserProxySession(t *testing.T) {
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

	proxy, auth, _ := newProxySessionServer(t)

	var now atomic.Int64
	now.Store(time.Now().Unix())
	auth.now = func() time.Time { return time.Unix(now.Load(), 0) }

	entry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `<a id="wake" href="%s/connections/reference">Open Wake</a>`, proxy.URL)
	}))
	defer entry.Close()
	// The synthetic proxy uses an httptest certificate; keep the browser sandbox enabled.
	opts := append(
		chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(browser),
		chromedp.Flag("ignore-certificate-errors", true),
	)

	allocator, stop := chromedp.NewExecAllocator(context.Background(), opts...)
	defer stop()

	ctx, cancel := chromedp.NewContext(allocator)
	defer cancel()

	ctx, deadline := context.WithTimeout(ctx, 45*time.Second)
	defer deadline()

	if err := chromedp.Do(
		ctx,
		chromedp.Navigate(strings.Replace(entry.URL, "127.0.0.1", "localhost", 1)),
		chromedp.WaitVisible(chromedp.CSS("#wake")),
	); err != nil {
		t.Fatal(err)
	}

	response, err := chromedp.RunResponse(ctx, chromedp.Click(chromedp.CSS("#wake")))
	if err != nil || response == nil || response.Status != http.StatusOK ||
		response.URL != proxy.URL+"/connections/reference" {
		t.Fatal("cross-site entry did not serve the requested page", response, err)
	}

	var before, after string
	if err = chromedp.Do(
		ctx,
		chromedp.WaitVisible(chromedp.CSS(`form[action="/reference/key"]`)),
		testbrowser.Evaluate(`document.querySelector('input[name="csrf"]').value`, &before),
	); err != nil {
		t.Fatal(err)
	}

	now.Add(int64(auth.lifetime.Seconds()) + 1)

	if err = chromedp.Do(
		ctx,
		chromedp.Evaluate[chromedp.Void](
			`window.htmx.ajax("GET", "/system/contact", {source: document.body, swap: "none"});`,
		),
		chromedp.WaitVisible(chromedp.CSS("#status-sign-in")),
		testbrowser.Evaluate(`document.querySelector('input[name="csrf"]').value`, &after),
	); err != nil || before != after {
		t.Fatal("background expiry changed the form token", err)
	}

	response, err = chromedp.RunResponse(ctx, chromedp.Click(chromedp.CSS("#status-sign-in")))
	if err != nil || response == nil || response.Status != http.StatusOK {
		t.Fatal("proxy recovery navigation", response, err)
	}

	if err = chromedp.Do(
		ctx,
		chromedp.Navigate(proxy.URL+"/connections/reference"),
		chromedp.WaitVisible(chromedp.CSS(`form[action="/reference/key"]`)),
		testbrowser.Evaluate(`document.querySelector('input[name="csrf"]').value`, &after),
	); err != nil ||
		before == after {
		t.Fatal("document navigation did not renew the session", err)
	}
}
