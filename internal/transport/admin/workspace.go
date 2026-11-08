// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/a-h/templ"
	"go.trippwill.dev/wake/financial"
	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/provider"
	"go.trippwill.dev/wake/source"
)

func provided(value string) string {
	if value == "" {
		return "Not provided"
	}

	return value
}

func readableJSON(value string) string {
	var formatted bytes.Buffer
	if json.Indent(&formatted, []byte(value), "", "  ") != nil {
		return value
	}

	return formatted.String()
}

func providedDate(value financial.Date) string {
	if value.IsZero() {
		return "Not provided"
	}

	return value.String()
}

func identifierLabel(kind source.IdentifierKind) string {
	switch kind {
	case source.UnclassifiedFIGI:
		return "Reported FIGI"
	case source.VenueFIGI:
		return "Venue FIGI"
	case source.CompositeFIGI:
		return "Composite FIGI"
	case source.ShareClassFIGI:
		return "Share-class FIGI"
	case source.InstrumentFIGI:
		return "Instrument FIGI"
	default:
		return strings.ToUpper(string(kind))
	}
}

func identityFIGILabel(allocation string) string {
	switch allocation {
	case "share_class":
		return identifierLabel(source.ShareClassFIGI)
	case "composite":
		return identifierLabel(source.CompositeFIGI)
	case "instrument":
		return identifierLabel(source.InstrumentFIGI)
	case "venue":
		return identifierLabel(source.VenueFIGI)
	default:
		return "Reference FIGI"
	}
}

type (
	pageKey   struct{}
	pageState struct {
		URL      string
		Query    url.Values
		Fragment bool
		Accounts []application.Account
		Method   string
	}
)

func page(ctx context.Context) pageState {
	if p, ok := ctx.Value(pageKey{}).(pageState); ok {
		return p
	}

	return pageState{URL: "/", Query: url.Values{}}
}

func withAccounts(r *http.Request, accounts []application.Account) *http.Request {
	p := page(r.Context())
	p.Accounts = accounts

	return r.WithContext(context.WithValue(r.Context(), pageKey{}, p))
}

func pageRequest(r *http.Request) *http.Request {
	return r.WithContext(
		context.WithValue(
			r.Context(),
			pageKey{},
			pageState{
				URL:      r.URL.RequestURI(),
				Query:    r.URL.Query(),
				Fragment: r.Header.Get("HX-Request") == "true",
				Method:   r.Method,
			},
		),
	)
}

func workspaceURL(ctx context.Context) string {
	u, _ := url.Parse(page(ctx).URL)
	if u == nil {
		return "/activity"
	}

	switch {
	case u.Path == "/":
		return "/"
	case u.Path == "/positions":
		return "/positions"
	case u.Path == "/review":
		return "/review"
	case u.Path == "/instruments" || strings.HasPrefix(u.Path, "/instruments/"):
		return "/instruments"
	case strings.HasPrefix(u.Path, "/connections"):
		return "/connections"
	case u.Path == "/system" || strings.HasPrefix(u.Path, "/system/"):
		return "/system"
	}

	return "/activity"
}

func returnURL(ctx context.Context) string {
	u, _ := url.Parse(page(ctx).URL)
	if u != nil && strings.HasSuffix(u.Path, "/correct") {
		return strings.TrimSuffix(u.Path, "/correct")
	}

	return workspaceURL(ctx)
}

func parentLabel(ctx context.Context) string {
	p := strings.Split(returnURL(ctx), "?")[0]
	switch {
	case p == "/positions":
		return "Positions"
	case p == "/activity":
		return "Activity"
	case p == "/instruments":
		return "Instruments"
	case p == "/review":
		return "Needs review"
	case strings.HasPrefix(p, "/instruments/"):
		return "Instrument"
	case strings.HasPrefix(p, "/evidence/") || strings.HasPrefix(p, "/reference-evidence/"):
		return "Supporting evidence"
	case strings.HasPrefix(p, "/connections"):
		return "Connections"
	case p == "/system" || strings.HasPrefix(p, "/system/"):
		return "System"
	default:
		return "Activity detail"
	}
}

func activeView(ctx context.Context, fallback string) string {
	if v := page(ctx).Query.Get("view"); v != "" {
		return v
	}

	return fallback
}

func correctionMode(ctx context.Context, f application.CorrectionForm) string {
	if f.Message != "" {
		if f.Draft.Action == "release" {
			return "decisions"
		}

		if f.Draft.Scope == "reference" || f.Draft.Scope == "observation" {
			return "evidence"
		}

		return "field"
	}

	switch page(ctx).Query.Get("mode") {
	case "evidence":
		return "evidence"
	case "decisions":
		return "decisions"
	default:
		return "field"
	}
}

func modeURL(ctx context.Context, mode string) string {
	u, _ := url.Parse(page(ctx).URL)
	if u == nil {
		return "?mode=" + mode
	}

	q := url.Values{"mode": {mode}}
	if field := page(ctx).Query.Get("field"); field != "" {
		q.Set("field", field)
	}

	u.RawQuery = q.Encode()

	return u.String()
}

