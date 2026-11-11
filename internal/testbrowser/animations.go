// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

//go:build browser

package testbrowser

import (
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

// SettleAnimations waits for live finite transitions and their final paint.
// Polling avoids retaining promises after animation targets disappear.
// Paused and scroll-driven animations need input, not elapsed time, to finish.
func SettleAnimations() chromedp.Action[chromedp.Void] {
	return chromedp.Evaluate[chromedp.Void](
		`(async()=>{
 const paint=()=>new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve)));
 const active=()=>document.getAnimations().some(a=>a.timeline===document.timeline&&a.playState==='running'&&!a.pending&&Number.isFinite(a.effect?.getComputedTiming().endTime));
 globalThis.wakeCapturePhase='initial paint';
 await paint();
 globalThis.wakeCapturePhase='transitions';
 while(active())await paint();
 globalThis.wakeCapturePhase='final paint';
 await paint();
 globalThis.wakeCapturePhase='settled';
})()`,
		func(p *runtime.EvaluateParams) { p.AwaitPromise = new(true) },
	)
}
