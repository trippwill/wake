// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

//go:build browser

package admin

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
	"go.trippwill.dev/wake/internal/testbrowser"
)

func TestBrowserResponsiveGridAndNavigation(t *testing.T) {
	f := newAdminBrowserFixture(t)
	check := f.check
	assert := f.assert
	origin := f.origin

	// Resize the same populated document across the former width and height breakpoints.
	for _, theme := range []string{"light", "dark"} {
		check(chromedp.Evaluate[chromedp.Void](`localStorage.setItem('wake.theme',` + strconv.Quote(theme) + `)`))

		const size = 50

		check(
			testbrowser.SetViewport(1280, 720),
		)
		f.visitGrid("/test/dense-grid?size="+strconv.Itoa(size), size, min(size, 70))
		check(chromedp.Evaluate[chromedp.Void](`window.resizedGrid=document.querySelector('[data-grid]')`))

		for index, viewport := range [][2]int64{{1280, 720}, {768, 640}, {768, 639}, {320, 700}, {1280, 720}} {
			width, height := viewport[0], viewport[1]
			check(
				testbrowser.SetViewport(width, height),
				testbrowser.SettleAnimations(),
				chromedp.Poll[chromedp.Void](fmt.Sprintf(`(()=>{
					const holder=document.querySelector('.tabulator-tableholder');
					const grid=document.querySelector('[data-grid]').getBoundingClientRect();
					const footer=document.querySelector('.tabulator-footer').getBoundingClientRect();
					return innerWidth===%d && innerHeight===%d && holder.clientHeight>100 && holder.scrollHeight>holder.clientHeight && footer.bottom<=grid.bottom+1;
				})()`, width, height)),
				chromedp.Evaluate[chromedp.Void](
					`document.querySelector('[data-grid]').scrollIntoView({block:'start'})`,
				),
			)
			assert(`(()=>{
				const grid=document.querySelector('[data-grid]');
				const holder=grid.querySelector('.tabulator-tableholder');
				const chrome=[...grid.querySelectorAll('[data-list-filters],.tabulator-header,.tabulator-footer')];
				const positions=chrome.map(n=>n.getBoundingClientRect().top);
				holder.scrollTop=holder.scrollHeight;
				const last=holder.querySelector('.tabulator-row:last-child').getBoundingClientRect();
				const bounds=holder.getBoundingClientRect();
				const reachable=grid===window.resizedGrid && holder.scrollTop>0 && last.top>=bounds.top && last.bottom<=bounds.bottom+1 && chrome.every((n,i)=>Math.abs(n.getBoundingClientRect().top-positions[i])<1) && document.documentElement.scrollWidth<=innerWidth+1;
				holder.scrollTop=0;
				return reachable;
			})()`, fmt.Sprintf("resizing clipped rows or moved fixed controls %s %d %dx%d", theme, size, width, height))

			if height == 640 || height == 720 {
				minimumRows := 4
				if height == 720 {
					minimumRows = 5
				}

				assert(fmt.Sprintf(`(()=>{
					const holder=document.querySelector('.tabulator-tableholder');
					const bounds=holder.getBoundingClientRect();
					const ordinary=holder.querySelectorAll('.tabulator-row')[10];
					holder.scrollTop+=ordinary.getBoundingClientRect().top-bounds.top;
					return [...holder.querySelectorAll('.tabulator-row')].filter(row=>{
						const r=row.getBoundingClientRect();
						return r.top>=bounds.top && r.bottom<=Math.min(innerHeight,bounds.bottom);
					}).length>=%d;
				})()`, minimumRows), fmt.Sprintf("fixed header crowds out ordinary wrapped rows %s %dx%d", theme, width, height))
			}

			if index == 0 || width == 320 {
				f.screenshot(fmt.Sprintf("grid-resize-%s-%dx%d", theme, width, height))
			}
		}

		check(
			chromedp.Focus(chromedp.CSS(".wake-grid-help summary")),
			chromedp.SendKeys(chromedp.CSS(".wake-grid-help summary"), "\r"),
		)
		assert(
			`document.querySelector('.wake-grid-help').open && document.querySelector('.wake-grid-help').textContent.includes('Alt + arrow keys') && document.querySelector('.tabulator-tableholder').clientHeight>100`,
			"table keyboard help is unavailable or crowds out the rows",
		)
		check(chromedp.SendKeys(chromedp.CSS(".wake-grid-help summary"), "\r"))
		assert(
			`!document.querySelector('.wake-grid-help').open && document.activeElement.matches('.wake-grid-help summary')`,
			"closing table help lost focus",
		)
	}

	for _, page := range []struct {
		name, path, theme string
		width             int64
	}{
		{"comparison-narrow", "/test/correction?mode=evidence&candidate=candidate-one", "light", 320},
		{"report-desktop", "/evidence/synthetic-report", "dark", 1440},
		{"settings-narrow", "/system/settings", "dark", 390},
	} {
		check(
			chromedp.Evaluate[chromedp.Void](`localStorage.setItem('wake.theme',`+strconv.Quote(page.theme)+`)`),
			testbrowser.SetViewport(page.width, 1000),
			testbrowser.NewDocument(chromedp.Navigate(origin+page.path)),
			chromedp.WaitVisible(chromedp.CSS("h1")),
			chromedp.Poll[chromedp.Void](`document.documentElement.dataset.wakeReady==='true'`),
		)

		if page.name == "report-desktop" {
			check(chromedp.Poll[chromedp.Void](`document.querySelector('[data-grid]').dataset.gridReady==='true'`))
		}

		assert(`document.documentElement.scrollWidth<=innerWidth+1`, "page overflow: "+page.name)
		f.screenshot(page.name)
	}

	check(
		testbrowser.SetViewport(1280, 420),
		testbrowser.NewDocument(chromedp.Navigate(origin+"/system/settings")),
		chromedp.WaitVisible(chromedp.CSS(".wake-sidebar .wake-contact")),
	)
	assert(`(()=>{
		const nav=document.querySelector('#main-navigation');
		const contact=document.querySelector('.wake-contact');
		const before=contact.getBoundingClientRect();
		nav.scrollTop=nav.scrollHeight;
		const after=contact.getBoundingClientRect();
		return nav.scrollTop>0 && before.top===after.top && after.bottom<=innerHeight && innerHeight-after.bottom<40;
	})()`, "status moved when scrolling short desktop navigation")
	check(chromedp.Focus(chromedp.CSS(".wake-contact-trigger")))
	assert(`!document.querySelector('.wake-contact-details').open`, "focus opened contact details")
	check(chromedp.KeyEvent(kb.Enter))
	assert(`(()=>{
		const details=document.querySelector('.wake-contact-details');
		const bounds=details.querySelector('.wake-contact-panel').getBoundingClientRect();
		const sidebar=details.closest('.wake-sidebar').getBoundingClientRect();
		return details.open && bounds.height>0 && bounds.top>=sidebar.top && bounds.right<=sidebar.right &&
			bounds.bottom<=details.querySelector('summary').getBoundingClientRect().top;
	})()`, "desktop contact details clipped or below the trigger")
	check(chromedp.KeyEvent(kb.Escape))
	assert(
		`!document.querySelector('.wake-contact-details').open && document.activeElement.matches('.wake-contact-trigger')`,
		"Escape did not dismiss contact details and retain focus",
	)
	check(chromedp.Click(chromedp.CSS(".wake-contact-trigger")))
	assert(`document.querySelector('.wake-contact-details').open`, "click did not open contact details")
	check(chromedp.Click(chromedp.CSS(".wake-contact-trigger")))
	assert(
		`!document.querySelector('.wake-contact-details').open`,
		"second click did not close contact details",
	)

	check(
		testbrowser.SetViewport(320, 800),
		testbrowser.NewDocument(chromedp.Navigate(origin+"/test/correction")),
		chromedp.WaitVisible(chromedp.CSS("h1")),
	)
	assert(
		`document.querySelector('#main-navigation').hidden && document.querySelector('.wake-sidebar > .wake-contact').getBoundingClientRect().height>0`,
		"narrow navigation must collapse independently of status",
	)
	check(
		chromedp.Click(chromedp.CSS(".wake-menu-toggle")),
		chromedp.Evaluate[chromedp.Void](
			`document.dispatchEvent(new KeyboardEvent('keydown',{key:'Escape',bubbles:true}))`,
		),
	)
	assert(
		`document.querySelector('#main-navigation').hidden && document.activeElement.matches('.wake-menu-toggle')`,
		"Escape focus return",
	)

	var contactPoint input.TouchPoint
	check(
		testbrowser.Command(
			emulation.SetTouchEmulationEnabled,
			emulation.SetTouchEmulationEnabledParams{Enabled: true},
		),
		testbrowser.Evaluate(`(()=>{
			const bounds=document.querySelector('.wake-contact-trigger').getBoundingClientRect();
			return {x:bounds.left+bounds.width/2, y:bounds.top+bounds.height/2};
		})()`, &contactPoint),
		testbrowser.Command(
			input.DispatchTouchEvent,
			input.DispatchTouchEventParams{
				Type:        input.DispatchTouchEventTypeTouchStart,
				TouchPoints: []*input.TouchPoint{&contactPoint},
			},
		),
		testbrowser.Command(
			input.DispatchTouchEvent,
			input.DispatchTouchEventParams{
				Type:        input.DispatchTouchEventTypeTouchEnd,
				TouchPoints: []*input.TouchPoint{},
			},
		),
		chromedp.Poll[chromedp.Void](`document.querySelector('.wake-contact-details').open`),
		testbrowser.Command(
			emulation.SetTouchEmulationEnabled,
			emulation.SetTouchEmulationEnabledParams{Enabled: false},
		),
	)
	assert(`(()=>{
		const bounds=document.querySelector('.wake-contact-panel').getBoundingClientRect();
		return bounds.height>0 && bounds.left>=0 && bounds.right<=innerWidth &&
			bounds.top>=document.querySelector('.wake-contact-trigger').getBoundingClientRect().bottom;
	})()`, "narrow contact details clipped or above the trigger")
	// Text resizing stresses long exact values and native controls independently of display density.
	for _, path := range []string{"/test/correction?field=price", "/test/correction?mode=evidence&candidate=candidate-one", "/test/grid", "/system/settings"} {
		check(
			testbrowser.NewDocument(chromedp.Navigate(origin+path)),
			chromedp.WaitVisible(chromedp.CSS("h1")),
			chromedp.Evaluate[chromedp.Void](`document.documentElement.style.fontSize='200%'`),
			chromedp.Evaluate[chromedp.Void](
				`new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve)))`,
				func(p *runtime.EvaluateParams) { p.AwaitPromise = new(true) },
			),
		)
		assert(`document.documentElement.scrollWidth<=innerWidth+1`, "200 percent text overflow: "+path)

		if strings.Contains(path, "mode=evidence") {
			assert(
				`document.querySelector('.wake-comparison caption').getBoundingClientRect().width>100`,
				"200 percent comparison caption",
			)
		}
	}
}
