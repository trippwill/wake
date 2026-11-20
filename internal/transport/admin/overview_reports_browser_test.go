// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

//go:build browser

package admin

import (
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/testbrowser"
	"go.trippwill.dev/wake/source"
)

func morningOverview(v fixtureView, scenario string) overviewView {
	ref, _ := source.NewReference("synthetic", "account", "", "one")
	details, _ := application.NewAccountDetails("Long-term investments", "USD")
	v.Account, _ = application.NewAccount(
		"synthetic-account",
		v.Connections[0].ID,
		"synthetic",
		ref,
		details,
		"fixture",
	)
	view := fixtureOverview(v)
	view.AccountCount = 1
	view.Connections[0].Name = "Synthetic brokerage"
	view.Connections[0].CanAcquire = true
	view.Connections[0].Work = []application.WorkStatus{
		{
			Operation:     "acquire",
			Stream:        "confirmations",
			Scheduled:     true,
			Progress:      "retained",
			Next:          time.Now().UTC().Add(time.Hour),
			LastRetrieved: time.Date(2026, 10, 21, 9, 5, 0, 1, time.UTC),
		},
	}
	view.PositionsAsOf = &application.PositionTime{Date: "2026-10-18", Basis: source.PositionBrokerEffective}

	const reportDay = "2026-10-20"

	view.Reports = []application.ReportEntry{
		{
			ProcessingStatus: application.ProcessingStatus{
				Artifact: "synthetic-report", Stream: "confirmations", Receipt: "repeated-receipt",
				Complete: true, ReportedFrom: reportDay, ReportedTo: reportDay,
			},
			ConnectionName: "Synthetic brokerage",
			ConnectionID:   v.Connections[0].ID,
			Operation:      "acquire",
			Received: time.Date(
				2026,
				10,
				21,
				9,
				5,
				0,
				1,
				time.UTC,
			),
			ReusedBody:        true,
			CanNormalize:      true,
			ProviderAvailable: true,
		},
		{
			ProcessingStatus: application.ProcessingStatus{
				Artifact:      "synthetic-report",
				Stream:        "confirmations",
				Receipt:       "first-receipt",
				Complete:      true,
				ReportedFrom:  "2026-10-19",
				ReportedTo:    reportDay,
				RequestedFrom: "2026-10-18",
				RequestedTo:   reportDay,
			},
			ConnectionName: "Synthetic brokerage",
			ConnectionID:   v.Connections[0].ID,
			Operation:      "acquire",
			Received: time.Date(
				2026,
				10,
				21,
				8,
				40,
				0,
				0,
				time.UTC,
			),
			Accepted:          2,
			Repeated:          4,
			CanNormalize:      true,
			ProviderAvailable: true,
		},
		{
			ProcessingStatus: application.ProcessingStatus{
				Artifact: "synthetic-positions", Stream: "positions", Receipt: "positions-receipt", Complete: true,
				ReportedFrom: reportDay, ReportedTo: reportDay,
			},
			ConnectionName: "Synthetic brokerage",
			ConnectionID:   v.Connections[0].ID,
			Operation:      "acquire",
			Received: time.Date(
				2026,
				10,
				20,
				19,
				12,
				0,
				0,
				time.UTC,
			),
			PositionSets:      1,
			CanNormalize:      true,
			ProviderAvailable: true,
		},
	}
	if scenario == "attention" || scenario == "pending" || scenario == "failed" {
		view.PendingReport = application.PendingReport{
			Artifact:         "synthetic-pending",
			ConnectionID:     v.Connections[0].ID,
			ConnectionName:   "Synthetic brokerage",
			AccountConfirmed: true,
		}
		view.Reports[0].Artifact = "synthetic-pending"
		view.Reports[0].Receipt = "pending-receipt"
		view.Reports[0].Complete = false
		view.Reports[0].ReusedBody = false
		view.Reports[0].Stream = "activities"
		view.Reports[0].ReportedFrom = ""
		view.Reports[0].ReportedTo = ""

		if scenario == "failed" {
			view.PendingReport.ProcessingFailed = true
			view.Reports[0].Failure = "processing failed; retained evidence is safe"
		}
	}

	if scenario == "attention" {
		view.ReviewPage.Matched = 1
		view.Reviews = []reviewItem{
			{
				Label:       "Buy Sable Fund",
				Description: "Long-term investments · 20 Oct 2026",
				Reason:      "Conflicting prices reported",
				Href:        "/activity/synthetic-activity",
			},
		}
	}

	return view
}

