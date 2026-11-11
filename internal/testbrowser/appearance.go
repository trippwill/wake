// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

//go:build browser

package testbrowser

import (
	"strconv"

	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/chromedp"
)

// SetViewport changes the device metrics without changing touch emulation.
func SetViewport(width, height int64) chromedp.Action[chromedp.Void] {
	return Command(emulation.SetDeviceMetricsOverride, emulation.SetDeviceMetricsOverrideParams{
		Width:             width,
		Height:            height,
		DeviceScaleFactor: 1,
	})
}

// EmulateSystemTheme waits for Wake's System preference to follow the emulated appearance.
func EmulateSystemTheme(theme string) chromedp.Action[chromedp.Void] {
	return chromedp.Steps(
		Command(emulation.SetEmulatedMedia, emulation.SetEmulatedMediaParams{
			Features: []*emulation.MediaFeature{{Name: "prefers-color-scheme", Value: theme}},
		}),
		chromedp.Poll[chromedp.Void](`document.documentElement.dataset.theme===`+strconv.Quote("wake-"+theme)),
	)
}
