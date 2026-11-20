// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/a-h/templ"
	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/source"
)

func TestOverviewPrioritizesActionAndKeepsReportContext(t *testing.T) {
	ref, err := source.NewReference("ibkr", "account", "", "one")
	if err != nil {
		t.Fatal(err)
	}

	details, err := application.NewAccountDetails("Synthetic account", "USD")
	if err != nil {
		t.Fatal(err)
	}

	account, err := application.NewAccount("account", "source", "ibkr-flex", ref, details, "synthetic")
	if err != nil {
		t.Fatal(err)
	}

	latest := application.ReportEntry{
		ProcessingStatus: application.ProcessingStatus{
			Artifact: "retained", Receipt: "latest", Stream: "confirmations", Complete: true,
			ReportedFrom: "2026-10-19", ReportedTo: "2026-10-20",
		},
		ConnectionName: "Synthetic source",
		Received:       time.Date(2026, 10, 21, 9, 5, 0, 1, time.UTC),
		Accepted:       1,
	}

	for _, tc := range []struct {
		name   string
		change func(*overviewView)
		title  string
		action string
	}{
		{"history progress outranks review", func(v *overviewView) {
			v.HistoryUpdate = application.HistoryUpdateStatus{Phase: "activity", Completed: 12}
			v.ReviewPage.Matched = 2
		}, "Updating retained history", "12 items processed"},
		{"featured issue outranks pending receipt", func(v *overviewView) {
			v.ReviewPage.Matched = 2
			v.Reviews = []reviewItem{{Label: "Buy SYN", Description: "Synthetic account", Reason: "Conflicting prices", Href: "/activity/fill"}}
			v.PendingReport = application.PendingReport{Artifact: "pending"}
		}, "2 items need your review", `href="/activity/fill">Buy SYN`},
		{"review list without a featured issue", func(v *overviewView) {
			v.ReviewPage.Matched = 1
		}, "1 item needs your review", `href="/review">Open Needs review`},
		{"report awaiting account authority", func(v *overviewView) {
			v.PendingReport = application.PendingReport{Artifact: "pending", ConnectionID: "source", ConnectionName: "Synthetic source"}
		}, "A report needs account confirmation", `href="/connections/source?view=status">Review account authority`},
		{"received report pending processing", func(v *overviewView) {
			v.PendingReport = application.PendingReport{Artifact: "pending", AccountConfirmed: true, ConnectionName: "Synthetic source"}
		}, "A report is awaiting processing", `href="/evidence/pending">Inspect retained report`},
		{"retained report processing failed", func(v *overviewView) {
			v.PendingReport = application.PendingReport{Artifact: "failed", AccountConfirmed: true, ProcessingFailed: true}
		}, "Report processing failed", `href="/evidence/failed">Inspect retained report`},
		{"warehouse paused", func(v *overviewView) {
			v.Storage.Paused = true
		}, "Synchronization is paused", `href="#synchronization">Inspect synchronization`},
		{"missing source access", func(v *overviewView) {
			v.Connections[0].MissingAccess = true
		}, "Source work needs attention", "Access review required"},
		{"terminal stream without failure text", func(v *overviewView) {
			v.Connections[0].Work = []application.WorkStatus{{Operation: "acquire", Scheduled: true, RetryBlocked: true}}
		}, "Source work needs attention", "Operator action required"},
		{"stream failure includes its reason", func(v *overviewView) {
			v.Connections[0].Work = []application.WorkStatus{{Operation: "acquire", Scheduled: true, Failure: "Source retry scheduled"}}
		}, "Source work needs attention", "Source retry scheduled"},
		{"reference issue", func(v *overviewView) {
			v.ReferenceFailure = "Reference access unavailable"
		}, "Processing or reference needs attention", `href="/connections/reference">Instrument reference access`},
		{"no connections", func(v *overviewView) {
			v.Account, v.Connections, v.Reports = application.Account{}, nil, nil
		}, "Connect your first account", `href="/connections/new">Add source connection`},
		{"connection without a confirmed account", func(v *overviewView) {
			v.Account, v.Reports = application.Account{}, nil
		}, "Confirm an account", `href="/connections">Review account discovery`},
		{"partial source list", func(v *overviewView) {
			v.MoreConnections = true
		}, "No financial review items found", `href="/connections">Inspect all connections`},
		{"no reports", func(v *overviewView) {
			v.Reports = nil
		}, "Waiting for the first report", `href="/connections">Inspect source status`},
		{"quiet reported account", func(v *overviewView) {
			v.PositionsAsOf = &application.PositionTime{Date: "2026-10-20", Basis: source.PositionBrokerEffective}
		}, "Nothing awaiting financial review", `<time datetime="2026-10-20">20 Oct 2026</time>`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			view := fixtureOverview(fixtureView{
				Account: account,
				Connections: []application.ConnectionView{
					{ID: "source", Name: "Synthetic source", CanAcquire: true},
				},
			})
			view.AccountCount = 1
			view.Reports = []application.ReportEntry{latest}
			tc.change(&view)

			html := renderReportTest(t, DashboardOverview("csrf", view, ""))
			for _, text := range []string{"<h1>" + tc.title + "</h1>", tc.action, `id="synchronization"`} {
				if !strings.Contains(html, text) {
					t.Errorf("Overview is missing %q", text)
				}
			}

			if len(view.Reports) > 0 {
				for _, text := range []string{`href="/evidence/retained?receipt=latest"`, "19–20 Oct 2026", "1 new entry"} {
					if !strings.Contains(html, text) {
						t.Errorf("Overview lost latest report detail %q", text)
					}
				}
			}
		})
	}
}

