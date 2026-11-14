// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

//go:build browser

package admin

import (
	"context"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
	"go.trippwill.dev/wake/internal/testbrowser"
)

func TestBrowserPasswordSignInAndSignOut(t *testing.T) {
	dir := privateAuthDir(t)
	server := httptest.NewUnstartedServer(nil)

	auth, _, err := Open(dir, "http://"+server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}

	var clockOffset atomic.Int64

	auth.now = func() time.Time { return time.Now().Add(time.Duration(clockOffset.Load())) }

	server.Config.Handler, err = NewHandler(auth, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}

	server.Start()
	defer server.Close()

	ctx, cancel := context.WithTimeout(testbrowser.Start(t), 45*time.Second)
	defer cancel()

	check := func(actions ...chromedp.Action[chromedp.Void]) {
		t.Helper()

		if err := chromedp.Do(ctx, actions...); err != nil {
			t.Fatal(err)
		}
	}

	const password = "synthetic operator password"

	check(
		testbrowser.SetViewport(390, 844),
		testbrowser.NewDocument(chromedp.Navigate(server.URL)),
		chromedp.Poll[chromedp.Void](
			`document.querySelector("main pre").textContent === "wake access set password"`,
		),
	)

	var fits bool
	check(testbrowser.Evaluate(`document.documentElement.scrollWidth <= window.innerWidth`, &fits))

	if !fits {
		t.Fatal("password setup screen overflows mobile viewport")
	}

	if err := SetPassword(dir, []byte(password)); err != nil {
		t.Fatal(err)
	}

	check(
		testbrowser.SetViewport(1280, 900),
		testbrowser.NewDocument(chromedp.Click(chromedp.CSS(`main a[href="/login"]`))),
		chromedp.SendKeys(chromedp.CSS("#password"), password),
		testbrowser.NewDocument(chromedp.Click(chromedp.CSS(`form[action="/login"] button`))),
		chromedp.WaitVisible(chromedp.CSS("main h1")),
		chromedp.Focus(chromedp.CSS(`form[action="/logout"] button`)),
		testbrowser.NewDocument(chromedp.KeyEvent(kb.Enter)),
		chromedp.WaitVisible(chromedp.CSS(`input[autocomplete="current-password"]`)),
	)
	clockOffset.Add(int64(attemptInterval))
	check(
		chromedp.SendKeys(chromedp.CSS("#password"), password),
		testbrowser.NewDocument(chromedp.Click(chromedp.CSS(`form[action="/login"] button`))),
		chromedp.WaitVisible(chromedp.CSS(`form[action="/logout"] button`)),
		testbrowser.SetViewport(320, 800),
		chromedp.WaitVisible(chromedp.CSS(".wake-menu-toggle")),
		chromedp.Poll[chromedp.Void](`document.querySelector("#main-navigation").hidden`),
		chromedp.WaitVisible(chromedp.CSS(`form[action="/logout"] button`)),
		testbrowser.NewDocument(chromedp.Click(chromedp.CSS(`form[action="/logout"] button`))),
		chromedp.WaitVisible(chromedp.CSS(`input[autocomplete="current-password"]`)),
	)
}
