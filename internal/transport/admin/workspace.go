// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package admin

import (
	"context"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"

	"github.com/a-h/templ"
	"go.trippwill.dev/wake/internal/application"
)

type (
	pageKey   struct{}
	pageState struct {
		URL      string
		Query    url.Values
		Fragment bool
		Method   string
	}
)

func page(ctx context.Context) pageState {
	if p, ok := ctx.Value(pageKey{}).(pageState); ok {
		return p
	}

	return pageState{URL: "/", Query: url.Values{}}
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

func localDestination(raw string) string {
	if len(raw) > 2048 || !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") ||
		strings.ContainsAny(raw, "\\\r\n") {
		return ""
	}

	u, err := url.Parse(raw)
	if err != nil || u.Host != "" || u.Scheme != "" {
		return ""
	}

	if path.Clean(u.Path) != u.Path || strings.ContainsAny(u.Path, "\\\r\n") {
		return ""
	}

	allowed := u.Path == "/" || u.Path == "/activity" || u.Path == "/connections" || u.Path == "/status" ||
		u.Path == "/configuration" ||
		strings.HasPrefix(u.Path, "/activity/") ||
		strings.HasPrefix(u.Path, "/executions/") ||
		strings.HasPrefix(u.Path, "/instruments/") ||
		strings.HasPrefix(u.Path, "/connections/") ||
		strings.HasPrefix(
			u.Path,
			"/evidence/",
		) || strings.HasPrefix(u.Path, "/reference-evidence/") || strings.HasPrefix(u.Path, "/system/")
	if !allowed {
		return ""
	}

	q := url.Values{}
	for _, key := range []string{"view", "q", "kind", "state", "sort", "dir", "page", "selected", "observations_q", "observations_sort", "observations_dir", "observations_page", "mode", "candidate", "location", "field"} {
		if v := u.Query().Get(key); v != "" {
			q.Set(key, v)
		}
	}

	u.RawQuery = q.Encode()
	u.Fragment = ""

	return u.String()
}

func origin(ctx context.Context) string {
	if v := localDestination(page(ctx).Query.Get("origin")); v != "" {
		return v
	}

	u, _ := url.Parse(page(ctx).URL)
	if u != nil &&
		(u.Path == "/activity" || u.Path == "/connections" || u.Path == "/status" || u.Path == "/configuration" || u.Path == "/system/maintenance") {
		return localDestination(u.String())
	}

	if u != nil && strings.HasPrefix(u.Path, "/connections/") {
		return "/connections"
	}

	if u != nil && strings.HasPrefix(u.Path, "/system/") {
		return "/status"
	}

	return "/activity"
}

func navURL(ctx context.Context, dest string) string {
	u, err := url.Parse(dest)
	if err != nil {
		return dest
	}

	q := u.Query()
	q.Set("origin", origin(ctx))

	current, _ := url.Parse(page(ctx).URL)
	if current != nil && current.Path != "/" && current.Path != "/activity" && current.Path != "/connections" {
		q.Set("parent", localDestination(current.String()))
	}

	u.RawQuery = q.Encode()

	return u.String()
}

func returnURL(ctx context.Context) string {
	if v := localDestination(page(ctx).Query.Get("parent")); v != "" {
		u, _ := url.Parse(v)
		q := u.Query()
		q.Set("origin", origin(ctx))
		u.RawQuery = q.Encode()

		return u.String()
	}

	return origin(ctx)
}

func parentLabel(ctx context.Context) string {
	p := strings.Split(returnURL(ctx), "?")[0]
	switch {
	case p == "/activity":
		return "Activity"
	case strings.HasPrefix(p, "/instruments/"):
		return "Instrument"
	case strings.HasPrefix(p, "/evidence/") || strings.HasPrefix(p, "/reference-evidence/"):
		return "Supporting evidence"
	case strings.HasPrefix(p, "/connections"):
		return "Connections"
	case p == "/status" || p == "/configuration" || strings.HasPrefix(p, "/system/"):
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

	q := u.Query()
	q.Set("mode", mode)
	q.Del("candidate")
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
	case strings.HasPrefix(field, "option.") || strings.HasPrefix(field, "debt.") || field == "quotation_basis":
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

type reviewItem struct{ Key, Kind, Label, Reason, Href string }

func sourceWork(v sourceView, c application.ConnectionView) []application.WorkStatus {
	operation := "discover"
	if v.Account.Connection() == c.ID && c.CanAcquire {
		operation = "acquire"
	}

	out := []application.WorkStatus{}
	for _, w := range c.Work {
		if w.Operation == operation {
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

		for _, w := range sourceWork(v, c) {
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
	ID, Href, Label, Date, Kind, Instrument, InstrumentID, Reference, Quantity, Price, Basis, State string
	FinancialState                                                                                  application.ActivityState
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

func connectionFailure(v sourceView, c application.ConnectionView) bool {
	if c.MissingAccess {
		return true
	}

	for _, w := range sourceWork(v, c) {
		if w.Failure != "" {
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
	q.Set("origin", origin(ctx))

	if parent := localDestination(page(ctx).Query.Get("parent")); parent != "" {
		q.Set("parent", parent)
	}

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
		return "/activity?view=instruments"
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

func candidateScope(f application.CorrectionForm) string {
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
	if path == "/configuration" {
		return "configuration"
	}

	return "status"
}

func servePageError(w http.ResponseWriter, r *http.Request, csrf string, status int, title, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_ = SectionShell(title, csrf, "", ErrorPage(title, message)).Render(r.Context(), w)
}

func originLabel(ctx context.Context) string {
	p := origin(ctx)
	if strings.HasPrefix(p, "/connections") {
		return "Connections"
	}

	if strings.HasPrefix(p, "/status") || strings.HasPrefix(p, "/configuration") ||
		strings.HasPrefix(p, "/system/") {
		return "System"
	}

	return "Activity"
}

func workspaceAttributes(ctx context.Context, csrf string) templ.Attributes {
	u, err := url.Parse(page(ctx).URL)
	if csrf == "" || page(ctx).Method == http.MethodPost || err != nil ||
		strings.HasSuffix(u.Path, "/correct") ||
		strings.HasPrefix(u.Path, "/evidence/") ||
		strings.HasPrefix(u.Path, "/reference-evidence/") {
		return templ.Attributes{}
	}

	// Financial grids refresh through their JSON API. Accepted details are explicit
	// page-load snapshots, leaving disclosures and inspected terms stable.
	if u.Path == "/activity" && page(ctx).Query.Get("view") != "review" ||
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

func orderedEvidence(
	ctx context.Context,
	observations []application.EvidenceObservation,
) []application.EvidenceObservation {
	out := append([]application.EvidenceObservation(nil), observations...)
	location := page(ctx).Query.Get("location")
	if location != "" {
		sort.SliceStable(out, func(i, j int) bool {
			return out[i].Location == location && out[j].Location != location
		})
	}

	return out
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
