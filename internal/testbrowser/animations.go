// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

//go:build browser

package testbrowser

import (
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

// SettleAnimations waits for paint and active finite transitions before a capture.
// Paused and scroll-driven animations need input, not elapsed time, to finish.
func SettleAnimations() chromedp.Action {
	return chromedp.Evaluate(
		`new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve))).then(()=>Promise.all(document.getAnimations().filter(a=>a.timeline===document.timeline&&a.playState==='running'&&!a.pending&&Number.isFinite(a.effect?.getComputedTiming().endTime)).map(a=>a.finished.catch(()=>{}))))`,
		nil,
		func(p *runtime.EvaluateParams) *runtime.EvaluateParams { return p.WithAwaitPromise(true) },
	)
}
