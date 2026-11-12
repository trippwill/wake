// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

//go:build browser

package admin

import (
	"context"
	"strconv"
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
	"go.trippwill.dev/wake/internal/testbrowser"
)

func TestBrowserCorrectionAndEvidenceNavigation(t *testing.T) {
	f := newAdminBrowserFixture(t)
	check := f.check
	assert := f.assert
	origin := f.origin

	// Direct corrections preserve the canonical parent, including after contextual history is cleared.
	for _, path := range []string{"/activity/synthetic-activity", "/instruments/synthetic-instrument"} {
		check(
			testbrowser.NewDocument(chromedp.Navigate(origin+path+"/correct?mode=decisions")),
			chromedp.WaitVisible(chromedp.CSS("[data-parent-breadcrumb]")),
		)
		assert(
			`(()=>{const parent=document.querySelector('[data-parent-breadcrumb]');return !parent.hidden&&new URL(parent.querySelector('a').href).pathname===`+strconv.Quote(
				path,
			)+`})()`,
			"direct correction lost its canonical parent",
		)
		check(
			chromedp.Evaluate[chromedp.Void](
				`history.replaceState({wakeNavigation:{workspace:'/review',trail:['/review','/evidence/synthetic-report']}},'');dispatchEvent(new PopStateEvent('popstate'))`,
			),
		)
		assert(
			`document.querySelector('[data-parent-breadcrumb] a').textContent==='Supporting evidence'`,
			"contextual parent was not applied",
		)
		check(
			chromedp.Evaluate[chromedp.Void](
				`history.replaceState(null,'');dispatchEvent(new PopStateEvent('popstate'))`,
			),
		)
		assert(
			`(()=>{const parent=document.querySelector('[data-parent-breadcrumb]');return !parent.hidden&&new URL(parent.querySelector('a').href).pathname===`+strconv.Quote(
				path,
			)+`})()`,
			"canonical parent was not restored",
		)
	}

	for _, suffix := range []string{"", "/correct?mode=decisions"} {
		check(
			testbrowser.NewDocument(chromedp.Navigate(origin+"/review?q=retained-filter&size=25")),
			chromedp.WaitVisible(chromedp.CSS("h1")),
			chromedp.Evaluate[chromedp.Void](
				`(()=>{const a=document.createElement('a');a.id='alias-proof';a.href='/instruments/retired-instrument'+`+strconv.Quote(
					suffix,
				)+`;a.textContent='Inspect retired instrument';document.body.append(a)})()`,
			),
			testbrowser.NewDocument(chromedp.Click(chromedp.CSS("#alias-proof"))),
			chromedp.Poll[chromedp.Void](`(()=>{
				const grid=document.querySelector('[data-grid]');
				return history.state?.wakeNavigation?.workspace==='/review' &&
					(!grid || (grid.dataset.gridReady==='true' && !grid.hasAttribute('aria-busy')));
			})()`),
		)
		assert(
			`location.pathname.startsWith('/instruments/synthetic-instrument')&&new URL(document.querySelector('[data-return]').href).pathname==='/review'&&new URL(document.querySelector('[data-return]').href).searchParams.get('q')==='retained-filter'&&document.querySelector('.wake-nav-link[aria-current="page"]').getAttribute('href')==='/review'`,
			"canonical redirect lost the originating workspace or filters",
		)
		check(
			testbrowser.NewDocument(chromedp.Reload()),
			chromedp.Poll[chromedp.Void](`history.state?.wakeNavigation?.workspace==='/review'`),
		)
	}

	for _, action := range []string{"link", "form"} {
		f.holdAlias.Store(true)
		check(
			testbrowser.NewDocument(chromedp.Navigate(origin+"/review?q=delayed-filter&size=25")),
			chromedp.WaitVisible(chromedp.CSS("h1")),
			chromedp.Evaluate[chromedp.Void](
				`(()=>{const a=document.createElement('a');a.id='delayed-alias';a.href='/instruments/retired-instrument';a.textContent='Open retired instrument';document.body.append(a)})()`,
			),
			testbrowser.NewDocument(chromedp.Click(chromedp.CSS("#delayed-alias"))),
			chromedp.Func(func(ctx context.Context, _ *chromedp.Target) error {
				select {
				case <-f.aliasStarted:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			}),
			chromedp.Evaluate[chromedp.Void](
				`(()=>{const form=document.createElement('form');form.id='delayed-form';form.method='get';form.action='/instruments/synthetic-instrument/correct';form.innerHTML='<input name="mode" value="decisions" type="hidden"><button type="submit" id="delayed-submit">Open corrections</button>';document.body.append(form);const a=document.createElement('a');a.id='delayed-link';a.href='/instruments/synthetic-instrument/correct?mode=decisions';a.textContent='Correct instrument data';document.body.append(a)})()`,
			),
		)

		selector := "#delayed-link"
		if action == "form" {
			selector = "#delayed-submit"
		}

		var waiting bool
		check(
			testbrowser.NewDocument(chromedp.Steps(
				chromedp.Click(chromedp.CSS(selector)),
				testbrowser.Evaluate(
					`location.pathname==='/instruments/synthetic-instrument'&&document.querySelector('[aria-busy="true"]')!==null`,
					&waiting,
				),
				chromedp.Func(func(_ context.Context, _ *chromedp.Target) error {
					f.holdAlias.Store(false)

					f.releaseAlias <- struct{}{}

					return nil
				}),
			)),
			chromedp.Poll[chromedp.Void](
				`history.state?.wakeNavigation?.workspace==='/review'&&location.pathname.endsWith('/correct')`,
			),
		)

		if !waiting {
			t.Fatal("navigation did not wait for alias verification", action)
		}

		assert(
			`new URL(document.querySelector('[data-workspace-breadcrumb] a').href).searchParams.get('q')==='delayed-filter'&&document.querySelector('.wake-nav-link[aria-current="page"]').getAttribute('href')==='/review'&&new URL(location.href).searchParams.get('mode')==='decisions'`,
			"action during alias verification lost workspace, filters or form data",
		)
	}

	check(
		testbrowser.NewDocument(chromedp.Navigate(origin+"/review")),
		chromedp.WaitVisible(chromedp.CSS("h1")),
		chromedp.Evaluate[chromedp.Void](
			`(()=>{sessionStorage.setItem('wake.navigation.pending',JSON.stringify({from:location.pathname,to:'/instruments/unrelated-instrument',navigation:{workspace:'/review',trail:['/review']}}));const a=document.createElement('a');a.id='unrelated-proof';a.href='/instruments/synthetic-instrument';a.textContent='Open unrelated destination';a.addEventListener('click',event=>event.stopPropagation());document.body.append(a)})()`,
		),
		testbrowser.NewDocument(chromedp.Click(chromedp.CSS("#unrelated-proof"))),
		chromedp.Poll[chromedp.Void](
			`document.querySelector('[data-grid]')?.dataset.gridReady==='true'&&history.state?.wakeNavigation?.workspace==='/instruments'`,
		),
	)
	assert(`history.state?.wakeNavigation?.trail.length===0`, "unrelated handoff adopted navigation context")

	check(
		testbrowser.NewDocument(chromedp.Navigate(origin+"/review?q=fragment-filter&size=25")),
		chromedp.WaitVisible(chromedp.CSS("h1")),
		chromedp.Evaluate[chromedp.Void](
			`(()=>{const a=document.createElement('a');a.id='fragment-report';a.href='/evidence/synthetic-report';a.textContent='Inspect source report';document.body.append(a)})()`,
		),
		testbrowser.NewDocument(chromedp.Click(chromedp.CSS("#fragment-report"))),
		chromedp.Poll[chromedp.Void](`document.querySelector('[data-grid]')?.dataset.gridReady==='true'`),
		chromedp.Evaluate[chromedp.Void](`window.fragmentContext=JSON.stringify(history.state.wakeNavigation)`),
	)
	assert(
		`new URL(location.href).searchParams.get('sort')==='report'&&new URL(location.href).searchParams.get('dir')==='desc'&&[...document.querySelectorAll('.tabulator-col[tabulator-field]')].filter(column=>column.getClientRects().length).length===4`,
		"artifact table wrote invalid default ordering into its URL",
	)
	check(
		chromedp.Click(chromedp.CSS(`a[href="#original-content"]`)),
		chromedp.Poll[chromedp.Void](`location.hash==='#original-content'`),
	)
	assert(
		`JSON.stringify(history.state?.wakeNavigation)===window.fragmentContext&&document.querySelector('.wake-nav-link[aria-current="page"]').getAttribute('href')==='/review'&&new URL(document.querySelector('[data-return]').href).searchParams.get('q')==='fragment-filter'`,
		"in-page report navigation discarded its return context",
	)
	check(
		chromedp.Evaluate[chromedp.Void](`history.back()`),
		chromedp.Poll[chromedp.Void](`location.hash===''`),
	)
	assert(
		`JSON.stringify(history.state?.wakeNavigation)===window.fragmentContext`,
		"fragment Back lost navigation context",
	)
	check(
		chromedp.Evaluate[chromedp.Void](`history.forward()`),
		chromedp.Poll[chromedp.Void](`location.hash==='#original-content'`),
	)
	assert(
		`JSON.stringify(history.state?.wakeNavigation)===window.fragmentContext`,
		"fragment Forward lost navigation context",
	)
	check(
		testbrowser.NewDocument(chromedp.Reload()),
		chromedp.Poll[chromedp.Void](`document.querySelector('[data-grid]')?.dataset.gridReady==='true'`),
	)
	assert(
		`history.state.wakeNavigation.workspace==='/review'&&new URL(document.querySelector('[data-return]').href).searchParams.get('q')==='fragment-filter'`,
		"fragment reload lost the originating workspace or filters",
	)
	assert(
		`new URL(location.href).searchParams.get('sort')==='report'&&new URL(location.href).searchParams.get('dir')==='desc'`,
		"artifact default ordering did not survive reload",
	)
	check(
		chromedp.SetValue(chromedp.CSS("#observation-search"), "TradeConfirm[75]"),
		chromedp.Click(chromedp.CSS("[data-observation-filters] button")),
		chromedp.Poll[chromedp.Void](
			`document.querySelector('[data-grid-status]').textContent==='1 of 75 results'`,
		),
		chromedp.Evaluate[chromedp.Void](`window.Tabulator.findTable('.tabulator')[0].setSort('outcome','asc')`),
		chromedp.Poll[chromedp.Void](
			`new URL(location.href).searchParams.get('sort')==='outcome'&&new URL(location.href).searchParams.get('dir')==='asc'&&document.querySelector('[data-grid]').dataset.gridReady==='true'`,
		),
		testbrowser.NewDocument(chromedp.Reload()),
		chromedp.Poll[chromedp.Void](
			`document.querySelector('[data-grid-status]').textContent==='1 of 75 results'`,
		),
	)
	assert(
		`new URL(location.href).searchParams.get('sort')==='outcome'&&new URL(location.href).searchParams.get('dir')==='asc'&&document.querySelector('.tabulator-col[tabulator-field="outcome"]').getAttribute('aria-sort')==='ascending'`,
		"artifact selected ordering did not survive reload",
	)
	check(
		chromedp.Evaluate[chromedp.Void](`history.back()`),
		chromedp.Poll[chromedp.Void](
			`!new URL(location.href).searchParams.has('q')&&new URL(location.href).searchParams.get('sort')==='report'&&document.querySelector('[data-grid-status]').textContent==='75 of 75 results'`,
		),
		chromedp.Evaluate[chromedp.Void](`history.forward()`),
		chromedp.Poll[chromedp.Void](
			`new URL(location.href).searchParams.get('q')==='TradeConfirm[75]'&&new URL(location.href).searchParams.get('sort')==='outcome'&&document.querySelector('[data-grid-status]').textContent==='1 of 75 results'`,
		),
	)

	check(
		testbrowser.NewDocument(chromedp.Navigate(origin+"/test/reference-evidence")),
		chromedp.WaitVisible(chromedp.CSS("#original-content summary")),
	)
	assert(
		`!document.querySelector('#original-content').open&&!document.querySelector('a[href="#original-content"]')`,
		"duplicate evidence action",
	)
	check(
		chromedp.Focus(chromedp.CSS("#original-content summary")),
		chromedp.SendKeys(chromedp.CSS("#original-content summary"), "\r"),
		chromedp.WaitVisible(chromedp.CSS(`pre[aria-label="Retained response"]`)),
	)
	assert(
		`document.querySelector('#original-content').open&&document.querySelector('pre[aria-label="Retained request"]').textContent.includes('\n    "idType"')`,
		"keyboard evidence disclosure",
	)
	check(
		testbrowser.NewDocument(chromedp.Navigate(origin+"/test/correction")),
		chromedp.WaitVisible(chromedp.CSS("#decision-scope")),
	)
	assert(
		`document.querySelector('#decision-scope').value==='' && document.querySelector('[data-correction]').hidden && !document.querySelector('#decision-value')`,
		"explicit choice or comparison",
	)
	check(
		chromedp.SetValue(chromedp.CSS("#decision-scope"), "price"),
		chromedp.Evaluate[chromedp.Void](
			`document.querySelector('#decision-scope').dispatchEvent(new Event('change',{bubbles:true}))`,
		),
	)
	assert(
		`document.querySelector('#decision-value').inputMode==='decimal' && document.querySelector('select#decision-currency:required:enabled').value==='USD'`,
		"decimal and currency controls did not preserve the current price",
	)
	assert(
		`!document.querySelector('#decision-current').hidden && document.querySelector('#decision-current').textContent.includes('9007199254740992.9999999999999 USD')`,
		"exact current value missing beside edit",
	)

	chooseField := func(field string) {
		t.Helper()
		check(
			chromedp.SetValue(chromedp.CSS("#decision-scope"), field),
			chromedp.Evaluate[chromedp.Void](
				`document.querySelector('#decision-scope').dispatchEvent(new Event('change',{bubbles:true}))`,
			),
		)
	}
	setCorrection := func(value string) {
		t.Helper()
		check(
			chromedp.SetValue(chromedp.CSS("#decision-value"), value),
			chromedp.Evaluate[chromedp.Void](
				`document.querySelector('#decision-value').dispatchEvent(new Event('input',{bubbles:true}))`,
			),
		)
	}
	setCurrency := func(code string) {
		t.Helper()
		check(
			chromedp.SetValue(chromedp.CSS("#decision-currency"), code),
			chromedp.Evaluate[chromedp.Void](
				`document.querySelector('#decision-currency').dispatchEvent(new Event('change',{bubbles:true}))`,
			),
		)
	}
	ready := func() {
		t.Helper()
		check(
			chromedp.Poll[chromedp.Void](
				`document.querySelector('#decision-feedback').dataset.state==='valid' && !document.querySelector('[data-correction] button[type="submit"]').disabled`,
			),
		)
	}

	setCorrection("12x")
	assert(
		`document.querySelector('[data-correction] button[type="submit"]').disabled && document.querySelector('#decision-feedback').dataset.state==='invalid'`,
		"malformed decimal accepted locally",
	)
	setCorrection("9007199254740993.0000000000001")
	setCurrency("")
	assert(
		`document.querySelector('[data-correction] button[type="submit"]').disabled && document.querySelector('#decision-feedback').textContent==='Choose a currency.'`,
		"missing currency allowed saving a price",
	)
	setCurrency("HKD")
	f.correctionCurrencies.Store(new([]string{"QZZ", "USD"}))
	ready()
	assert(
		`document.querySelector('#decision-value').value==='9007199254740993.0000000000001'`,
		"live validation rounded a decimal",
	)
	chooseField("quantity")
	setCorrection("0")
	check(
		chromedp.Poll[chromedp.Void](`document.querySelector('#decision-feedback').dataset.state==='invalid'`),
	)
	assert(
		`document.querySelector('[data-correction] button[type="submit"]').disabled`,
		"zero quantity accepted by live check",
	)
	chooseField("instrument")
	check(chromedp.SendKeys(chromedp.CSS("#decision-link-search"), "second"))
	assert(
		`document.querySelector('#decision-value').tagName==='SELECT' && document.querySelector('#decision-value').value==='instrument-one' && !document.querySelector('#decision-value option[value="instrument-three"]')`,
		"searchable links changed the selection or kept unrelated matches",
	)
	setCorrection("instrument-two")
	ready()
	assert(
		`new FormData(document.querySelector('[data-correction]')).get('value')==='instrument-two' && document.querySelector('[data-link-selection]').textContent.includes('second option')`,
		"link selection missing readable identity",
	)
	chooseField("side")
	assert(
		`document.querySelector('#decision-value').tagName==='SELECT' && document.querySelector('#decision-value').value==='buy'`,
		"side is not a typed choice",
	)
	chooseField("date")
	assert(`document.querySelector('#decision-value').type==='date'`, "calendar date control missing")
	check(
		chromedp.Evaluate[chromedp.Void](
			`document.querySelector("#decision-value").value="2026-02-30";document.querySelector("#decision-value").dispatchEvent(new Event("input",{bubbles:true}))`,
		),
	)
	assert(
		`document.querySelector('[data-correction] button[type="submit"]').disabled`,
		"invalid calendar date accepted",
	)
	setCorrection("2028-02-29")
	ready()
	chooseField("at")
	assert(
		`document.querySelector('#decision-value').type==='hidden' && document.querySelector('#decision-time').value==='13:30:00.120000000' && document.querySelector('#decision-zone').value==='Z'`,
		"timestamp precision lost while configuring controls",
	)
	check(
		chromedp.SetValue(chromedp.CSS("#decision-time"), "14:05:06.120000000"),
		chromedp.SetValue(chromedp.CSS("#decision-zone"), "+01:30"),
		chromedp.Evaluate[chromedp.Void](
			`document.querySelector('#decision-zone').dispatchEvent(new Event('input',{bubbles:true}))`,
		),
	)
	ready()
	assert(
		`document.querySelector('#decision-value').value==='2026-10-20T14:05:06.120000000+01:30'`,
		"timestamp offset or fractional zeros changed",
	)
	f.offline.Store(true)
	check(
		chromedp.Evaluate[chromedp.Void](
			`document.querySelector('#decision-zone').dispatchEvent(new Event('input',{bubbles:true}))`,
		),
		chromedp.WaitVisible(chromedp.CSS("[data-check-again]")),
	)
	assert(
		`document.querySelector('[data-correction] button[type="submit"]').disabled`,
		"validation failure allowed saving",
	)
	f.offline.Store(false)
	check(chromedp.Click(chromedp.CSS("[data-check-again]")))
	ready()
	chooseField("price")
	assert(
		`document.querySelector('#decision-value').value==='9007199254740993.0000000000001' && document.querySelector('#decision-currency').value==='HKD' && !document.querySelector('#decision-currency option[value="QZZ"]')`,
		"field-specific draft lost on switching",
	)
	ready()
	assert(
		`!document.querySelector('#workspace-content').hasAttribute('hx-trigger')`,
		"correction form is not a stable review snapshot",
	)
	check(
		testbrowser.NewDocument(chromedp.Navigate(origin+"/test/correction?field=price")),
		chromedp.WaitVisible(chromedp.CSS("#decision-currency")),
	)
	assert(
		`document.querySelector('#decision-currency option[value="QZZ"]')!==null && !document.querySelector('#decision-currency option[value="HKD"]')`,
		"new correction form did not use the latest currency choices",
	)
	f.correctionCurrencies.Store(nil)

	check(
		testbrowser.NewDocument(chromedp.Navigate(origin+"/test/correction?mode=evidence")),
		chromedp.WaitVisible(chromedp.CSS("#decision-candidate")),
	)
	assert(
		`document.querySelector('#decision-candidate').value==='' && !document.querySelector('[data-candidate]')`,
		"candidate selected implicitly",
	)
	check(
		chromedp.SetValue(chromedp.CSS("#decision-candidate"), "candidate-one"),
		chromedp.Click(chromedp.CSS(`form[aria-label="Compare retained evidence"] button`)),
		chromedp.WaitVisible(chromedp.CSS("[data-candidate]")),
	)
	assert(
		`document.querySelector('[data-different="true"]').textContent.includes('9007199254740993.0000000000001')`,
		"exact candidate comparison missing",
	)

	check(
		testbrowser.NewDocument(
			chromedp.Navigate(origin+"/evidence/synthetic-report"),
		),
		chromedp.Poll[chromedp.Void](`document.querySelector('[data-grid]').dataset.gridReady==='true'`),
	)
	assert(
		`document.querySelectorAll('.tabulator-row').length===50 && !new URL(location.href).searchParams.has('location')`,
		"canonical report or bounded initial page missing",
	)
	assert(
		`(()=>{const link=new URL(document.querySelector('.tabulator-row [tabulator-field="activity"] a').href);return link.pathname==='/activity/synthetic-activity'&&!link.search})()`,
		"related activity link contains view parameters",
	)
	check(
		chromedp.Evaluate[chromedp.Void](
			`window.reportTable=window.Tabulator.findTable('.tabulator')[0]; window.reportTable.setPageSize(25).then(()=>window.reportTable.setPage(2))`,
		),
		chromedp.Poll[chromedp.Void](
			`document.querySelector('[data-grid-status]').textContent.includes('75 of 75') && new URL(location.href).searchParams.get('page')==='2'`,
		),
	)
	assert(
		`document.querySelectorAll('.tabulator-row').length===25`,
		"report table paging lost bounds",
	)
	check(
		chromedp.SetValue(chromedp.CSS("#observation-search"), "no additional contribution"),
		chromedp.Click(chromedp.CSS("[data-observation-filters] button")),
		chromedp.Poll[chromedp.Void](
			`document.querySelector('[data-grid-status]').textContent==='50 of 75 results'`,
		),
	)
	assert(
		`[...document.querySelectorAll('.tabulator-row [tabulator-field="outcome"]')].every(cell=>cell.textContent==='no additional contribution')`,
		"report contribution label was not displayed or searchable",
	)
	check(
		chromedp.SetValue(chromedp.CSS("#observation-search"), "TradeConfirm[75]"),
		chromedp.Click(chromedp.CSS("[data-observation-filters] button")),
		chromedp.Poll[chromedp.Void](
			`document.querySelector('[data-grid-status]').textContent==='1 of 75 results'`,
		),
	)
	assert(
		`document.querySelector('.tabulator-row').textContent.includes('/r[1]'.repeat(200)+'/TradeConfirm[75]')`,
		"long source path is missing from filtered observation",
	)
	check(
		chromedp.Focus(chromedp.CSS(`.tabulator-col[tabulator-field="location"]`)),
		chromedp.KeyEvent(kb.Enter),
		chromedp.Poll[chromedp.Void](
			`new URL(location.href).searchParams.get('sort')==='location' && document.querySelector('[data-grid]').dataset.gridReady==='true'`,
		),
		chromedp.Evaluate[chromedp.Void](
			`window.previousObservationColumns=localStorage.getItem('tabulator-wake-observations-columns');window.reportTable.getColumn('location').setWidth(410)`,
		),
	)
	assert(
		`localStorage.getItem('tabulator-wake-observations-artifact-columns')!==null && localStorage.getItem('tabulator-wake-observations-columns')===window.previousObservationColumns`,
		"report column preferences overwrote the ordinary observation layout",
	)
}
