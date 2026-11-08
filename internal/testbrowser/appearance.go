// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

//go:build browser

package testbrowser

import (
	"strconv"

	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/chromedp"
)

// EmulateSystemTheme waits for Wake's System preference to follow the emulated appearance.
func EmulateSystemTheme(theme string) chromedp.Action {
	return chromedp.Tasks{
		emulation.SetEmulatedMedia().
			WithFeatures([]*emulation.MediaFeature{{Name: "prefers-color-scheme", Value: theme}}),
		chromedp.Poll(`document.documentElement.dataset.theme===`+strconv.Quote("wake-"+theme), nil),
	}
}
