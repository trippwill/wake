// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

//go:build browser

package admin

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"go.trippwill.dev/wake/internal/testbrowser"
)

func TestBrowserClientEnrollmentAndRevocation(t *testing.T) {
	front, api, _ := newFrontedServer(t, false)
	ctx := testbrowser.Start(t)

	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()

	check := func(actions ...chromedp.Action[chromedp.Void]) {
		t.Helper()

		if err := chromedp.Do(ctx, actions...); err != nil {
			t.Fatal(err)
		}
	}
	check(
		testbrowser.SetViewport(1280, 1000),
		testbrowser.NewDocument(chromedp.Navigate(front.URL+"/system/clients")),
		chromedp.WaitVisible(chromedp.CSS("#unlock")),
		chromedp.SendKeys(chromedp.CSS("#unlock"), frontedUnlock),
		testbrowser.NewDocument(chromedp.Click(chromedp.CSS(`button[type="submit"]`))),
		testbrowser.NewDocument(chromedp.Navigate(front.URL+"/system/clients")),
		chromedp.WaitVisible(chromedp.CSS("#client-name")),
		chromedp.SendKeys(chromedp.CSS("#client-name"), "Laptop dashboard"),
		testbrowser.NewDocument(chromedp.Click(chromedp.CSS(`form[action="/system/clients"] button`))),
		chromedp.WaitVisible(chromedp.CSS("#client-token")),
	)

	var token string
	check(testbrowser.Evaluate(`document.querySelector('#client-token').value`, &token))

	if token == "" {
		t.Fatal("issued token missing")
	}

	if folder := os.Getenv("WAKE_SCREENSHOT_DIR"); folder != "" {
		var png []byte
		check(testbrowser.Capture(&png, chromedp.FullScreenshot(90)))

		//nolint:gosec // Caller-selected directory for synthetic test screenshots.
		if err := os.MkdirAll(folder, 0o700); err != nil {
			t.Fatal(err)
		}

		//nolint:gosec // Caller-selected directory and fixed screenshot name.
		if err := os.WriteFile(filepath.Join(folder, "api-clients.png"), png, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	request := func() int {
		t.Helper()

		r, err := http.NewRequestWithContext(ctx, http.MethodGet, api.URL+"/v1/snapshot", nil)
		if err != nil {
			t.Fatal(err)
		}

		r.Header.Set("Authorization", "Bearer "+token)

		res, err := api.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}

		_ = res.Body.Close()

		return res.StatusCode
	}
	if status := request(); status != http.StatusOK {
		t.Fatal("enrollment did not grant reads", status)
	}

	check(
		testbrowser.NewDocument(chromedp.Navigate(front.URL+"/system/clients")),
		chromedp.WaitVisible(chromedp.CSS("#client-name")),
	)

	var hidden bool
	check(testbrowser.Evaluate(`!document.querySelector('#client-token')`, &hidden))

	if !hidden {
		t.Fatal("token redisplayed")
	}

	check(
		testbrowser.NewDocument(
			chromedp.Click(chromedp.CSS(`button[aria-label="Revoke access for Laptop dashboard"]`)),
		),
		chromedp.WaitVisible(chromedp.CSS("#client-name")),
	)

	if status := request(); status != http.StatusUnauthorized {
		t.Fatal("revoked token remained authorized", status)
	}

	check(testbrowser.SetViewport(390, 844))

	var fits bool
	check(testbrowser.Evaluate(`document.documentElement.scrollWidth <= window.innerWidth`, &fits))

	if !fits {
		t.Fatal("client page overflows mobile viewport")
	}
}