func selectedCandidate(ctx context.Context, f application.CorrectionForm) string {
	if f.Draft.Scope == "reference" || f.Draft.Scope == "observation" {
		return f.Draft.Value
	}

	return page(ctx).Query.Get("candidate")
}

func fieldGroup(field string) string {
	switch {
	case field == "instrument" || field == "listing" || field == "underlier":
		return "Identity and links"
	case strings.HasPrefix(field, "option.") || strings.HasPrefix(field, "debt."):
		return "Contract terms"
	case strings.Contains(field, "date") || field == "executed_at":
		return "Dates and time"
	default:
		return "Financial terms"
	}
}

func hasDifferences(f application.CorrectionForm, c application.CorrectionCandidate) bool {
	for _, field := range comparisonFields(f, c) {
		if v, ok := c.Fields[field]; ok && f.Values[field] != v {
			return true
		}
	}

	return false
}

type reviewItem struct{ Key, Kind, Label, Description, Reason, Href string }

func sourceWork(c application.ConnectionView) []application.WorkStatus {
	operation := "discover"
	if c.Account.ID() != "" && c.CanAcquire {
		operation = "acquire"
	}

	out := []application.WorkStatus{}
	for _, w := range c.Work {
		if w.Operation == operation && w.Scheduled {
			out = append(out, w)
		}
	}

	return out
}

func repairs(v sourceView) []reviewItem {
	out := []reviewItem{}
	if v.Storage.Paused {
		out = append(
			out,
			reviewItem{
				Label:  "Review restored access",
				Reason: "Upstream work is paused.",
				Href:   "/connections/recovery",
			},
		)
	}

	for _, c := range v.Connections {
		if c.MissingAccess {
			out = append(
				out,
				reviewItem{
					Label:  c.Name,
					Reason: "Source settings or credentials are missing.",
					Href:   connectionURL(c.ID, "access"),
				},
			)
		}

		for _, w := range sourceWork(c) {
			if w.Failure != "" {
				out = append(out, reviewItem{Label: c.Name, Reason: w.Failure, Href: connectionURL(c.ID, "status")})
				break
			}
		}

		for _, p := range c.Processing {
			if p.Failure != "" {
				out = append(
					out,
					reviewItem{
						Label:  "Processing · " + c.Name,
						Reason: "A recent report failed processing.",
						Href:   connectionURL(c.ID, "reports"),
					},
				)

				break
			}
		}
	}

	if v.ReferenceFailure != "" {
		out = append(
			out,
			reviewItem{
				Label:  "Instrument reference access",
				Reason: v.ReferenceFailure,
				Href:   "/connections/reference",
			},
		)
	}

	return out
}
func repairCount(v sourceView) int { return len(repairs(v)) }

func stringTitle(s string) string {
	if s == "" {
		return "Unknown"
	}

	return strings.ToUpper(s[:1]) + s[1:]
}

type activityItem struct {
	ID, Href, Label, Date, Kind, Instrument, Description, InstrumentID, Reference, Quantity, Amount, Price, Basis, State, Realized string
	AccountName                                                                                                                    string
	RealizedConflict                                                                                                               bool
	FinancialState                                                                                                                 application.ActivityState
}

func connectionByID(v sourceView, id string) (application.ConnectionView, bool) {
	for _, c := range v.Connections {
		if c.ID == id {
			return c, true
		}
	}

	return application.ConnectionView{}, false
}

func connectionURL(id, view string) string {
	return "/connections/" + url.PathEscape(id) + "?view=" + view
}

func connectionFailure(c application.ConnectionView) bool {
	if c.MissingAccess {
		return true
	}

	for _, w := range sourceWork(c) {
		if w.Failure != "" || w.Progress == provider.ProgressUnavailable {
			return true
		}
	}

	return false
}

func taskURL(ctx context.Context, path string) string {
	u, err := url.Parse(path)
	if err != nil {
		return path
	}

	q := u.Query()

	for _, key := range []string{"mode", "candidate"} {
		if v := page(ctx).Query.Get(key); v != "" {
			q.Set(key, v)
		}
	}

	u.RawQuery = q.Encode()

	return u.String()
}

func confirmURL(id string, c application.Candidate) string {
	ref := c.Candidate.Reference()
	query := url.Values{
		"artifact":  {c.Artifact},
		"account":   {ref.Value()},
		"authority": {ref.Authority()},
		"kind":      {ref.Kind()},
		"scope":     {ref.Scope()},
	}

	return "/connections/" + id + "/confirm?" + query.Encode()
}

func readiness(ok bool) string {
	if ok {
		return "Stored"
	}

	return "Missing"
}

func recoveryReady(v sourceView) bool {
	if v.Storage.ReferenceReview || v.MissingRecoveryAccess {
		return false
	}

	for _, c := range v.Connections {
		if c.MissingAccess {
			return false
		}
	}

	return true
}

func resetListURL(instruments bool) string {
	if instruments {
		return "/instruments"
	}

	return "/activity"
}

func streamAttributes(csrf string) templ.Attributes {
	if csrf == "" {
		return templ.Attributes{}
	}

	return templ.Attributes{"sse-connect": "/events"}
}