func TestBrowserMorningOverviewAndReports(t *testing.T) {
	f := newAdminBrowserFixture(t)
	for _, scenario := range []struct{ name, tone, title, contribution, period string }{
		{"quiet", "quiet", "Nothing awaiting financial review", "Reused a retained report", "20 Oct 2026"},
		{"attention", "attention", "1 item needs your review", "Awaiting processing", "Not provided"},
	} {
		f.check(
			testbrowser.NewDocument(chromedp.Navigate(f.origin+"/?fixture="+scenario.name)),
			chromedp.WaitVisible(chromedp.CSS(".wake-overview-report")),
		)

		for _, theme := range []string{"light", "dark"} {
			for _, width := range []int64{1280, 390, 320} {
				f.check(
					testbrowser.SetViewport(width, 920),
					chromedp.Evaluate[chromedp.Void](
						`document.documentElement.dataset.theme=`+strconv.Quote("wake-"+theme),
					),
				)
				f.assert(fmt.Sprintf(`(()=>{
const report=document.querySelector('.wake-overview-report');
const verdict=document.querySelector('.wake-verdict');
const sync=document.querySelector('#synchronization-title');
const context=document.querySelector('.wake-overview-context');
return document.documentElement.scrollWidth<=innerWidth+1 &&
verdict.dataset.tone===%q &&
document.querySelector('h1').textContent.trim()===%q &&
context.textContent.includes('Long-term investments') &&
context.textContent.includes('Positions as of') &&
context.querySelector('time').dateTime==='2026-10-18' &&
report.querySelector('time').dateTime==='2026-10-21T09:05:00.000000001Z' &&
report.textContent.includes('Reported period') && report.textContent.includes(%q) && report.textContent.includes(%q) &&
sync.getBoundingClientRect().top < innerHeight &&
(innerWidth>704 ? report.getBoundingClientRect().left>=verdict.getBoundingClientRect().right : report.getBoundingClientRect().top>=verdict.getBoundingClientRect().bottom);
})()`, scenario.tone, scenario.title, scenario.contribution, scenario.period), "Overview lost its receipt timeline, context or priority")

				if width != 320 {
					f.screenshot(fmt.Sprintf("overview-%s-%s-%d", scenario.name, theme, width))
				}
			}
		}
	}

	f.check(testbrowser.NewDocument(chromedp.Navigate(f.origin + "/?fixture=attention")))
	f.assert(
		`document.querySelector('.wake-verdict-copy a').getAttribute('href')==='/activity/synthetic-activity' &&
document.querySelector('.wake-verdict-copy a').textContent==='Buy Sable Fund' &&
document.querySelector('.wake-verdict-copy p').textContent.includes('Conflicting prices reported')`,
		"Priority did not identify the affected activity",
	)
	f.check(
		testbrowser.NewDocument(chromedp.Navigate(f.origin+"/?fixture=pending")),
		chromedp.WaitVisible(chromedp.CSS(`.wake-verdict[data-tone="pending"]`)),
	)
	f.assert(
		`document.querySelector('h1').textContent==='A report is awaiting processing' && document.querySelector('.wake-verdict-copy p').textContent.includes('Synthetic brokerage')`,
		"Pending processing lacked its own status and source",
	)

	f.check(testbrowser.NewDocument(chromedp.Navigate(f.origin + "/?fixture=failed")))
	f.assert(
		`document.querySelector('h1').textContent==='Report processing failed' &&
document.querySelector('.wake-verdict-copy a').getAttribute('href')==='/evidence/synthetic-pending' &&
document.querySelector('.wake-overview-report-result').textContent.trim()==='Processing failed'`,
		"Processing failure was described as awaiting processing",
	)

	f.check(testbrowser.SetViewport(1280, 920),
		testbrowser.NewDocument(chromedp.Click(chromedp.CSS(".wake-report-heading a[href='/reports']"))),
		chromedp.WaitVisible(chromedp.CSS("#reports-search")),
	)
	f.assert(`document.querySelector('h1').textContent==='Reports' &&
document.querySelector('.wake-report-entry:nth-child(2) .wake-report-period').textContent.includes('18–20 Oct 2026')`,
		"Requested periods were missing from Reports")
	f.screenshot("reports-index-light-1280")
	f.check(testbrowser.NewDocument(chromedp.Click(chromedp.CSS(".wake-report-entry h3 a"))),
		chromedp.WaitVisible(chromedp.CSS("[data-return]")),
	)
	f.assert(
		`location.pathname==='/evidence/synthetic-report' && new URLSearchParams(location.search).get('receipt')==='repeated-receipt' &&
document.querySelector('[data-selected-receipt]').textContent.includes('2026-10-21T09:05:00.000000001Z') &&
document.querySelector('[data-return]').getAttribute('href')==='/reports'`,
		"Report detail lost its receipt identity or archive return path",
	)
}

func TestBrowserReportGridEmptyStates(t *testing.T) {
	f := newAdminBrowserFixture(t)
	placeholderIs := func(message string) string {
		return fmt.Sprintf(
			`document.querySelector('#observations-grid .tabulator-placeholder')?.textContent.trim()===%q && document.querySelectorAll('#observations-grid .tabulator-row').length===0`,
			message,
		)
	}

	for _, report := range []struct{ id, message string }{
		{"synthetic-pending", "Awaiting processing. The original report is available."},
		{"synthetic-positions", "No processed activity entries in this report."},
	} {
		f.check(
			testbrowser.NewDocument(chromedp.Navigate(f.origin+"/evidence/"+report.id)),
			chromedp.WaitVisible(chromedp.CSS("#observations-grid .tabulator-placeholder")),
			chromedp.Poll[chromedp.Void](placeholderIs(report.message)),
		)
	}

	f.check(
		testbrowser.NewDocument(chromedp.Navigate(f.origin+"/evidence/synthetic-report")),
		chromedp.Poll[chromedp.Void](
			`document.querySelector('#observations-grid [data-grid-status]').textContent==='75 of 75 results'`,
		),
		chromedp.SetValue(chromedp.CSS("#observation-search"), "unmatched report entry"),
		chromedp.Click(chromedp.CSS("[data-observation-filters] button")),
		chromedp.WaitVisible(chromedp.CSS("#observations-grid .tabulator-placeholder")),
		chromedp.Poll[chromedp.Void](
			placeholderIs("No matching report entries.")+
				` && document.querySelector('#observations-grid [data-grid-status]').textContent==='0 of 75 results'`,
		),
	)
}
