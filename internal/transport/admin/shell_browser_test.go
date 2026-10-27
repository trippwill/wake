// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

//go:build browser

package admin

import (
	"context"
	"net"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/chromedp"
)

func TestBrowserShell(t *testing.T) {
	browser := os.Getenv("WAKE_BROWSER")
	if browser == "" {
		for _, name := range []string{"brave-browser", "chromium", "google-chrome"} {
			p, err := exec.LookPath(name)
			if err == nil {
				browser = p
				break
			}
		}
	}

	if browser == "" {
		t.Fatal("Chromium required; set WAKE_BROWSER")
	}

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	origin := "http://" + l.Addr().String()

	dir := t.TempDir()
	if err = SetUnlock(dir, []byte("synthetic-browser-unlock")); err != nil {
		t.Fatal(err)
	}

	auth, _, err := Open(dir, "local", origin)
	if err != nil {
		t.Fatal(err)
	}
	defer auth.Close()

	h, err := NewHandler(auth, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}

	server := httptest.NewUnstartedServer(h)

	_ = server.Listener.Close()
	server.Listener = l
	server.Start()
	defer server.Close()

	opts := append(chromedp.DefaultExecAllocatorOptions[:], chromedp.ExecPath(browser))

	allocator, stop := chromedp.NewExecAllocator(context.Background(), opts...)
	defer stop()

	ctx, cancel := chromedp.NewContext(allocator)
	defer cancel()

	ctx, deadline := context.WithTimeout(ctx, 30*time.Second)
	defer deadline()

	var ok bool

	err = chromedp.Run(
		ctx,
		chromedp.Navigate(origin+"/login"),
		chromedp.WaitVisible("#unlock"),
		chromedp.SendKeys("#unlock", "synthetic-browser-unlock"),
		chromedp.Click(`button[type="submit"]`),
		chromedp.WaitVisible("#main-navigation"),
		chromedp.Evaluate(
			`document.querySelectorAll('#main-navigation a').length === 3 && !document.querySelector('a[href="/trades"]')`,
			&ok,
		),
	)
	if err != nil || !ok {
		t.Fatal("sign-in/navigation", ok, err)
	}

	err = chromedp.Run(
		ctx,
		chromedp.Click(`a[href="/configuration"]`),
		chromedp.WaitVisible("h1"),
		chromedp.Evaluate(
			`document.querySelector('h1').textContent==='Configuration' && document.body.textContent.includes('No acquisition providers')`,
			&ok,
		),
	)
	if err != nil || !ok {
		t.Fatal("configuration", ok, err)
	}

	err = chromedp.Run(
		ctx,
		chromedp.SetValue("[data-theme-preference]", "dark"),
		chromedp.Evaluate(
			`document.querySelector('[data-theme-preference]').dispatchEvent(new Event('change',{bubbles:true}))`,
			nil,
		),
		chromedp.Reload(),
		chromedp.WaitVisible("h1"),
		chromedp.Evaluate(`document.documentElement.dataset.theme === 'wake-dark'`, &ok),
	)
	if err != nil || !ok {
		t.Fatal("theme persistence", ok, err)
	}

	err = chromedp.Run(
		ctx,
		chromedp.Click(`a[href="/status"]`),
		chromedp.WaitVisible("h1"),
		chromedp.Evaluate(`document.body.textContent.includes('Daemon running')`, &ok),
	)
	if err != nil || !ok {
		t.Fatal("status", ok, err)
	}

	err = chromedp.Run(ctx, emulation.SetDeviceMetricsOverride(390, 844, 1, false))
	if err != nil {
		t.Fatal(err)
	}

	if dir := os.Getenv("WAKE_SCREENSHOT_DIR"); dir != "" {
		var png []byte
		if err = chromedp.Run(ctx, chromedp.FullScreenshot(&png, 90)); err != nil {
			t.Fatal(err)
		}
		//nolint:gosec // Caller-selected test artifact directory.
		if err = os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		//nolint:gosec // Caller-selected test artifact directory.
		if err = os.WriteFile(filepath.Join(dir, "foundation-status.png"), png, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}