func TestOverviewListsEachFailedSourceOnce(t *testing.T) {
	failed := func(id, name string) application.ReportEntry {
		return application.ReportEntry{
			ProcessingStatus: application.ProcessingStatus{Failure: "Synthetic failure"},
			ConnectionID:     id,
			ConnectionName:   name,
			CanNormalize:     true,
		}
	}
	first := failed("source", "Synthetic source")
	second := failed("outside-one", "First source outside Overview")
	third := failed("outside-two", "Second source outside Overview")
	view := overviewView{Reports: []application.ReportEntry{first, first, second, second, third}}
	view.MoreConnections = true

	for _, withSourceIssue := range []bool{false, true} {
		if withSourceIssue {
			view.Connections = []application.ConnectionView{
				{
					ID:         "source",
					Name:       "Synthetic source",
					Processing: []application.ProcessingStatus{first.ProcessingStatus},
				},
			}
		}

		got := overviewIssues(view)
		if len(got) != 3 {
			t.Fatalf("with source issue %t, got %v", withSourceIssue, got)
		}

		for i, id := range []string{"source", "outside-one", "outside-two"} {
			if got[i].Href != connectionURL(id, "reports") ||
				got[i].Reason != "A recent report failed processing." {
				t.Fatalf("with source issue %t, got %v at position %d", withSourceIssue, got[i], i)
			}
		}
	}
}

func TestReportsKeepReceiptIdentityProcessingAndContributionSeparate(t *testing.T) {
	pageView := application.ReportList{
		PageInfo: application.PageInfo{Page: 1, PageSize: 5, Total: 6, Matched: 6},
		Items: []application.ReportEntry{
			{
				ProcessingStatus: application.ProcessingStatus{
					Artifact: "report-one", Receipt: "repeat + later", Stream: "confirmations", Complete: true,
					RequestedTo: "2026-10-20", ReportedFrom: "2026-10-19",
				},
				ConnectionName: "Synthetic source",
				Operation:      "supply",
				Received:       time.Date(2026, 10, 21, 9, 5, 0, 1, time.UTC),
				ReusedBody:     true,
				Accepted:       100,
			},
			{
				ProcessingStatus: application.ProcessingStatus{
					Artifact: "failed",
					Failure:  "failure",
					Stream:   "activities",
				},
				ProviderAvailable: true,
				CanNormalize:      true,
			},
			{
				ProcessingStatus:  application.ProcessingStatus{Artifact: "queued", Stream: "activities"},
				ProviderAvailable: true, CanNormalize: true,
			},
			{
				ProcessingStatus:  application.ProcessingStatus{Artifact: "retained", Stream: "activities"},
				ProviderAvailable: true,
			},
			{ProcessingStatus: application.ProcessingStatus{Artifact: "unavailable", Stream: "activities"}},
		},
	}

	request := pageRequest(httptest.NewRequest(http.MethodGet, "/reports?q=synthetic", nil))

	var out strings.Builder
	if err := ReportsPage("csrf", pageView).Render(request.Context(), &out); err != nil {
		t.Fatal(err)
	}

	for _, text := range []string{
		`href="/evidence/report-one?receipt=repeat+%2B+later"`,
		"2026-10-21T09:05:00.000000001Z", "· Supplied", "Reused a retained report", "Through 20 Oct 2026",
		"From 19 Oct 2026", "Processing failed", "Awaiting processing", "Evidence retained", "Provider unavailable",
		`action="/processing/failed/retry"`, `name="return" value="reports"`, `value="synthetic"`, "Clear search", "Next page",
	} {
		if !strings.Contains(out.String(), text) {
			t.Errorf("Reports is missing %q", text)
		}
	}

	if strings.Contains(out.String(), "100 new entries") ||
		strings.Contains(out.String(), "/processing/queued/retry") {
		t.Fatal("reused or pending evidence was presented as a new contribution or failure")
	}
}

