// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

//go:build browser

package testbrowser

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// Start launches an isolated Chromium session before any action deadlines begin.
func Start(t *testing.T) context.Context {
	t.Helper()

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

	allocator, stop := chromedp.NewExecAllocator(
		t.Context(),
		append(chromedp.DefaultExecAllocatorOptions[:], chromedp.ExecPath(browser))...,
	)
	t.Cleanup(stop)

	ctx, cancel := chromedp.NewContext(allocator)
	t.Cleanup(cancel)

	// Cancel only stalled startup. A short-lived child context would own and
	// terminate the browser process when its deadline ends.
	startup := time.AfterFunc(30*time.Second, cancel)
	defer startup.Stop()

	if err := chromedp.Do(ctx); err != nil {
		t.Fatalf("start Chromium (%s): %v", browser, err)
	}

	return ctx
}
