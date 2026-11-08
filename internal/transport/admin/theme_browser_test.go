// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

//go:build browser

package admin

import (
	"context"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
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

	server := httptest.NewUnstartedServer(nil)
	origin := "http://" + server.Listener.Addr().String()

	dir := t.TempDir()
	if err := SetUnlock(dir, []byte("synthetic-theme-unlock")); err != nil {
		t.Fatal(err)
	}

	auth, _, err := Open(dir, "local", origin)
	if err != nil {
		t.Fatal(err)
	}
	defer auth.Close()

	server.Config.Handler, err = NewHandler(auth, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}

	server.Start()
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

	session := httptest.NewRecorder()
	auth.issue(session)
	cookie := session.Result().Cookies()[0]
	check := func(actions ...chromedp.Action) {
		t.Helper()

		if err := chromedp.Run(ctx, actions...); err != nil {
			t.Fatal(err)
		}
	}
	check(
		network.Enable(),
		chromedp.ActionFunc(func(ctx context.Context) error {
			return network.SetCookie(cookie.Name, cookie.Value).WithURL(origin).WithHTTPOnly(true).Do(ctx)
		}),
		testbrowser.NewDocument(chromedp.Navigate(origin+"/system")),
		chromedp.Poll(`!document.querySelector('.wake-toolbar,[data-theme-preference]')`, nil),
		testbrowser.NewDocument(
			chromedp.Click(`nav[aria-label="System views"] a[href="/system/settings"]`, chromedp.ByQuery),
		),
		chromedp.WaitVisible("[data-theme-preference]", chromedp.ByQuery),
		chromedp.Poll(
			`document.querySelector('nav[aria-label="System views"] [aria-current="page"]')?.textContent==='Settings'`,
			nil,
		),
	)

	if gzipAssets.Load() == 0 {
		t.Fatal("browser did not receive any compressed assets")
	}

	choose := func(preference string) {
		t.Helper()
		check(
			chromedp.SetValue("[data-theme-preference]", preference, chromedp.ByQuery),
			chromedp.Evaluate(
				`document.querySelector('[data-theme-preference]').dispatchEvent(new Event('change',{bubbles:true}))`,
				nil,
			),
		)
	}
	for _, preference := range []string{"light", "dark"} {
		ready := `document.documentElement.dataset.theme===` + strconv.Quote("wake-"+preference) +
			` && document.querySelector('[data-theme-preference]').value===` + strconv.Quote(preference) +
			` && localStorage.getItem('wake.theme')===` + strconv.Quote(preference)
		choose(preference)
		check(
			chromedp.Poll(ready, nil),
			testbrowser.NewDocument(chromedp.Reload()),
			chromedp.WaitVisible("h1", chromedp.ByQuery),
			chromedp.Poll(ready, nil),
		)
	}

	check(
		chromedp.NavigateBack(),
		chromedp.Poll(
			`location.pathname==='/system' && document.documentElement.dataset.theme==='wake-dark'`,
			nil,
		),
		chromedp.NavigateForward(),
		chromedp.Poll(
			`location.pathname==='/system/settings' && document.querySelector('[data-theme-preference]')?.value==='dark'`,
			nil,
		),
		testbrowser.NewDocument(chromedp.Navigate(origin+"/login")),
		chromedp.WaitVisible("#unlock", chromedp.ByQuery),
		chromedp.Poll(
			`document.documentElement.dataset.theme==='wake-dark' && !document.querySelector('.wake-toolbar,[data-theme-preference],#daemon-contact')`,
			nil,
		),
		chromedp.NavigateBack(),
		chromedp.WaitVisible("[data-theme-preference]", chromedp.ByQuery),
	)
	choose("system")

	var wrappedLabels []string
	check(
		chromedp.Poll(`localStorage.getItem('wake.theme')==='system'`, nil),
		testbrowser.EmulateSystemTheme("dark"),
		testbrowser.EmulateSystemTheme("light"),
		// Headless browser history may reload instead of restoring a cached page.
		chromedp.Evaluate(
			`localStorage.setItem('wake.theme','dark'); window.dispatchEvent(new PageTransitionEvent('pageshow',{persisted:true}))`,
			nil,
		),
		chromedp.Poll(
			`document.documentElement.dataset.theme==='wake-dark' && document.querySelector('[data-theme-preference]')?.value==='dark'`,
			nil,
		),
		chromedp.EmulateViewport(320, 800),
		chromedp.Evaluate(`document.documentElement.style.fontSize='200%'`, nil),
		chromedp.Evaluate(`(()=>{
			const range=document.createRange();
			return [...document.querySelectorAll('nav[aria-label="System views"] a,.wake-appearance h2')].filter(label=>{
				range.selectNodeContents(label);
				return range.getClientRects().length!==1;
			}).map(label=>label.textContent);
		})()`, &wrappedLabels),
	)

	if len(wrappedLabels) != 0 {
		t.Fatal("labels wrap at 200 percent text", wrappedLabels)
	}
}
