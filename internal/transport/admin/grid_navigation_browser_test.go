// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

//go:build browser

package admin

import (
	"context"
	"fmt"
	"strconv"
	"testing"

	cdppage "github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
	"go.trippwill.dev/wake/internal/testbrowser"
)

func TestBrowserGridNavigationAndPreferences(t *testing.T) {
	f := newAdminBrowserFixture(t)
	check := f.check
	assert := f.assert
	origin := f.origin

	check(
		chromedp.Evaluate[chromedp.Void](
			`localStorage.setItem('tabulator-wake-activity-columns','invalid JSON')`,
		),
		testbrowser.NewDocument(chromedp.Navigate(origin+"/test/grid")),
		chromedp.WaitVisible(chromedp.CSS(".tabulator")),
		chromedp.Poll[chromedp.Void](`document.querySelector('.tabulator-row')!==null`),
	)
	assert(
		`!document.querySelector('table') && !window.privateInjection && document.querySelector('[data-grid-status]').textContent==='70 of 70 results'`,
		"JSON grid or escaping",
	)

	chooseSize := func(size, rows int) {
		t.Helper()
		check(
			chromedp.Focus(chromedp.CSS(".tabulator-page-size")),
			chromedp.SetValue(chromedp.CSS(".tabulator-page-size"), strconv.Itoa(size)),
			chromedp.Evaluate[chromedp.Void](
				`document.querySelector('.tabulator-page-size').dispatchEvent(new Event('change',{bubbles:true}))`,
			),
			chromedp.Poll[chromedp.Void](fmt.Sprintf(
				`!document.querySelector('[data-grid]').hasAttribute('aria-busy') && document.querySelector('[data-grid]').dataset.gridReady==='true' && document.querySelectorAll('.tabulator-row').length===%d && document.querySelector('.tabulator-page-size').value==='%d'`,
				rows,
				size,
			)),
		)
		assert(`document.activeElement.matches('.tabulator-page-size')`, "page-size change lost keyboard focus")
	}

	// Exercise the native selector, then carry its size through filtering and history.
	f.visitGrid("/test/grid?size=50&q=investment&sort=price&dir=asc&page=2&selected=precision-025", 50, 20)
	assert(
		`(()=>{const s=document.querySelector('.tabulator-page-size');return s.getAttribute('aria-label')==='Rows per page' && s.labels[0]?.textContent==='Rows per page' && [...s.options].map(o=>o.value).join(',')==='10,25,50,100,200'})()`,
		"page-size selector choices or label",
	)
	check(
		chromedp.Focus(chromedp.CSS(".tabulator-page-size")),
		chromedp.SendKeys(chromedp.CSS(".tabulator-page-size"), kb.ArrowUp),
		chromedp.Poll[chromedp.Void](
			`document.querySelectorAll('.tabulator-row').length===25 && new URL(location.href).searchParams.get('size')==='25' && !document.querySelector('[data-grid]').hasAttribute('aria-busy')`,
		),
	)
	assert(
		`(()=>{const q=new URL(location.href).searchParams;return q.get('page')==='1' && q.get('q')==='investment' && q.get('sort')==='price' && q.get('dir')==='asc' && !q.has('selected') && document.activeElement.matches('.tabulator-page-size')})()`,
		"keyboard page-size change lost query state, focus, or retained the old page/selection",
	)
	check(
		chromedp.Evaluate[chromedp.Void](`window.Tabulator.findTable('.tabulator')[0].setPage(2)`),
		chromedp.Poll[chromedp.Void](
			`new URL(location.href).searchParams.get('page')==='2' && !document.querySelector('[data-grid]').hasAttribute('aria-busy')`,
		),
		chromedp.Evaluate[chromedp.Void](
			`(()=>{const url=new URL(location.href);url.searchParams.set('selected','precision-025');history.replaceState(history.state,'',url);document.querySelector('[data-list-filters]').requestSubmit()})()`,
		),
		chromedp.Poll[chromedp.Void](
			`new URL(location.href).searchParams.get('page')==='1' && !document.querySelector('[data-grid]').hasAttribute('aria-busy')`,
		),
	)
	chooseSize(100, 70)
	check(
		chromedp.Evaluate[chromedp.Void](`history.back()`),
		chromedp.Poll[chromedp.Void](
			`new URL(location.href).searchParams.get('page')==='2' && new URL(location.href).searchParams.get('size')==='25' && document.querySelector('.tabulator-page-size').value==='25' && document.querySelectorAll('.tabulator-row').length===25 && !document.querySelector('[data-grid]').hasAttribute('aria-busy')`,
		),
	)
	assert(
		`new URL(location.href).searchParams.get('selected')==='precision-025'`,
		"history size restoration cleared its selected record",
	)
	check(
		chromedp.Evaluate[chromedp.Void](`history.forward()`),
		chromedp.Poll[chromedp.Void](
			`new URL(location.href).searchParams.get('size')==='100' && new URL(location.href).searchParams.get('page')==='1' && document.querySelectorAll('.tabulator-row').length===70 && !document.querySelector('[data-grid]').hasAttribute('aria-busy')`,
		),
	)
	chooseSize(200, 70)
	assert(
		`new URL(location.href).searchParams.get('size')==='200'`,
		"selected size did not reach the owning URL",
	)
	chooseSize(25, 25)
	check(chromedp.Click(chromedp.CSS("[data-grid-reset]")))
	f.visitGrid("/test/grid", 25, 25)
	assert(
		`Object.keys(JSON.parse(localStorage.getItem('tabulator-wake-activity-page'))).join(',')==='paginationSize'`,
		"page-size persistence also saved the page number",
	)

	// Each table type keeps its own page-size preference.
	f.visitGrid("/instruments/synthetic-instrument", 50, 2)
	chooseSize(10, 2)
	assert(
		`new URL(location.href).searchParams.get('size')==='10'`,
		"observation page size missing from URL",
	)
	assert(
		`(()=>{const link=new URL(document.querySelector('.tabulator-row a').href);return link.pathname.startsWith('/evidence/') && !link.search && new URL(location.href).searchParams.get('size')==='10'})()`,
		"evidence return link lost the selected observation page size",
	)
	f.visitGrid("/test/instruments", 50, 50)
	chooseSize(100, 70)
	f.visitGrid("/test/grid", 25, 25)
	f.visitGrid("/instruments/synthetic-instrument", 10, 2)
	f.visitGrid("/test/instruments", 100, 70)
	f.visitGrid("/test/grid?size=200", 200, 70)

	for _, saved := range []string{"invalid JSON", `{"paginationSize":201}`, `{"paginationSize":[25]}`} {
		check(
			chromedp.Evaluate[chromedp.Void](
				`localStorage.setItem('tabulator-wake-activity-page',` + strconv.Quote(saved) + `)`,
			),
		)
		f.visitGrid("/test/grid?size=invalid", 50, 50)
	}

	check(
		chromedp.Evaluate[chromedp.Void](
			`localStorage.setItem('tabulator-wake-activity-page','{"paginationSize":25,"paginationInitialPage":2}')`,
		),
	)
	f.visitGrid("/test/grid?size=201", 25, 25)
	assert(`new URL(location.href).searchParams.get('page')==='1'`, "saved preference restored a page number")

	// Unsupported URL sizes must reach browser fallback through the production HTML route.
	for _, kind := range []string{"activity", "instruments"} {
		check(
			chromedp.Evaluate[chromedp.Void](
				`localStorage.setItem('tabulator-wake-` + kind + `-page','{"paginationSize":25}')`,
			),
		)

		for _, size := range []string{"invalid", "201", "20"} {
			f.visitGrid("/"+kind+"?size="+size, 25, 0)
		}

		check(chromedp.Evaluate[chromedp.Void](`localStorage.removeItem('tabulator-wake-` + kind + `-page')`))
		f.visitGrid("/"+kind+"?size=invalid", 50, 0)
	}

	// Block storage before initialization as well as while changing the control.
	var blockedStorage cdppage.ScriptIdentifier

	check(chromedp.Func(func(ctx context.Context, _ *chromedp.Target) error {
		result, err := chromedp.Call(
			ctx,
			cdppage.AddScriptToEvaluateOnNewDocument,
			cdppage.AddScriptToEvaluateOnNewDocumentParams{Source: `
			for(const method of ['getItem','setItem']) {
				const original=Storage.prototype[method];
				Storage.prototype[method]=function(key,...args) {
					if(key.startsWith('tabulator-')) throw new DOMException('Blocked','SecurityError');
					return original.call(this,key,...args);
				};
			}
		`},
		)

		blockedStorage = result.Identifier

		return err
	}))
	f.visitGrid("/test/grid", 50, 50)
	chooseSize(100, 70)

	// Reset navigation retains the live size even when no browser preference can be saved.
	for _, path := range []string{"/activity?kind=trade&state=accepted", "/instruments?kind=equity&state=resolved"} {
		f.visitGrid(path+"&q=investment&size=50&page=2&selected=one", 50, 0)
		chooseSize(25, 0)
		check(
			testbrowser.NewDocument(chromedp.Click(chromedp.CSS("[data-grid-reset-filters]"))),
			chromedp.Poll[chromedp.Void](
				`document.querySelector('[data-grid]')?.dataset.gridReady==='true' && document.querySelector('.tabulator-page-size')?.value==='25'`,
			),
		)
		assert(
			`(()=>{const q=new URL(location.href).searchParams;return ['/activity','/instruments'].includes(location.pathname) && q.get('size')==='25' && q.get('page')==='1' && ['q','kind','state','selected'].every(key=>!q.has(key)) && (document.querySelector('[data-grid]').dataset.grid==='instruments')===(location.pathname==='/instruments')})()`,
			"resetting filters lost page size or retained filters/selection with blocked storage",
		)
	}

	check(
		testbrowser.Command(
			cdppage.RemoveScriptToEvaluateOnNewDocument,
			cdppage.RemoveScriptToEvaluateOnNewDocumentParams{Identifier: blockedStorage},
		),
	)
	f.visitGrid("/test/grid?size=100", 100, 70)

	f.refreshFailed.Store(true)
	check(
		chromedp.SetValue(chromedp.CSS(".tabulator-page-size"), "25"),
		chromedp.Evaluate[chromedp.Void](
			`document.querySelector('.tabulator-page-size').dispatchEvent(new Event('change',{bubbles:true}))`,
		),
		chromedp.WaitVisible(chromedp.CSS("[data-grid-retry]")),
	)
	assert(
		`document.querySelectorAll('.tabulator-row').length===70`,
		"failed size request discarded the displayed rows",
	)
	f.refreshFailed.Store(false)
	check(
		chromedp.Click(chromedp.CSS("[data-grid-retry]")),
		chromedp.Poll[chromedp.Void](
			`document.querySelectorAll('.tabulator-row').length===25 && new URL(location.href).searchParams.get('size')==='25' && !document.querySelector('[data-grid]').hasAttribute('aria-busy')`,
		),
	)
	f.emptyGrid.Store(true)
	chooseSize(10, 0)
	f.emptyGrid.Store(false)
	chooseSize(50, 50)
	check(
		chromedp.Evaluate[chromedp.Void](
			`for(const kind of ['activity','instruments','observations']) localStorage.removeItem('tabulator-wake-'+kind+'-page')`,
		),
	)

	check(
		testbrowser.NewDocument(chromedp.Navigate(origin+"/test/grid")),
		chromedp.WaitVisible(chromedp.CSS(".tabulator-row")),
	)
	check(
		chromedp.Evaluate[chromedp.Void](
			`window.proofGrid=window.Tabulator.findTable('.tabulator')[0];window.proofGrid.setSort('price','asc')`,
		),
		chromedp.Poll[chromedp.Void](
			`document.querySelector('.tabulator-row [tabulator-field=price]')?.textContent==='-1.0000000000000001 USD' && !document.querySelector('[data-grid]').hasAttribute('aria-busy')`,
		),
	)
	assert(
		`document.querySelectorAll('.tabulator-row').length===50`,
		"remote grid downloaded more than one bounded page",
	)
	check(
		chromedp.Evaluate[chromedp.Void](`window.proofGrid.setPage(2)`),
		chromedp.Poll[chromedp.Void](
			`new URL(location.href).searchParams.get('page')==='2' && document.querySelectorAll('.tabulator-row').length===20 && Array.from(document.querySelectorAll('.tabulator-row')).at(-1)?.querySelector('[tabulator-field=price]')?.textContent==='9007199254740993.0000000000001 USD'`,
		),
	)
	check(
		chromedp.Evaluate[chromedp.Void](
			`(()=>{const input=document.querySelector('#activity-search');input.value='Synthetic investment 69';input.form.requestSubmit()})()`,
		),
		chromedp.Poll[chromedp.Void](
			`document.querySelector('[data-grid-status]').textContent==='1 of 70 results' && document.querySelectorAll('.tabulator-row').length===1`,
		),
	)
	assert(`window.proofGrid.getPageMax()===1`, "server filtering did not update pagination")
	check(
		chromedp.Evaluate[chromedp.Void](
			`(()=>{const input=document.querySelector('#activity-search');input.value='investment';input.form.requestSubmit()})()`,
		),
		chromedp.Poll[chromedp.Void](
			`document.querySelector('[data-grid-status]').textContent==='70 of 70 results' && new URL(location.href).searchParams.get('page')==='1'`,
		),
	)
	// Same-document browser history restores the filter form, server query and page.
	check(
		chromedp.Evaluate[chromedp.Void](`history.back()`),
		chromedp.Poll[chromedp.Void](
			`new URL(location.href).searchParams.get('q')==='Synthetic investment 69' && document.querySelector('#activity-search').value==='Synthetic investment 69' && document.querySelector('[data-grid-status]').textContent==='1 of 70 results' && document.querySelectorAll('.tabulator-row').length===1`,
		),
		chromedp.Evaluate[chromedp.Void](`history.back()`),
		chromedp.Poll[chromedp.Void](
			`!new URL(location.href).searchParams.has('q') && new URL(location.href).searchParams.get('page')==='2' && document.querySelector('#activity-search').value==='' && document.querySelectorAll('.tabulator-row').length===20`,
		),
		chromedp.Evaluate[chromedp.Void](`history.go(2)`),
		chromedp.Poll[chromedp.Void](
			`new URL(location.href).searchParams.get('q')==='investment' && new URL(location.href).searchParams.get('page')==='1' && document.querySelector('#activity-search').value==='investment' && document.querySelectorAll('.tabulator-row').length===50`,
		),
	)
	// A denied preference write must leave keyboard/layout interactions usable.
	check(
		chromedp.Evaluate[chromedp.Void](
			`(()=>{const write=Storage.prototype.setItem;try{Storage.prototype.setItem=()=>{throw new DOMException('Blocked','SecurityError')};window.proofGrid.getColumn('label').setWidth(334)}finally{Storage.prototype.setItem=write}})()`,
		),
	)
	assert(
		`window.proofGrid.getColumn('label').getWidth()===334 && document.querySelectorAll('.tabulator-row').length===50`,
		"blocked preference storage prevented table interactions",
	)
	check(
		chromedp.Evaluate[chromedp.Void](
			`window.proofGrid.getColumn('label').setWidth(310);window.proofGrid.setSort('price','desc');document.activeElement.blur()`,
		),
		chromedp.Poll[chromedp.Void](
			`document.querySelector('.tabulator-row [tabulator-field=price]')?.textContent==='9007199254740993.0000000000001 USD' && !document.querySelector('[data-grid]').hasAttribute('aria-busy')`,
		),
	)
	// Reset uses fresh definitions after native persistence has loaded a layout.
	check(
		testbrowser.SetViewport(1440, 1000),
		testbrowser.NewDocument(chromedp.Navigate(origin+"/test/grid")),
		chromedp.Poll[chromedp.Void](`document.querySelector('[data-grid]').dataset.gridReady==='true'`),
	)
	assert(
		`window.Tabulator.findTable('.tabulator')[0].getColumn('label').getWidth()===310`,
		"saved column layout was not loaded before reset",
	)
	check(
		chromedp.Click(chromedp.CSS(`[data-grid-reset]`)),
		chromedp.Poll[chromedp.Void](
			`document.querySelector('.tabulator-col[tabulator-field="label"]').style.width!=='310px'`,
		),
		testbrowser.NewDocument(chromedp.Navigate(origin+"/test/grid")),
		chromedp.Poll[chromedp.Void](`document.querySelector('[data-grid]').dataset.gridReady==='true'`),
	)
	assert(
		`window.Tabulator.findTable('.tabulator')[0].getColumn('label').getWidth()!==310`,
		"reset columns restored the saved layout on reload",
	)

	check(
		testbrowser.NewDocument(chromedp.Navigate(origin+"/test/grid")),
		chromedp.WaitVisible(chromedp.CSS(".tabulator")),
	)
	check(
		chromedp.Evaluate[chromedp.Void](
			`(()=>{const h=document.querySelector('.tabulator-col[tabulator-field="label"]');h.focus();window.beforeWidth=window.Tabulator.findTable('.tabulator')[0].getColumn('label').getWidth();h.dispatchEvent(new KeyboardEvent('keydown',{key:'ArrowRight',altKey:true,bubbles:true}))})()`,
		),
	)
	assert(
		`window.Tabulator.findTable('.tabulator')[0].getColumn('label').getWidth()===beforeWidth+24`,
		"keyboard column resize",
	)
	check(
		chromedp.Evaluate[chromedp.Void](
			`document.querySelector('.tabulator-col[tabulator-field="price"]').dispatchEvent(new KeyboardEvent('keydown',{key:'Enter',bubbles:true}))`,
		),
	)
	assert(
		`window.Tabulator.findTable('.tabulator')[0].getSorters().some(s=>s.field==='price')`,
		"keyboard column sorting",
	)
}
