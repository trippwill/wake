// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package admin

import (
	"context"
	"math/big"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
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
	for _, key := range []string{"view", "q", "kind", "state", "sort", "dir", "page", "selected", "mode", "candidate", "location", "field"} {
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
	for _, field := range f.Fields {
		if v, ok := c.Fields[field]; ok && f.Values[field] != v {
			return true
		}
	}

	return false
}

func reviewState(state string) bool {
	return state == "needs review" || state == "conflict" || state == "ambiguous" ||
		strings.Contains(state, "needs review")
}
func referenceGap(state string) bool { return state != "resolved" && !reviewState(state) }

type reviewItem struct{ Key, Kind, Label, Reason, Href string }

func reviews(v application.Dashboard) []reviewItem {
	items := []reviewItem{}
	seen := map[string]bool{}

	add := func(i reviewItem) {
		if !seen[i.Key] {
			seen[i.Key] = true
			items = append(items, i)
		}
	}
	for _, r := range activityItems(v) {
		if reviewState(r.State) {
			add(
				reviewItem{
					"activity/" + r.ID,
					"Activity",
					r.Label,
					"Reported values need review",
					"/activity/" + r.ID + "/correct?mode=evidence",
				},
			)
		}
	}

	for _, i := range v.Instruments {
		if reviewState(i.Status()) {
			add(
				reviewItem{
					"instrument/" + i.ID(),
					"Identity",
					known(i.Name()),
					"Instrument identity: " + i.Status(),
					"/instruments/" + i.ID() + "/correct?mode=evidence",
				},
			)
		}
	}

	for _, c := range v.Corrections {
		if !reviewState(c.Status) {
			continue
		}

		linked := false
		for _, id := range append(append([]string{}, c.TargetIDs...), c.ReplacementIDs...) {
			for _, a := range activityItems(v) {
				if a.ID == id {
					add(
						reviewItem{
							"activity/" + id,
							"Activity",
							a.Label,
							"Source correction: " + c.Status,
							"/activity/" + id + "/correct?mode=evidence",
						},
					)

					linked = true
				}
			}
		}

		if !linked {
			add(
				reviewItem{
					"source/" + c.Artifact + "/" + c.Location,
					"Source instruction",
					stringTitle(c.Kind) + " instruction",
					c.Status,
					"/evidence/" + c.Artifact + "?location=" + url.QueryEscape(c.Location),
				},
			)
		}
	}

	for _, c := range v.Connections {
		for _, p := range c.Processing {
			if p.NeedsReview > 0 {
				add(
					reviewItem{
						Key:    "report/" + p.Artifact,
						Kind:   "Source claims",
						Label:  "Identity review · " + c.Name,
						Reason: strconv.FormatInt(p.NeedsReview, 10) + " source claims have unresolved identity.",
						Href:   "/evidence/" + p.Artifact,
					},
				)
			}
		}
	}

	return items
}

func sourceWork(v application.Dashboard, c application.ConnectionView) []application.WorkStatus {
	operation := "discover"
	if v.Account.Connection() == c.ID {
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

func repairs(v application.Dashboard) []reviewItem {
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
					Reason: "Source settings or token are missing.",
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

	if len(v.Recovery) > 0 && v.Recovery[0].Status == "failed" {
		out = append(
			out,
			reviewItem{Label: "Projection rebuild", Reason: v.Recovery[0].Failure, Href: "/system/maintenance"},
		)
	}

	return out
}
func repairCount(v application.Dashboard) int { return len(repairs(v)) }

func stringTitle(s string) string {
	if s == "" {
		return "Unknown"
	}

	return strings.ToUpper(s[:1]) + s[1:]
}

func instrumentName(v application.Dashboard, id string) string {
	for _, i := range v.Instruments {
		if i.ID() == id {
			if i.Name() != "" {
				return i.Name()
			}

			return known(i.Ticker())
		}
	}

	return "Unresolved instrument"
}

func instrumentState(v application.Dashboard, id string) string {
	for _, i := range v.Instruments {
		if i.ID() == id {
			return i.Status()
		}
	}

	return "Unknown"
}

type activityItem struct{ ID, Href, Label, Date, Kind, Instrument, InstrumentID, Reference, Quantity, Price, Basis, State string }

func activityItems(v application.Dashboard) []activityItem {
	out := []activityItem{}
	for _, e := range v.Executions {
		f := e.Terms().Fields()
		name := instrumentName(v, e.Instrument())
		out = append(
			out,
			activityItem{
				ID:           e.ID(),
				Href:         "/executions/" + e.ID(),
				Label:        stringTitle(string(f.Side)) + " " + name,
				Date:         f.TradeDate.String(),
				Kind:         "trade",
				Instrument:   name,
				InstrumentID: e.Instrument(),
				Reference:    instrumentState(v, e.Instrument()),
				Quantity:     f.Quantity.Value() + " " + e.Terms().QuantityUnit(),
				Price:        priceText(f.Price),
				Basis:        e.Terms().PriceBasis(),
				State:        e.Status(),
			},
		)
	}

	for _, e := range v.Lifecycles {
		f := e.Terms().Fields()
		name := instrumentName(v, e.Instrument())
		out = append(
			out,
			activityItem{
				ID:           e.ID(),
				Href:         "/activity/" + e.ID(),
				Label:        stringTitle(string(f.Kind)) + " · " + name,
				Date:         f.Date.String(),
				Kind:         "option outcome",
				Instrument:   name,
				InstrumentID: e.Instrument(),
				Reference:    instrumentState(v, e.Instrument()),
				Quantity:     f.Quantity.Value() + " contracts",
				Price:        "Not applicable",
				State:        e.Status(),
			},
		)
	}

	for _, e := range v.Conversions {
		f := e.Terms().Fields()
		pair := currencyText(f.Sent.Currency()) + " → " + currencyText(f.Received.Currency())
		basis := ""
		if f.Rate.Amount() != "" {
			basis = "per " + currencyText(f.RateBase)
		}

		out = append(
			out,
			activityItem{
				ID:         e.ID(),
				Href:       "/activity/" + e.ID(),
				Label:      "Convert " + pair,
				Date:       f.Date.String(),
				Kind:       "conversion",
				Instrument: pair,
				Quantity:   currencyAmountText(f.Sent) + " sent · " + currencyAmountText(f.Received) + " received",
				Price:      priceText(f.Rate),
				Basis:      basis,
				State:      e.Status(),
			},
		)
	}

	return out
}

func decimalOrder(a, b string) int {
	af, bf := strings.Fields(a), strings.Fields(b)
	if len(af) == 0 || len(bf) == 0 {
		return strings.Compare(a, b)
	}

	x, xok := new(big.Rat).SetString(af[0])
	y, yok := new(big.Rat).SetString(bf[0])
	if !xok || !yok {
		return strings.Compare(a, b)
	}

	if unit := strings.Compare(strings.Join(af[1:], " "), strings.Join(bf[1:], " ")); unit != 0 {
		return unit
	}

	return x.Cmp(y)
}

func filteredActivity(ctx context.Context, v application.Dashboard) []activityItem {
	q := page(ctx).Query
	out := []activityItem{}
	for _, r := range activityItems(v) {
		if kind := q.Get("kind"); kind != "" && kind != r.Kind {
			continue
		}

		if state := q.Get("state"); state != "" && state != r.State {
			continue
		}

		if !strings.Contains(
			strings.ToLower(r.Label+" "+r.Date+" "+r.Quantity+" "+r.Price+" "+r.State+" "+r.Reference),
			strings.ToLower(q.Get("q")),
		) {
			continue
		}

		out = append(out, r)
	}

	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		cmp := 0
		switch q.Get("sort") {
		case "label":
			cmp = strings.Compare(a.Label, b.Label)
		case "quantity":
			cmp = decimalOrder(a.Quantity, b.Quantity)
		case "price":
			cmp = decimalOrder(a.Price+" "+a.Basis, b.Price+" "+b.Basis)
		case "state":
			cmp = strings.Compare(a.State, b.State)
		case "reference":
			cmp = strings.Compare(a.Reference, b.Reference)
		default:
			cmp = strings.Compare(a.Date, b.Date)
		}

		if cmp == 0 {
			cmp = strings.Compare(a.ID, b.ID)
		}

		if q.Get("dir") == "asc" {
			return cmp < 0
		}

		return cmp > 0
	})

	return out
}

func filteredInstruments(ctx context.Context, v application.Dashboard) []application.Instrument {
	q := page(ctx).Query
	out := []application.Instrument{}
	for _, i := range v.Instruments {
		if kind := q.Get("kind"); kind != "" && kind != i.Kind() {
			continue
		}

		state := q.Get("state")
		if state == "gaps" && !referenceGap(i.Status()) || state == "review" && !reviewState(i.Status()) ||
			state != "" && state != "gaps" && state != "review" && state != i.Status() {
			continue
		}

		if !strings.Contains(
			strings.ToLower(i.Name()+" "+i.Ticker()+" "+i.Kind()+" "+i.Status()),
			strings.ToLower(q.Get("q")),
		) {
			continue
		}

		out = append(out, i)
	}

	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		left, right := a.Name(), b.Name()
		switch q.Get("sort") {
		case "kind":
			left, right = a.Kind(), b.Kind()
		case "ticker":
			left, right = a.Ticker(), b.Ticker()
		case "state":
			left, right = a.Status(), b.Status()
		}

		cmp := strings.Compare(left, right)
		if cmp == 0 {
			cmp = strings.Compare(a.ID(), b.ID())
		}

		if q.Get("dir") == "desc" {
			return cmp > 0
		}

		return cmp < 0
	})

	return out
}

func resultCount(n, total int) string {
	return strconv.Itoa(n) + " of " + strconv.Itoa(total) + " records"
}

func connectionByID(v application.Dashboard, id string) (application.ConnectionView, bool) {
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

func connectionFailure(v application.Dashboard, c application.ConnectionView) bool {
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
	return "/connections/" + id + "/confirm?artifact=" + url.QueryEscape(
		c.Artifact,
	) + "&account=" + url.QueryEscape(
		c.Candidate.Reference().Value(),
	)
}

func readiness(ok bool) string {
	if ok {
		return "Stored"
	}

	return "Missing"
}

func recoveryReady(v application.Dashboard) bool {
	if v.Storage.ReferenceReview {
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

	return templ.Attributes{
		"hx-get":     page(ctx).URL,
		"hx-trigger": "sse:refresh, every 10s",
		"hx-target":  "this",
		"hx-select":  "#workspace-content",
		"hx-swap":    "morph:outerHTML",
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
