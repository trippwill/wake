// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package admin

import (
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/source"
)

type overviewPriority struct {
	Title, Note, Tone, Action, Href string
}

func overviewVerdict(view overviewView, issues []reviewItem) overviewPriority {
	if view.HistoryUpdate.Phase != "" {
		return overviewPriority{
			Title: "Updating retained history",
			Note:  "Financial views will return when the update is complete.",
			Tone:  "attention",
		}
	}

	count := view.ReviewPage.Matched
	if count > 0 {
		title := "1 item needs your review"
		if count > 1 {
			title = fmt.Sprintf("%d items need your review", count)
		}

		if len(view.Reviews) > 0 {
			item := view.Reviews[0]

			return overviewPriority{
				Title: title, Note: describe(item.Description, item.Reason),
				Tone: "attention", Action: item.Label, Href: item.Href,
			}
		}

		return overviewPriority{Title: title, Tone: "attention", Action: "Open Needs review", Href: "/review"}
	}

	if report := view.PendingReport; report.Artifact != "" {
		if !report.AccountConfirmed {
			return overviewPriority{
				"A report needs account confirmation",
				report.ConnectionName,
				"attention",
				"Review account authority",
				connectionURL(report.ConnectionID, "status"),
			}
		}

		if report.ProcessingFailed {
			return overviewPriority{
				"Report processing failed",
				"Retained report from " + report.ConnectionName + ".",
				"attention",
				"Inspect retained report",
				reportURL(report.Artifact),
			}
		}

		return overviewPriority{
			"A report is awaiting processing",
			"Received from " + report.ConnectionName + ".",
			"pending",
			"Inspect retained report",
			reportURL(report.Artifact),
		}
	}

	if view.Storage.Paused {
		return overviewPriority{
			"Synchronization is paused",
			"Retained history remains available.",
			"attention",
			"Inspect synchronization",
			"#synchronization",
		}
	}

	for _, c := range view.Connections {
		blocker := sourceSyncBlocker(c, false)
		if blocker.Label != "" {
			return overviewPriority{
				"Source work needs attention", describe(c.Name, blocker.Label), "attention",
				"Inspect synchronization", "#synchronization",
			}
		}

		for _, work := range sourceWork(c) {
			if work.Scheduled && (work.RetryBlocked || work.Failure != "") {
				note := "Operator action required"
				if work.Failure != "" {
					note = work.Failure
				}

				return overviewPriority{
					"Source work needs attention", describe(c.Name, note), "attention",
					"Inspect synchronization", "#synchronization",
				}
			}
		}
	}

	if len(issues) != 0 {
		return overviewPriority{
			"Processing or reference needs attention",
			"",
			"attention",
			"View issues",
			"#overview-attention-title",
		}
	}

	if view.Account.ID() == "" {
		if len(view.Connections) == 0 {
			return overviewPriority{
				"Connect your first account",
				"Add a source to begin collecting financial reports.",
				"quiet",
				"Add source connection",
				"/connections/new",
			}
		}

		return overviewPriority{
			"Confirm an account",
			"Review the discovered accounts before automatic synchronization.",
			"attention",
			"Review account discovery",
			"/connections",
		}
	}

	if view.MoreConnections {
		return overviewPriority{
			"No financial review items found",
			"Overview shows synchronization for the first 50 connections.",
			"pending",
			"Inspect all connections",
			"/connections",
		}
	}

	if len(view.Reports) == 0 {
		return overviewPriority{
			Title:  "Waiting for the first report",
			Tone:   "quiet",
			Action: "Inspect source status",
			Href:   "/connections",
		}
	}

	return overviewPriority{Title: "Nothing awaiting financial review", Tone: "quiet"}
}

func overviewIssues(view overviewView) []reviewItem {
	issues := reportAndReferenceIssues(view.sourceView)
	for _, report := range view.Reports {
		if !report.CanNormalize || report.Complete || report.Failure == "" {
			continue
		}

		href := connectionURL(report.ConnectionID, "reports")
		if slices.ContainsFunc(issues, func(item reviewItem) bool { return item.Href == href }) {
			continue
		}

		issues = append(issues, reviewItem{
			Label:  "Processing · " + report.ConnectionName,
			Reason: "A recent report failed processing.",
			Href:   href,
		})
	}

	return issues
}

func overviewPositionsLabel(view overviewView) string {
	if view.PositionsAsOf == nil {
		return "Reported positions"
	}

	if view.PositionsAsOf.Basis == source.PositionSourceFetched {
		return "Positions fetched"
	}

	return "Positions as of"
}

func reportURL(artifact string) string { return "/evidence/" + url.PathEscape(artifact) }

func reportReceiptURL(report application.ReportEntry) string {
	return reportURL(report.Artifact) + "?receipt=" + url.QueryEscape(report.Receipt)
}

func reportEmptyMessage(processing application.ReportProcessing) string {
	switch processing {
	case application.ReportProcessingComplete:
		return "No processed activity entries in this report."
	case application.ReportProcessingFailed:
		return "Processing failed. The original report is available."
	case application.ReportProcessingPending:
		return "Awaiting processing. The original report is available."
	default:
		return "The original report is available. Processing is unavailable for this source."
	}
}

func reportState(report application.ReportEntry) string {
	switch {
	case report.Complete:
		return "Processed"
	case !report.ProviderAvailable:
		return "Provider unavailable"
	case !report.CanNormalize:
		return "Evidence retained"
	case report.Failure != "":
		return "Processing failed"
	default:
		return "Awaiting processing"
	}
}

func reportContribution(report application.ReportEntry) string {
	if report.ReusedBody {
		return "Reused a retained report"
	}

	if !report.Complete {
		return ""
	}

	parts := []string{}
	for _, fact := range []struct {
		count        int64
		one, several string
	}{
		{report.Accepted, "new entry", "new entries"},
		{report.Enriched, "entry added detail", "entries added detail"},
		{report.Repeated, "entry already reported", "entries already reported"},
		{report.NeedsReview, "entry needs review", "entries need review"},
		{report.PositionSets, "reported position set", "reported position sets"},
		{report.ExcludedRows, "excluded row", "excluded rows"},
	} {
		if fact.count == 1 {
			parts = append(parts, "1 "+fact.one)
		} else if fact.count > 1 {
			parts = append(parts, fmt.Sprintf("%d %s", fact.count, fact.several))
		}
	}

	return strings.Join(parts, " · ")
}

func reportPeriod(from, to string) string {
	switch {
	case from == "" && to == "":
		return "Not provided"
	case from == "":
		return "Through " + reportDate(to)
	case to == "":
		return "From " + reportDate(from)
	case from == to:
		return reportDate(from)
	default:
		start, startErr := time.Parse(time.DateOnly, from)
		end, endErr := time.Parse(time.DateOnly, to)
		if startErr == nil && endErr == nil && start.Year() == end.Year() {
			layout := "2 Jan"
			if start.Month() == end.Month() {
				layout = "2"
			}

			return start.Format(layout) + "–" + end.Format("2 Jan 2006")
		}

		return reportDate(from) + " – " + reportDate(to)
	}
}

func reportDate(value string) string {
	parsed, err := time.Parse(time.DateOnly, value)
	if err != nil {
		return value
	}

	return parsed.Format("2 Jan 2006")
}
