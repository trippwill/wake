// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

//go:build browser

package admin

import (
	"context"
	"testing"
	"time"

	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
	"go.trippwill.dev/wake/internal/testbrowser"
)

func TestBrowserGridRefreshAndInteraction(t *testing.T) {
	f := newAdminBrowserFixture(t)
	check := f.check
	assert := f.assert
	origin := f.origin
	ctx := f.ctx

	f.visitGrid("/test/grid?q=investment&size=50", 50, 50)
	check(
		chromedp.Evaluate[chromedp.Void](`void (window.proofGrid=window.Tabulator.findTable(".tabulator")[0])`),
	)
	check(
		chromedp.Evaluate[chromedp.Void](
			`window.proofGrid.getColumn('label').setWidth(310);window.proofGrid.setSort('price','desc');document.activeElement.blur()`,
		),
		chromedp.Poll[chromedp.Void](
			`document.querySelector('.tabulator-row [tabulator-field=price]')?.textContent==='9007199254740993.0000000000001 USD' && !document.querySelector('[data-grid]').hasAttribute('aria-busy')`,
		),
	)
	check(
		chromedp.Evaluate[chromedp.Void](
			`window.proofGrid.setPage(2);document.querySelector('#view-notes').open=true`,
		),
		chromedp.Poll[chromedp.Void](
			`new URL(location.href).searchParams.get('page')==='2' && document.querySelectorAll('.tabulator-row').length===20`,
		),
	)

	// A real polling tick must leave an unchanged page in place throughout the
	// download and after completion, including its row nodes and scroll position.
	check(chromedp.Evaluate[chromedp.Void](`
		window.beforePollRow=document.querySelector('.tabulator-row');
		document.querySelector('.tabulator-tableholder').scrollTop=150;
		window.beforePollScroll=document.querySelector('.tabulator-tableholder').scrollTop;
	`))
	f.holdRefresh.Store(true)

	select {
	case <-f.refreshStarted:
	case <-time.After(15 * time.Second):
		t.Fatal("polling did not request fresh table data")
	}

	assert(
		`window.beforePollRow.isConnected && document.querySelectorAll('.tabulator-row').length===20 && document.querySelector('.tabulator-tableholder').clientHeight>0 && document.querySelector('[data-grid]').getAttribute('aria-busy')==='true'`,
		"polling cleared the current rows while downloading",
	)

	f.releaseRefresh <- struct{}{}

	check(chromedp.Poll[chromedp.Void](`!document.querySelector('[data-grid]').hasAttribute('aria-busy')`))
	assert(
		`window.beforePollRow===document.querySelector('.tabulator-row') && document.querySelector('.tabulator-tableholder').scrollTop===window.beforePollScroll && new URL(location.href).searchParams.get('page')==='2' && document.querySelector('[data-grid-refresh]').hidden`,
		"unchanged polling response rebuilt the table or reset the current view",
	)

	f.extra.Store(true)
	check(
		chromedp.Evaluate[chromedp.Void](
			`window.htmx.trigger(document.querySelector('#workspace-content'),'sse:refresh')`,
		),
		chromedp.WaitVisible(chromedp.CSS("[data-grid-refresh]")),
	)
	assert(
		`window.beforePollRow===document.querySelector('.tabulator-row') && document.querySelectorAll('.tabulator-row').length===20 && document.querySelector('[data-grid-status]').textContent.includes('Updated results are available')`,
		"changed response replaced rows before explicit refresh",
	)
	// If fresh results return to the displayed state, there is no net change
	// left to accept and the notice disappears without touching the rows.
	check(chromedp.Focus(chromedp.CSS("[data-grid-refresh]")))
	f.extra.Store(false)
	check(
		chromedp.Evaluate[chromedp.Void](
			`window.htmx.trigger(document.querySelector('#workspace-content'),'sse:refresh')`,
		),
		chromedp.Poll[chromedp.Void](
			`!document.querySelector('[data-grid]').hasAttribute('aria-busy') && document.querySelector('[data-grid-refresh]').hidden`,
		),
	)
	assert(
		`window.beforePollRow===document.querySelector('.tabulator-row')`,
		"reverted update changed the displayed rows",
	)
	assert(
		`document.activeElement.matches('.tabulator-col')`,
		"reverted update left focus on a hidden refresh button",
	)
	f.extra.Store(true)
	// Refresh fetches the current response rather than installing the snapshot
	// that first raised the notice; a last-moment reversion keeps the rows intact.
	check(
		chromedp.Evaluate[chromedp.Void](
			`window.htmx.trigger(document.querySelector('#workspace-content'),'sse:refresh')`,
		),
		chromedp.WaitVisible(chromedp.CSS("[data-grid-refresh]")),
	)

	f.extra.Store(false)
	check(
		chromedp.Focus(chromedp.CSS("[data-grid-refresh]")),
		chromedp.KeyEvent("\r"),
		chromedp.Poll[chromedp.Void](
			`document.querySelector('[data-grid-refresh]').hidden && document.querySelector('[data-grid]').dataset.gridReady==='true'`,
		),
	)
	assert(
		`window.beforePollRow===document.querySelector('.tabulator-row')`,
		"refresh applied a stale polling response",
	)
	f.extra.Store(true)
	check(
		chromedp.Evaluate[chromedp.Void](
			`window.htmx.trigger(document.querySelector('#workspace-content'),'sse:refresh')`,
		),
		chromedp.WaitVisible(chromedp.CSS("[data-grid-refresh]")),
	)
	check(
		chromedp.Focus(chromedp.CSS("[data-grid-refresh]")),
		chromedp.KeyEvent("\r"),
		chromedp.Poll[chromedp.Void](
			`document.querySelector('[data-grid-status]').textContent==='71 of 71 results' && document.querySelectorAll('.tabulator-row').length===21`,
		),
	)
	assert(
		`new URL(location.href).searchParams.get('page')==='2' && window.proofGrid.getSorters()[0].dir==='desc' && window.proofGrid.getColumn('label').getWidth()===310 && document.querySelector('#activity-search').value==='investment' && new URL(location.href).searchParams.get('q')==='investment'`,
		"remote view state lost on refresh",
	)
	assert(`document.querySelector('#view-notes').open`, "disclosure lost on refresh")
	assert(
		`document.activeElement.matches('.tabulator-col') && document.querySelector('[data-grid-refresh]').hidden`,
		"keyboard refresh did not restore focus to the table",
	)
	// A response may update rows while an unsubmitted filter draft keeps focus.
	f.extra.Store(false)
	f.holdRefresh.Store(true)
	check(
		chromedp.Focus(chromedp.CSS(`.tabulator-col[tabulator-field="label"]`)),
		chromedp.Evaluate[chromedp.Void](
			`window.htmx.trigger(document.querySelector('#workspace-content'),'sse:refresh')`,
		),
	)

	select {
	case <-f.refreshStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("delayed JSON refresh did not start")
	}

	check(
		chromedp.Focus(chromedp.CSS("#activity-search")),
		chromedp.Evaluate[chromedp.Void](
			`document.querySelector('#activity-search').value='unsubmitted filter draft'`,
		),
	)

	f.releaseRefresh <- struct{}{}

	check(
		applyBrowserGridUpdate(),
		chromedp.Poll[chromedp.Void](
			`document.querySelector('[data-grid-status]').textContent==='70 of 70 results' && document.querySelectorAll('.tabulator-row').length===20`,
		),
	)
	assert(
		`document.activeElement.id==='activity-search' && document.activeElement.value==='unsubmitted filter draft' && document.querySelector('#view-notes').open`,
		"JSON refresh changed focused draft",
	)
	check(
		chromedp.Evaluate[chromedp.Void](
			`document.querySelector('#activity-search').value='investment';document.activeElement.blur()`,
		),
	)
	f.extra.Store(true)
	f.refreshFailed.Store(true)
	check(
		chromedp.Evaluate[chromedp.Void](
			`window.htmx.trigger(document.querySelector('#workspace-content'),'sse:refresh')`,
		),
		chromedp.WaitVisible(chromedp.CSS("[data-grid-retry]")),
	)
	assert(
		`document.querySelectorAll('.tabulator-row').length===20 && document.querySelector('[data-grid-status]').textContent.includes('Previous results') && document.querySelector('#view-notes').open`,
		"failed JSON request replaced displayed data",
	)
	f.refreshFailed.Store(false)
	check(
		chromedp.Click(chromedp.CSS("[data-grid-retry]")),
		applyBrowserGridUpdate(),
		chromedp.Poll[chromedp.Void](
			`document.querySelector('[data-grid-status]').textContent==='71 of 71 results' && new URL(location.href).searchParams.get('page')==='2'`,
		),
	)
	// Focus follows the operator's latest choice within the grid, including
	// entering a row after a background request has already started.

	for _, initial := range []string{`document.querySelector('.tabulator-col[tabulator-field="label"]').focus()`, `document.activeElement.blur()`} {
		check(chromedp.Evaluate[chromedp.Void](initial))
		f.holdMovedFocus.Store(true)
		check(
			chromedp.Evaluate[chromedp.Void](
				`window.htmx.trigger(document.querySelector('#workspace-content'),'sse:refresh')`,
			),
		)

		select {
		case <-f.movedFocusStarted:
		case <-time.After(10 * time.Second):
			t.Fatal("focus movement did not hold a JSON request")
		}

		check(
			chromedp.Evaluate[chromedp.Void](
				`(()=>{const link=document.querySelectorAll('.tabulator-row a')[1];link.focus();document.querySelector('[data-grid]').dataset.focusTarget=link.closest('[data-record]').dataset.record})()`,
			),
		)

		f.extra.Store(!f.extra.Load())

		select {
		case f.releaseMovedFocus <- struct{}{}:
		case <-time.After(10 * time.Second):
			t.Fatal("focus response was abandoned")
		}

		check(
			applyBrowserGridUpdate(),
			chromedp.Poll[chromedp.Void](
				`!document.querySelector('[data-grid]').hasAttribute('aria-busy')&&document.querySelector('[data-grid]').dataset.gridReady==='true'`,
			),
		)

		var (
			focusOK bool
			trace   string
		)

		check(
			testbrowser.Evaluate(
				`document.activeElement.closest('[data-record]')?.dataset.record===document.querySelector('[data-grid]').dataset.focusTarget`,
				&focusOK,
			),
		)

		if !focusOK {
			check(
				testbrowser.Evaluate(
					`JSON.stringify({active:document.activeElement.tagName,record:document.activeElement.closest('[data-record]')?.dataset.record,href:document.activeElement.getAttribute('href'),expected:document.querySelector('[data-grid]').dataset.focusTarget})`,
					&trace,
				),
			)
			t.Fatal("focus after delayed response", initial, trace)
		}
	}

	// Copying an exact displayed amount must survive both a queued refresh and a
	// response that was already downloading when selection began.
	check(chromedp.Poll[chromedp.Void](`!document.querySelector('[data-grid]').hasAttribute('aria-busy')`))

	check(
		chromedp.Evaluate[chromedp.Void](
			`(()=>{const range=document.createRange();range.selectNodeContents(document.querySelector('.tabulator-row [tabulator-field="price"]'));document.getSelection().removeAllRanges();document.getSelection().addRange(range);window.copiedExactValue=document.getSelection().toString();window.htmx.trigger(document.querySelector('#workspace-content'),'sse:refresh')})()`,
		),
		chromedp.Poll[chromedp.Void](
			`!document.querySelector('[data-grid]').hasAttribute('aria-busy')`,
		),
	)

	assert(
		`document.getSelection().toString()===window.copiedExactValue && window.copiedExactValue!==''`,
		"background check disturbed selected exact text",
	)

	check(chromedp.Evaluate[chromedp.Void](`document.getSelection().removeAllRanges()`))
	f.holdSelection.Store(true)
	check(
		chromedp.Evaluate[chromedp.Void](
			`window.htmx.trigger(document.querySelector('#workspace-content'),'sse:refresh')`,
		),
	)

	select {
	case <-f.selectionStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("selection test did not hold a JSON request")
	}

	f.extra.Store(false)
	check(
		chromedp.Evaluate[chromedp.Void](
			`(()=>{const range=document.createRange();range.selectNodeContents(document.querySelector('.tabulator-row [tabulator-field="price"]'));document.getSelection().removeAllRanges();document.getSelection().addRange(range);window.copiedExactValue=document.getSelection().toString()})()`,
		),
	)
	close(f.releaseSelection)
	check(chromedp.WaitVisible(chromedp.CSS("[data-grid-refresh]")))
	assert(
		`document.getSelection().toString()===window.copiedExactValue&&window.copiedExactValue!==''&&document.querySelectorAll('.tabulator-row').length===21`,
		"in-flight response erased selected exact text",
	)
	check(
		chromedp.Evaluate[chromedp.Void](`document.getSelection().removeAllRanges()`),
		applyBrowserGridUpdate(),
		chromedp.Poll[chromedp.Void](
			`document.querySelector('[data-grid-status]').textContent==='70 of 70 results'&&document.querySelectorAll('.tabulator-row').length===20`,
		),
	)

	// A disappearing final page also waits for consent, then moves to the last
	// available page. A later increase updates the pagination controls on refresh.
	f.extra.Store(true)
	check(
		chromedp.Evaluate[chromedp.Void](
			`window.proofGrid.setPageSize(70).then(()=>window.proofGrid.setPage(2))`,
		),
		chromedp.Poll[chromedp.Void](
			`document.querySelectorAll('.tabulator-row').length===1 && !document.querySelector('[data-grid]').hasAttribute('aria-busy')`,
		),
	)
	f.extra.Store(false)
	check(
		chromedp.Evaluate[chromedp.Void](
			`window.htmx.trigger(document.querySelector('#workspace-content'),'sse:refresh')`,
		),
		chromedp.WaitVisible(chromedp.CSS("[data-grid-refresh]")),
	)
	assert(
		`window.proofGrid.getPage()===2 && document.querySelectorAll('.tabulator-row').length===1`,
		"background removal changed the displayed page",
	)
	check(
		applyBrowserGridUpdate(),
		chromedp.Poll[chromedp.Void](
			`document.querySelector('.tabulator-page[data-page="1"]').classList.contains('active') && document.querySelector('.tabulator-page[data-page="next"]').disabled && document.querySelectorAll('.tabulator-row').length===70`,
		),
	)
	f.extra.Store(true)
	check(
		chromedp.Evaluate[chromedp.Void](
			`window.htmx.trigger(document.querySelector('#workspace-content'),'sse:refresh')`,
		),
		applyBrowserGridUpdate(),
	)
	assert(
		`document.querySelector('.tabulator-page[data-page="2"]')!==null && !document.querySelector('.tabulator-page[data-page="next"]').disabled`,
		"explicit refresh did not update page controls",
	)

	// Retry finishes the original foreground action with one click, while the
	// earlier background-error test still requires an explicit Refresh afterward.
	f.refreshFailed.Store(true)
	check(
		chromedp.Evaluate[chromedp.Void](
			`(()=>{const input=document.querySelector('#activity-search');input.value='Synthetic investment 69';input.form.requestSubmit()})()`,
		),
		chromedp.WaitVisible(chromedp.CSS("[data-grid-retry]")),
	)
	f.refreshFailed.Store(false)
	check(
		chromedp.Click(chromedp.CSS("[data-grid-retry]")),
		chromedp.Poll[chromedp.Void](
			`document.querySelectorAll('.tabulator-row').length===1 && document.querySelector('[data-grid-status]').textContent==='1 of 71 results' && document.querySelector('[data-grid-refresh]').hidden`,
		),
		chromedp.Evaluate[chromedp.Void](
			`(()=>{const input=document.querySelector('#activity-search');input.value='investment';input.form.requestSubmit()})()`,
		),
		chromedp.Poll[chromedp.Void](
			`document.querySelectorAll('.tabulator-row').length===70 && !document.querySelector('[data-grid]').hasAttribute('aria-busy')`,
		),
	)
	f.refreshFailed.Store(true)
	check(
		chromedp.Evaluate[chromedp.Void](`window.proofGrid.setSort('price','asc')`),
		chromedp.WaitVisible(chromedp.CSS("[data-grid-retry]")),
	)
	f.refreshFailed.Store(false)
	check(
		chromedp.Click(chromedp.CSS("[data-grid-retry]")),
		chromedp.Poll[chromedp.Void](
			`new URL(location.href).searchParams.get('dir')==='asc' && document.querySelector('.tabulator-row [tabulator-field=price]').textContent==='-1.0000000000000001 USD' && document.querySelector('[data-grid-refresh]').hidden`,
		),
	)
	f.refreshFailed.Store(true)
	check(
		chromedp.Evaluate[chromedp.Void](`window.proofGrid.setPage(2)`),
		chromedp.WaitVisible(chromedp.CSS("[data-grid-retry]")),
	)
	f.refreshFailed.Store(false)
	check(
		chromedp.Click(chromedp.CSS("[data-grid-retry]")),
		chromedp.Poll[chromedp.Void](
			`new URL(location.href).searchParams.get('page')==='2' && document.querySelectorAll('.tabulator-row').length===1 && document.querySelector('[data-grid-refresh]').hidden`,
		),
	)
	f.extra.Store(false)
	check(
		chromedp.Evaluate[chromedp.Void](
			`window.htmx.trigger(document.querySelector('#workspace-content'),'sse:refresh')`,
		),
		chromedp.WaitVisible(chromedp.CSS("[data-grid-refresh]")),
	)
	f.refreshFailed.Store(true)
	check(
		chromedp.Click(chromedp.CSS("[data-grid-refresh]")),
		chromedp.WaitVisible(chromedp.CSS("[data-grid-retry]")),
	)
	f.refreshFailed.Store(false)
	check(
		chromedp.Click(chromedp.CSS("[data-grid-retry]")),
		chromedp.Poll[chromedp.Void](
			`new URL(location.href).searchParams.get('page')==='1' && document.querySelectorAll('.tabulator-row').length===70 && document.querySelector('[data-grid-status]').textContent==='70 of 70 results' && document.querySelector('[data-grid-refresh]').hidden`,
		),
	)

	// A response is not the displayed baseline until its rows are installed.
	// Empty results, a failed fallback and Retry must never retain stale rows.
	f.extra.Store(true)
	check(
		chromedp.Evaluate[chromedp.Void](
			`window.Tabulator.findTable('.tabulator')[0].setData().then(()=>window.Tabulator.findTable('.tabulator')[0].setPage(2))`,
		),
		chromedp.Poll[chromedp.Void](
			`document.querySelectorAll('.tabulator-row').length===1 && !document.querySelector('[data-grid]').hasAttribute('aria-busy')`,
		),
	)
	f.emptyGrid.Store(true)
	check(
		chromedp.Evaluate[chromedp.Void](
			`window.htmx.trigger(document.querySelector('#workspace-content'),'sse:refresh')`,
		),
		chromedp.WaitVisible(chromedp.CSS("[data-grid-refresh]")),
	)
	f.failFirstPage.Store(true)
	check(
		chromedp.Click(chromedp.CSS("[data-grid-refresh]")),
		chromedp.WaitVisible(chromedp.CSS("[data-grid-retry]")),
	)
	assert(
		`document.querySelectorAll('.tabulator-row').length===1 && document.querySelector('[data-grid-status]').textContent.includes('Previous results')`,
		"failed page fallback obscured the retained results or its error",
	)
	f.failFirstPage.Store(false)
	check(
		chromedp.Click(chromedp.CSS("[data-grid-retry]")),
		chromedp.Poll[chromedp.Void](
			`document.querySelectorAll('.tabulator-row').length===0 && document.querySelector('[data-grid-status]').textContent==='0 of 71 results' && document.querySelector('[data-grid-refresh]').hidden`,
		),
		chromedp.Evaluate[chromedp.Void](
			`window.htmx.trigger(document.querySelector('#workspace-content'),'sse:refresh')`,
		),
		chromedp.Poll[chromedp.Void](`!document.querySelector('[data-grid]').hasAttribute('aria-busy')`),
	)
	assert(
		`document.querySelectorAll('.tabulator-row').length===0 && document.querySelector('[data-grid-refresh]').hidden`,
		"background comparison lost the installed empty baseline",
	)
	f.emptyGrid.Store(false)

	f.offline.Store(true)
	check(
		chromedp.Evaluate[chromedp.Void](`document.dispatchEvent(new FocusEvent('focusout'))`),
		chromedp.Poll[chromedp.Void](`document.body.dataset.liveState==='stale'`),
	)
	check(
		testbrowser.SetViewport(320, 800),
		chromedp.Poll[chromedp.Void](
			`matchMedia('(max-width: 64rem)').matches && document.querySelector('#main-navigation').hidden`,
		),
		chromedp.Click(chromedp.CSS(".wake-contact-trigger")),
		chromedp.Poll[chromedp.Void](`document.querySelector('.wake-contact-details').open`),
	)
	f.offline.Store(false)
	f.expired.Store(true)
	check(
		chromedp.Evaluate[chromedp.Void](`document.dispatchEvent(new FocusEvent('focusout'))`),
		chromedp.Poll[chromedp.Void](
			`(()=>{
				const contact=document.querySelector('.wake-sidebar > .wake-contact');
				const link=contact.querySelector(':scope > #status-sign-in');
				const details=contact.querySelector('details');
				return link && details.open && contact.querySelector('#daemon-contact').textContent==='Sign-in required' &&
					link.getBoundingClientRect().top>=details.getBoundingClientRect().bottom &&
					document.querySelector('#main-navigation').hidden;
			})()`,
		),
		chromedp.Focus(chromedp.CSS(".wake-contact-trigger")),
		chromedp.KeyEvent(kb.Tab),
		chromedp.Poll[chromedp.Void](
			`document.activeElement.id==='status-sign-in' && document.activeElement.pathname==='/login'`,
		),
		testbrowser.SetViewport(1440, 1000),
	)
	f.expired.Store(false)
	check(
		testbrowser.NewDocument(chromedp.Navigate(
			origin+"/instruments/synthetic-instrument?q=synthetic&sort=location&dir=asc",
		)),
		chromedp.WaitVisible(chromedp.CSS(".tabulator-row")),
	)
	check(
		chromedp.Evaluate[chromedp.Void](
			`window.observationGrid=window.Tabulator.findTable('.tabulator')[0];window.observationGrid.setPageSize(1).then(()=>window.observationGrid.setPage(2))`,
		),
		chromedp.Poll[chromedp.Void](
			`new URL(location.href).searchParams.get('page')==='2' && document.querySelectorAll('.tabulator-row').length===1`,
		),
	)
	assert(
		`(()=>{const link=new URL(document.querySelector('.tabulator-row a').href);return !link.search&&new URL(location.href).searchParams.get('page')==='2'&&new URL(location.href).searchParams.get('dir')==='asc'})()`,
		"evidence link retained the previous observation page",
	)
	check(
		chromedp.Evaluate[chromedp.Void](`window.observationGrid.setSort('location','desc')`),
		chromedp.Poll[chromedp.Void](
			`new URL(location.href).searchParams.get('dir')==='desc' && new URL(location.href).searchParams.get('page')==='1' && !document.querySelector('[data-grid]').hasAttribute('aria-busy')`,
		),
	)
	assert(
		`(()=>{const link=new URL(document.querySelector('.tabulator-row a').href);return !link.search&&new URL(location.href).searchParams.get('page')==='1'&&new URL(location.href).searchParams.get('dir')==='desc'})()`,
		"evidence link retained the previous observation order",
	)
	check(
		chromedp.Click(chromedp.CSS(`[data-refresh-accepted]`)),
		chromedp.WaitVisible(chromedp.CSS(`.tabulator-row`)),
	)
	assert(
		`new URL(location.href).searchParams.get('q')==='synthetic'&&new URL(location.href).searchParams.get('dir')==='desc'&&document.querySelector('#observation-search').value==='synthetic'`,
		"accepted-detail refresh discarded current observation state",
	)

	check(
		testbrowser.NewDocument(chromedp.Navigate(origin+"/test/reports")),
		chromedp.WaitVisible(chromedp.CSS(`[data-disclosure="receipt-synthetic-receipt"]`)),
	)
	check(
		chromedp.Evaluate[chromedp.Void](
			`document.querySelector('[data-disclosure="receipt-synthetic-receipt"]').open=true;document.querySelector('.wake-brand').focus();document.getSelection().removeAllRanges();document.querySelector('#workspace-content').dataset.probe='pending';window.htmx.trigger(document.querySelector('#workspace-content'),'sse:refresh')`,
		),
		chromedp.Poll[chromedp.Void](`!document.querySelector('#workspace-content').hasAttribute('data-probe')`),
	)
	assert(
		`document.querySelector('[data-disclosure="receipt-synthetic-receipt"]').open`,
		"live region replacement closed a retained receipt disclosure",
	)

	f.extra.Store(true)

	check(
		testbrowser.SetViewport(390, 1000),
		testbrowser.NewDocument(chromedp.Navigate(origin+"/test/observations")),
		chromedp.Poll[chromedp.Void](
			`document.querySelector('[data-grid="observations"]')?.dataset.gridReady==='true'`,
		),
	)
	assert(
		`!document.querySelector('.tabulator details') && document.querySelector('.tabulator-row [tabulator-field="location"]').textContent.endsWith('/TradeConfirm[1]') && document.querySelector('.tabulator-row [tabulator-field="rule"]').textContent==='qualified-activity-observations/2 · 1'`,
		"observation metadata is not directly available in the table",
	)
	assert(
		`Array.from(document.querySelectorAll('.tabulator-cell')).every(cell=>cell.scrollHeight<=cell.clientHeight+1) && document.documentElement.scrollWidth<=innerWidth+1`,
		"observation metadata clipped or page overflowed",
	)

	f.extra.Store(false)
	check(
		chromedp.Evaluate[chromedp.Void](
			`document.activeElement.blur();window.htmx.trigger(document.querySelector('#workspace-content'),'sse:refresh')`,
		),
		applyBrowserGridUpdate(),
		chromedp.Poll[chromedp.Void](
			`document.querySelector('.tabulator-row:last-child').textContent.includes('needs review')`,
		),
	)
	assert(
		`!document.querySelector('.tabulator details') && Array.from(document.querySelectorAll('.tabulator-cell')).every(cell=>cell.scrollHeight<=cell.clientHeight+1) && document.querySelector('.tabulator-row [tabulator-field="location"]').textContent.endsWith('/TradeConfirm[1]')`,
		"refreshed observation metadata clipped or hidden",
	)
	check(
		testbrowser.NewDocument(chromedp.Navigate(origin+"/system")),
		chromedp.WaitVisible(chromedp.CSS("h1")),
	)
	assert(
		`document.body.textContent.includes('128.4 MiB (134637824 bytes)') && !document.body.textContent.includes('Operator workspaces')`,
		"status size presentation missing",
	)
	f.warehouseBytes.Store(268435456)
	check(
		chromedp.Poll[chromedp.Void](
			`document.body.textContent.includes('256.0 MiB (268435456 bytes)')`,
			chromedp.WithPollingTimeout(15*time.Second),
		),
	)

	for _, disabled := range []bool{false, true} {
		f.noGrid.Store(!disabled)

		document, closeDocument := chromedp.NewContext(ctx)

		step, stopStep := context.WithTimeout(document, 20*time.Second)
		err := chromedp.Do(
			step,
			testbrowser.Command(network.SetCacheDisabled, network.SetCacheDisabledParams{CacheDisabled: true}),
			testbrowser.Command(
				emulation.SetScriptExecutionDisabled,
				emulation.SetScriptExecutionDisabledParams{Value: disabled},
			),
			testbrowser.NewDocument(chromedp.Navigate(origin+"/test/grid")),
			chromedp.WaitVisible(chromedp.CSS("[data-grid]")),
		)

		var clear bool
		if err == nil {
			predicate := `!document.querySelector('table') && document.querySelector('[data-grid-status]').textContent.includes('could not load') && !document.querySelector('[data-grid-retry]').hidden`
			if disabled {
				predicate = `!document.querySelector('table') && document.querySelector('noscript').textContent.includes('JavaScript is required')`
			}

			err = chromedp.Do(step, testbrowser.Poll(predicate, &clear))
		}

		stopStep()
		closeDocument()

		if err != nil || !clear {
			t.Fatal("JS requirement or component retry was unclear", disabled, err, clear)
		}
	}
}
