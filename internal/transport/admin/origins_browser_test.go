// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

//go:build browser

package admin

import (
	"strconv"
	"testing"

	"github.com/chromedp/chromedp"
	"go.trippwill.dev/wake/internal/config"
	"go.trippwill.dev/wake/internal/testbrowser"
)

func TestBrowserOriginMatchesChromium(t *testing.T) {
	ctx := testbrowser.Start(t)
	for _, raw := range []string{
		"http://127.0.0.1:8787",
		"HTTP://LOCALHOST:18787/",
		"http://localhost:28787",
		"http://localhost:080",
		"https://Wake.Example:443/",
		"https://wake.example.:08443",
		"http://127.0.0.1.:18787/",
		"http://[0:0:0:0:0:0:0:1]:18787",
		"http://[::ffff:127.0.0.1]:18787",
		"http://[::ffff:7f00:1]:18787",
	} {
		got, err := config.BrowserOrigin(raw)
		if err != nil {
			t.Errorf("Wake rejected %q: %v", raw, err)
			continue
		}

		var want string
		if err := chromedp.Do(
			ctx,
			testbrowser.Evaluate("new URL("+strconv.Quote(raw)+").origin", &want),
		); err != nil {
			t.Fatalf("Chromium origin for %q: %v", raw, err)
		}

		if got != want {
			t.Errorf("origin for %q: Wake %q, Chromium %q", raw, got, want)
		}
	}
}
