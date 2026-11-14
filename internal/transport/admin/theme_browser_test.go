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

	dir := privateAuthDir(t)
	if err := SetUnlock(dir, []byte("synthetic-theme-unlock")); err != nil {
		t.Fatal(err)
	}

	auth, _, err := Open(dir, origin)
	if err != nil {
		t.Fatal(err)
	}

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
	if err := chromedp.Listen(ctx, func(event any) {
		if response, ok := event.(*network.EventResponseReceived); ok &&
			strings.Contains(response.Response.URL, "/assets/") &&
			response.Response.Headers["Content-Encoding"] == "gzip" {
			gzipAssets.Add(1)
		}
	}); err != nil {
		t.Fatal(err)
	}

	session := httptest.NewRecorder()
	issueTestSession(t, auth, session)
	cookie := session.Result().Cookies()[0]
	check := func(actions ...chromedp.Action[chromedp.Void]) {
		t.Helper()

		if err := chromedp.Do(ctx, actions...); err != nil {
			t.Fatal(err)
		}
	}
	check(
		testbrowser.Command(network.Enable, network.EnableParams{}),
		testbrowser.Command(
			network.SetCookie,
			network.SetCookieParams{Name: cookie.Name, Value: cookie.Value, URL: origin, HTTPOnly: new(true)},
		),
		testbrowser.NewDocument(chromedp.Navigate(origin+"/system")),
		chromedp.Poll[chromedp.Void](`!document.querySelector('.wake-toolbar,[data-theme-preference]')`),
		testbrowser.NewDocument(
			chromedp.Click(chromedp.CSS(`nav[aria-label="System views"] a[href="/system/settings"]`)),
		),
		chromedp.WaitVisible(chromedp.CSS("[data-theme-preference]")),
		chromedp.Poll[chromedp.Void](
			`document.querySelector('nav[aria-label="System views"] [aria-current="page"]')?.textContent==='Settings'`,
		),
	)

	if gzipAssets.Load() == 0 {
		t.Fatal("browser did not receive any compressed assets")
	}

	choose := func(preference string) {
		t.Helper()
		check(
			chromedp.SetValue(chromedp.CSS("[data-theme-preference]"), preference),
			chromedp.Evaluate[chromedp.Void](
				`document.querySelector('[data-theme-preference]').dispatchEvent(new Event('change',{bubbles:true}))`,
			),
		)
	}
	for _, preference := range []string{"light", "dark"} {
		ready := `document.documentElement.dataset.theme===` + strconv.Quote("wake-"+preference) +
			` && document.querySelector('[data-theme-preference]').value===` + strconv.Quote(preference) +
			` && localStorage.getItem('wake.theme')===` + strconv.Quote(preference)
		choose(preference)
		check(
			chromedp.Poll[chromedp.Void](ready),
			testbrowser.NewDocument(chromedp.Reload()),
			chromedp.WaitVisible(chromedp.CSS("h1")),
			chromedp.Poll[chromedp.Void](ready),
		)
	}

	check(
		chromedp.NavigateBack(),
		chromedp.Poll[chromedp.Void](
			`location.pathname==='/system' && document.documentElement.dataset.theme==='wake-dark'`,
		),
		chromedp.NavigateForward(),
		chromedp.Poll[chromedp.Void](
			`location.pathname==='/system/settings' && document.querySelector('[data-theme-preference]')?.value==='dark'`,
		),
		testbrowser.NewDocument(chromedp.Navigate(origin+"/login")),
		chromedp.WaitVisible(chromedp.CSS("#unlock")),
		chromedp.Poll[chromedp.Void](
			`document.documentElement.dataset.theme==='wake-dark' && !document.querySelector('.wake-toolbar,[data-theme-preference],#daemon-contact')`,
		),
		chromedp.NavigateBack(),
		chromedp.WaitVisible(chromedp.CSS("[data-theme-preference]")),
	)
	choose("system")

	var wrappedLabels []string
	check(
		chromedp.Poll[chromedp.Void](`localStorage.getItem('wake.theme')==='system'`),
		testbrowser.EmulateSystemTheme("dark"),
		testbrowser.EmulateSystemTheme("light"),
		// Headless browser history may reload instead of restoring a cached page.
		chromedp.Evaluate[chromedp.Void](
			`localStorage.setItem('wake.theme','dark'); window.dispatchEvent(new PageTransitionEvent('pageshow',{persisted:true}))`,
		),
		chromedp.Poll[chromedp.Void](
			`document.documentElement.dataset.theme==='wake-dark' && document.querySelector('[data-theme-preference]')?.value==='dark'`,
		),
		chromedp.EmulateViewport(320, 800),
		chromedp.Evaluate[chromedp.Void](`document.documentElement.style.fontSize='200%'`),
		testbrowser.Evaluate(`(()=>{
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