func TestReportContributionSummarizesExactCounts(t *testing.T) {
	got := reportContribution(application.ReportEntry{
		ProcessingStatus: application.ProcessingStatus{Complete: true, NeedsReview: 1, ExcludedRows: 3},
		Accepted:         2,
		Enriched:         1,
		Repeated:         4,
		PositionSets:     1,
	})
	want := "2 new entries · 1 entry added detail · 4 entries already reported · 1 entry needs review · 1 reported position set · 3 excluded rows"
	if got != want {
		t.Fatalf("report contribution = %q, want %q", got, want)
	}
}

func TestReportEmptyMessageDistinguishesProcessingFromMissingData(t *testing.T) {
	for _, tc := range []struct {
		processing application.ReportProcessing
		want       string
	}{
		{application.ReportProcessingComplete, "No processed activity entries in this report."},
		{application.ReportProcessingFailed, "Processing failed. The original report is available."},
		{application.ReportProcessingPending, "Awaiting processing. The original report is available."},
		{application.ReportProcessingUnavailable, "The original report is available. Processing is unavailable for this source."},
	} {
		if got := reportEmptyMessage(tc.processing); got != tc.want {
			t.Errorf("empty report message = %q, want %q", got, tc.want)
		}
	}
}

func TestReportPeriodPreservesProviderCoverage(t *testing.T) {
	for _, tc := range []struct{ from, to, want string }{
		{"", "", "Not provided"},
		{"", "2026-10-20", "Through 20 Oct 2026"},
		{"2026-10-19", "", "From 19 Oct 2026"},
		{"2026-10-20", "2026-10-20", "20 Oct 2026"},
		{"2026-10-19", "2026-10-20", "19–20 Oct 2026"},
		{"2026-09-30", "2026-10-01", "30 Sep–1 Oct 2026"},
		{"2025-12-31", "2026-01-01", "31 Dec 2025 – 1 Jan 2026"},
		{"provider text", "", "From provider text"},
	} {
		if got := reportPeriod(tc.from, tc.to); got != tc.want {
			t.Errorf("report period %q to %q = %q, want %q", tc.from, tc.to, got, tc.want)
		}
	}
}

func TestOverviewPositionWordingDistinguishesEffectiveTimeFromRetrieval(t *testing.T) {
	at := "2026-10-20T14:15:16.000000001Z"
	for _, tc := range []struct {
		view         overviewView
		label, value string
	}{
		{overviewView{PositionsAsOf: &application.PositionTime{At: &at, Basis: source.PositionSourceFetched}}, "Positions fetched", at},
		{overviewView{PositionsAvailable: 0}, "Reported positions", "No available position set"},
		{overviewView{PositionsAvailable: 1}, "Reported positions", "Available · effective time unknown"},
		{
			overviewView{sourceView: sourceView{Prerequisites: application.Prerequisites{AccountCount: 2}}, PositionsAvailable: 1},
			"Reported positions", "Available for 1 of 2 accounts",
		},
	} {
		if got := overviewPositionsLabel(tc.view); got != tc.label {
			t.Errorf("positions label = %q, want %q", got, tc.label)
		}

		if html := renderReportTest(t, OverviewPositionLink(tc.view)); !strings.Contains(html, tc.value) {
			t.Errorf("position link is missing %q", tc.value)
		}
	}
}

func renderReportTest(t *testing.T, c templ.Component) string {
	t.Helper()

	var out strings.Builder
	if err := c.Render(t.Context(), &out); err != nil {
		t.Fatal(err)
	}

	return out.String()
}