func correctionModeLabel(mode string) string {
	switch mode {
	case "evidence":
		return "Compare retained evidence"
	case "decisions":
		return "Manage decisions"
	default:
		return "Correct a field/link"
	}
}

func candidateScope(f application.CorrectionForm, candidate application.CorrectionCandidate) string {
	if candidate.Scope != "" {
		return candidate.Scope
	}

	if f.Kind == "instrument" {
		return "reference"
	}

	return "observation"
}

func activeDecisions(f application.CorrectionForm) int {
	n := 0
	for _, d := range f.Decisions {
		if d.Active {
			n++
		}
	}

	return n
}

func systemView(path string) string {
	if path == "/system/configuration" {
		return "configuration"
	}

	return "status"
}

func servePageError(w http.ResponseWriter, r *http.Request, csrf string, status int, title, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_ = SectionShell(title, csrf, "", ErrorPage(title, message)).Render(r.Context(), w)
}

func workspaceLabel(ctx context.Context) string {
	p := workspaceURL(ctx)
	if p == "/positions" {
		return "Positions"
	}

	if p == "/instruments" {
		return "Instruments"
	}

	if p == "/review" {
		return "Needs review"
	}

	if p == "/" {
		return "Overview"
	}

	if strings.HasPrefix(p, "/connections") {
		return "Connections"
	}

	if p == "/system" || strings.HasPrefix(p, "/system/") {
		return "System"
	}

	return "Activity"
}

func workspaceAttributes(ctx context.Context, csrf string) templ.Attributes {
	u, err := url.Parse(page(ctx).URL)
	if csrf == "" || page(ctx).Method == http.MethodPost || err != nil || u.Path == "/system/settings" ||
		strings.HasSuffix(u.Path, "/correct") ||
		strings.HasPrefix(u.Path, "/evidence/") ||
		strings.HasPrefix(u.Path, "/reference-evidence/") {
		return templ.Attributes{}
	}

	// Financial grids refresh through their JSON API. Accepted details are explicit
	// page-load snapshots, leaving disclosures and inspected terms stable.
	if u.Path == "/positions" || u.Path == "/activity" || u.Path == "/instruments" ||
		strings.HasPrefix(u.Path, "/executions/") ||
		strings.HasPrefix(u.Path, "/instruments/") ||
		strings.HasPrefix(u.Path, "/activity/") {
		return templ.Attributes{}
	}

	return templ.Attributes{
		"hx-get":     page(ctx).URL,
		"hx-trigger": "sse:refresh, every 10s",
		"hx-target":  "this",
		"hx-select":  "#workspace-content",
		"hx-swap":    "outerHTML",
		"hx-sync":    "this:queue last",
	}
}

func correctionContext(f application.CorrectionForm) string {
	if f.Kind == "activity" && f.Values["instrument"].Label != "" {
		return f.Values["instrument"].Label + " · " + f.Label
	}

	return f.Label
}

// Comparison includes retained terms independently of editable correction scopes.
func comparisonFields(f application.CorrectionForm, c application.CorrectionCandidate) []string {
	fields := append([]string{}, f.Fields...)
	seen := make(map[string]bool, len(fields))
	for _, field := range fields {
		seen[field] = true
	}

	extra := []string{}
	for field := range c.Fields {
		if !seen[field] {
			extra = append(extra, field)
		}
	}

	sort.Strings(extra)

	return append(fields, extra...)
}

func workLabel(work application.WorkStatus) string {
	if work.Stream == "" {
		return operationLabel(work.Operation)
	}

	return work.Stream + " · " + operationLabel(work.Operation)
}

func activityWorkspace(ctx context.Context) string {
	u, _ := url.Parse(page(ctx).URL)
	if u != nil {
		switch u.Path {
		case "/instruments":
			return "instruments"
		case "/review":
			return "review"
		}
	}

	return "all"
}

func activityTitle(view string) string {
	switch view {
	case "instruments":
		return "Instruments"
	case "review":
		return "Needs review"
	default:
		return "Activity"
	}
}

func activityDescription(view string) string {
	switch view {
	case "instruments":
		return "Inspect instrument identity, reported symbols and supporting observations."
	case "review":
		return "Resolve financial disagreements and uncertain instrument identity."
	default:
		return "Inspect financial history and its retained evidence."
	}
}

func workspaceSection(ctx context.Context, fallback string) string {
	if fallback == "Activity" {
		return workspaceLabel(ctx)
	}

	return fallback
}

func warehouseSizeText(bytes *int64) string {
	if bytes == nil || *bytes < 0 {
		return "Unavailable"
	}

	if *bytes < 1024 {
		return fmt.Sprintf("%d B (%d bytes)", *bytes, *bytes)
	}

	value := float64(*bytes)
	unit := "B"
	for _, next := range []string{"KiB", "MiB", "GiB", "TiB", "PiB", "EiB"} {
		value /= 1024
		unit = next

		if value < 1024 {
			break
		}
	}

	return fmt.Sprintf("%.1f %s (%d bytes)", value, unit, *bytes)
}
