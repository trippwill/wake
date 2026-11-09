// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

//go:build browser

package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/a-h/templ"
	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/network"
	cdppage "github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
	"go.trippwill.dev/wake/financial"
	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/config"
	"go.trippwill.dev/wake/internal/host"
	"go.trippwill.dev/wake/internal/testbrowser"
	"go.trippwill.dev/wake/provider"
	"go.trippwill.dev/wake/source"
)

// The independent browser fixture exposes only a remote page of exact values.
func polishGrid(notes bool) templ.Component {
	return templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		if err := PageHeader(
			"Activity",
			"Review exact financial terms and retained observations.",
		).Render(ctx, w); err != nil {
			return err
		}

		if _, err := io.WriteString(
			w,
			`<details id="view-notes"><summary>Reading this view</summary><p>Exact values remain available in every row.</p></details>`,
		); err != nil {
			return err
		}

		endpoint := "/test/grid-data"
		if notes {
			endpoint += "?notes=true"
		}

		return Grid("activity", endpoint, "Precision activity").Render(ctx, w)
	})
}

func polishGridData(w http.ResponseWriter, r *http.Request, extra bool) {
	params := r.URL.Query()

	page, _ := strconv.Atoi(params.Get("page"))
	size, _ := strconv.Atoi(params.Get("size"))

	scope := application.ActivityScope
	if params.Get("view") == "instruments" {
		scope = application.InstrumentScope
	}

	query, err := application.NewListQuery(
		application.ListOptions{
			Page:      page,
			PageSize:  size,
			Search:    params.Get("q"),
			Sort:      params.Get("sort"),
			Direction: params.Get("dir"),
		},
		scope,
	)
	if err != nil {
		gridJSON(w, nil, err)
		return
	}

	options := query.Options()
	values := []string{
		"9007199254740993.0000000000001",
		"9007199254740992.9999999999999",
		"-1.0000000000000001",
		"-1",
		"0",
		"-0",
	}

	count := 70
	if extra {
		count = 71
	}

	rows := make([]activityGridRow, 0, count)
	for i := range count {
		value := fmt.Sprint(i)
		if i < len(values) {
			value = values[i]
		}

		name := fmt.Sprintf("Synthetic investment %02d", i)
		if i == 0 {
			name = `Investment <img src=x onerror="window.privateInjection=true">`
		}

		if options.Search != "" &&
			!strings.Contains(strings.ToLower(name+" "+value+" USD accepted"), options.Search) {
			continue
		}

		rows = append(
			rows,
			activityGridRow{
				ID:        fmt.Sprintf("precision-%03d", i),
				Label:     gridCell{Text: name, Href: "/test/grid?revision=" + strconv.FormatBool(extra)},
				Date:      gridCell{Text: "2026-10-20"},
				Quantity:  gridCell{Text: "1 shares"},
				Price:     gridCell{Text: value + " USD"},
				State:     gridCell{Text: "accepted", Tone: "neutral"},
				Reference: gridCell{Text: "provisional"},
			},
		)

		if params.Get("notes") == "true" {
			rows[len(rows)-1].Label.Note = "Trade"
			rows[len(rows)-1].Price.Note = "Per share"
		}
	}

	slices.SortFunc(rows, func(a, b activityGridRow) int {
		order := 0
		switch options.Sort {
		case "price":
			av, _ := new(big.Rat).SetString(strings.TrimSuffix(a.Price.Text, " USD"))
			bv, _ := new(big.Rat).SetString(strings.TrimSuffix(b.Price.Text, " USD"))
			order = av.Cmp(bv)
		case "label", "name":
			order = strings.Compare(a.Label.Text, b.Label.Text)
		}

		if options.Direction == "desc" {
			order = -order
		}

		if order == 0 {
			return strings.Compare(a.ID, b.ID)
		}

		return order
	})
	matched := len(rows)

	start := min(int(query.Offset()), matched)
	end := min(start+options.PageSize, matched)

	info := application.PageInfo{
		Page:     options.Page,
		PageSize: options.PageSize,
		Total:    int64(count),
		Matched:  int64(matched),
	}
	if scope == application.InstrumentScope {
		instruments := make([]instrumentGridRow, 0, end-start)
		for _, row := range rows[start:end] {
			instruments = append(instruments, instrumentGridRow{
				ID:     row.ID,
				Ticker: row.Label,
				Name:   gridCell{Text: "Synthetic description"},
				Kind:   gridCell{Text: "Equity"},
				State:  row.Reference,
			})
		}

		gridJSON(w, gridResult(instruments, info), nil)

		return
	}

	gridJSON(
		w,
		gridResult(rows[start:end], info),
		nil,
	)
}

func polishObservations() templ.Component {
	return templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		if err := Breadcrumbs("Instrument").Render(ctx, w); err != nil {
			return err
		}

		if err := PageHeader(
			"Instrument observations",
			"Inspect retained source locations and acceptance rules.",
		).Render(ctx, w); err != nil {
			return err
		}

		if err := AcceptedDetailScope().Render(ctx, w); err != nil {
			return err
		}

		return ObservationHistory("instrument", "synthetic-instrument").Render(ctx, w)
	})
}

func polishObservationData(w http.ResponseWriter, r *http.Request, changed bool) {
	rows := []application.Observation{}

	count := 2
	artifact := r.URL.Query().Get("subject") == "artifact"
	if artifact {
		count = 75
	}

	if r.URL.Query().Get("id") == "empty-report" {
		count = 0
	}

	for i := range count {
		outcome := application.ObservationAccepted
		if i == 1 && changed {
			outcome = application.ObservationTermsConflict
		}

		rows = append(
			rows,
			application.Observation{
				Artifact: fmt.Sprintf("synthetic-%d", i+1),
				Version:  "1",
				Location: strings.Repeat(
					"/FlexQueryResponse[1]/FlexStatements[1]/FlexStatement[1]",
					4,
				) + fmt.Sprintf(
					"/TradeConfirm[%d]",
					i+1,
				),
				Outcome:   outcome,
				Rule:      "qualified-activity-observations/2",
				Retrieved: "2026-10-20T12:00:00Z",
			},
		)
	}

	if artifact {
		for i := range rows {
			rows[i].Artifact = "synthetic-report"
			if i%3 != 0 {
				rows[i].ActivityID = "synthetic-activity"
				rows[i].Outcome = application.ObservationNoContribution
			}
		}

		if len(rows) != 0 {
			rows[len(rows)-1].Location = strings.Repeat("/r[1]", 200) + "/TradeConfirm[75]"
		}
	}

	query, err := listQuery(r.Context(), application.ObservationScope)
	if err != nil {
		gridJSON(w, nil, err)
		return
	}

	options := query.Options()

	if options.Search != "" {
		rows = slices.DeleteFunc(rows, func(row application.Observation) bool {
			return !strings.Contains(
				strings.ToLower(row.Artifact+" "+row.Location+" "+observationStatus(row.Outcome)),
				options.Search,
			)
		})
	}

	if options.Direction == "desc" && options.Sort == "location" {
		slices.Reverse(rows)
	}

	start := min(int(query.Offset()), len(rows))
	end := min(start+options.PageSize, len(rows))

	gridJSON(
		w,
		observationGrid(
			application.ObservationList{
				Items: rows[start:end],
				PageInfo: application.PageInfo{
					Page:     options.Page,
					PageSize: options.PageSize,
					Total:    int64(count),
					Matched:  int64(len(rows)),
				},
			},
		),
		nil,
	)
}

func TestBrowserPolishedWorkflowsAndGrids(t *testing.T) {
	tenant, _ := provider.NewField("tenant", "Tenant identifier", provider.TextField, true)
	secret, _ := provider.NewField("api_secret", "API secret", provider.SecretField, true)
	stream, _ := provider.NewStream("statements", "synthetic-v1")
	descriptor, _ := provider.NewDescriptor(
		"synthetic",
		"Synthetic source",
		[]provider.Stream{stream},
		[]provider.Field{tenant, secret},
		nil,
	)

	registration, err := provider.NewRegistration(descriptor, "v1", nil, browserNormalizer{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	browser := os.Getenv("WAKE_BROWSER")
	if browser == "" {
		for _, name := range []string{"brave-browser", "chromium", "google-chrome"} {
			if p, err := exec.LookPath(name); err == nil {
				browser = p
				break
			}
		}
	}

	if browser == "" {
		t.Fatal("Chromium required")
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	origin := "http://" + listener.Addr().String()

	dir := t.TempDir()
	if err = SetUnlock(dir, []byte("synthetic-polish-unlock")); err != nil {
		t.Fatal(err)
	}

	auth, _, err := Open(dir, "local", origin)
	if err != nil {
		t.Fatal(err)
	}
	defer auth.Close()

	var warehouseBytes atomic.Int64
	warehouseBytes.Store(134637824)

	base, err := NewHandler(auth, Dependencies{
		Queries: browserStatus{size: &warehouseBytes},
		Config: config.Config{Paths: config.Paths{
			ConfigFile: "/synthetic/operator configuration/private instance/settings.toml",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	production, err := NewHandler(
		auth,
		Dependencies{Workflow: application.New(workspaceStore{}, nil, nil, nil, nil)},
	)
	if err != nil {
		t.Fatal(err)
	}

	identity := presentationInstrument(t)

	bill, err := source.NewDebtTerms(source.DebtInput{Type: source.TreasuryBill})
	if err != nil {
		t.Fatal(err)
	}

	var (
		extra, offline, expired, noGrid, refreshFailed, holdRefresh, holdSelection, holdMovedFocus atomic.Bool
		emptyGrid, failFirstPage                                                                   atomic.Bool
	)

	refreshStarted, releaseRefresh := make(chan struct{}, 1), make(chan struct{}, 1)
	selectionStarted, releaseSelection := make(chan struct{}, 1), make(chan struct{})
	movedFocusStarted, releaseMovedFocus := make(chan struct{}, 1), make(chan struct{})

	var holdAlias atomic.Bool

	aliasStarted, releaseAlias := make(chan struct{}, 1), make(chan struct{}, 1)

	eventShutdown := make(chan struct{})
	form := application.CorrectionForm{
		Kind:        "activity",
		ID:          "synthetic-activity",
		Label:       "Buy · Synthetic investment · 20 October 2026",
		AccountName: "Primary account",
		Revision:    4,
		Fields: []string{
			"price",
			"quantity",
			"instrument",
			"listing",
			"trade_date",
			"executed_at",
			"side",
			"option.right",
			"option.expiry",
		},
		Values: map[string]application.CorrectionValue{
			"price":      {Value: "9007199254740992.9999999999999", Currency: "USD"},
			"quantity":   {Value: "0.000000000000123", Unit: "shares"},
			"instrument": {Value: "instrument-one", Label: "Synthetic first option (ONE)"},
			"trade_date": {
				Value: "2026-10-20",
			},
			"executed_at":  {Value: "2026-10-20T13:30:00.120000000Z"},
			"side":         {Value: "buy"},
			"option.right": {Value: "call"},
		},
		Links: []application.CorrectionCandidate{
			{Scope: "instrument", Value: "instrument-one", Label: "Synthetic first option (ONE)"},
			{Scope: "instrument", Value: "instrument-two", Label: "Synthetic second option (TWO)"},
			{Scope: "instrument", Value: "instrument-three", Label: "Unrelated gamma option (GAMMA)"},
			{Scope: "listing", Value: "listing-one", Label: "CBOE · USD"},
		},
		Candidates: []application.CorrectionCandidate{
			{
				Value: "candidate-one",
				Label: "IBKR Confirmation · 20 October",
				Fields: map[string]application.CorrectionValue{
					"price":    {Value: "9007199254740993.0000000000001", Currency: "USD"},
					"quantity": {Value: "0.000000000000123", Unit: "shares"},
				},
			},
		},
	}
	view := fixtureView{
		Connections: []application.ConnectionView{
			{
				ID:         "synthetic-connection",
				Name:       "Long-term investment account",
				Descriptor: descriptor,

				SettingsStored: true, CredentialStored: true,
				Work: []application.WorkStatus{
					{
						Operation: "discover",
						Progress:  "waiting",
						Next:      time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC),
						Scheduled: true,
					},
				},
				Processing: []application.ProcessingStatus{
					{Artifact: "synthetic-report", Receipt: "synthetic-receipt", ActivityClaims: 70, Complete: true},
				},
			},
		},
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = pageRequest(r)
		if r.URL.Path == "/activity" || r.URL.Path == "/instruments" || r.URL.Path == "/review" ||
			r.URL.Path == "/grids/activity" || r.URL.Path == "/grids/instruments" {
			production.ServeHTTP(w, r)
			return
		}

		if strings.HasSuffix(r.URL.Path, "tabulator.min.js") && noGrid.Load() {
			http.NotFound(w, r)
			return
		}

		if r.URL.Path == "/system/contact" {
			if expired.Load() {
				http.Error(w, "expired", http.StatusUnauthorized)
				return
			}

			if offline.Load() {
				http.Error(w, "offline", http.StatusServiceUnavailable)
				return
			}
		}

		if r.URL.Path == "/events" {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "event: refresh\ndata: {}\n\n")
			w.(http.Flusher).Flush()

			select {
			case <-r.Context().Done():
			case <-eventShutdown:
			}

			return
		}

		if r.URL.Path == "/test/grid-data" && r.Header.Get("HX-Request") == "true" &&
			holdRefresh.CompareAndSwap(true, false) {
			refreshStarted <- struct{}{}

			select {
			case <-releaseRefresh:
			case <-r.Context().Done():
				return
			}
		}

		if r.URL.Path == "/test/grid-data" && holdMovedFocus.CompareAndSwap(true, false) {
			movedFocusStarted <- struct{}{}

			select {
			case <-releaseMovedFocus:
			case <-r.Context().Done():
				return
			}
		}

		if r.URL.Path == "/test/grid-data" && holdSelection.CompareAndSwap(true, false) {
			selectionStarted <- struct{}{}

			select {
			case <-releaseSelection:
			case <-r.Context().Done():
				return
			}
		}

		if r.URL.Path == "/test/grid-data" && r.Header.Get("HX-Request") == "true" &&
			(refreshFailed.Load() || (failFirstPage.Load() && r.URL.Query().Get("page") == "1")) {
			http.Error(w, "Read temporarily unavailable", http.StatusServiceUnavailable)
			return
		}

		if r.URL.Path == "/activity/synthetic-activity/correct/check" {
			if _, ok := auth.session(r); !ok {
				http.Error(w, "expired", http.StatusUnauthorized)
				return
			}

			if offline.Load() {
				http.Error(w, "unavailable", http.StatusServiceUnavailable)
				return
			}

			_ = r.ParseForm()
			field, value := r.Form.Get("scope"), r.Form.Get("value")
			valid := false
			switch field {
			case "price":
				currency, e := financial.ParseCurrency(r.Form.Get("currency"))
				if e == nil {
					_, e = financial.NewPrice(value, currency)
				}

				valid = e == nil
			case "quantity":
				q, e := financial.NewQuantity(value)
				valid = e == nil && q.Positive()
			case "trade_date", "option.expiry":
				_, e := financial.ParseDate(value)
				valid = e == nil || field == "option.expiry" && value == ""
			case "executed_at":
				_, e := financial.ParseTimestamp(value)
				valid = e == nil || value == ""
			case "side":
				valid = slices.Contains([]string{"buy", "sell"}, value)
			case "option.right":
				valid = slices.Contains([]string{"put", "call", ""}, value)
			case "instrument":
				valid = value == "instrument-one" || value == "instrument-two"
			case "listing":
				valid = value == "listing-one" || value == ""
			}

			message := "Ready to save."
			if !valid {
				message = "This value is invalid for the selected financial field."
			}

			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(struct {
				Valid   bool   `json:"valid"`
				Message string `json:"message"`
			}{valid, message})

			return
		}

		if r.URL.Path == "/test/grid-data" || r.URL.Path == "/grids/observations" {
			if _, ok := auth.session(r); !ok || expired.Load() {
				http.Error(w, "expired", http.StatusUnauthorized)
				return
			}

			if r.URL.Path == "/test/grid-data" {
				if emptyGrid.Load() {
					gridJSON(w, gridPage[activityGridRow]{Data: []activityGridRow{}, LastPage: 1, Total: 71}, nil)
				} else {
					polishGridData(w, r, extra.Load())
				}
			} else {
				polishObservationData(w, r, !extra.Load())
			}

			return
		}

		if strings.HasPrefix(r.URL.Path, "/test/") || strings.HasPrefix(r.URL.Path, "/evidence/") ||
			r.URL.Path == "/" ||
			r.URL.Path == "/activity/synthetic-activity/correct" ||
			r.URL.Path == "/instruments/synthetic-instrument" ||
			r.URL.Path == "/instruments/synthetic-instrument/correct" ||
			r.URL.Path == "/instruments/retired-instrument" ||
			r.URL.Path == "/instruments/retired-instrument/correct" {
			csrf, ok := auth.session(r)
			if !ok {
				http.Redirect(w, r, "/login", http.StatusSeeOther)
				return
			}

			setSecurityHeaders(w.Header())

			if r.Method == http.MethodHead && r.URL.Path == "/instruments/retired-instrument" && holdAlias.Load() {
				select {
				case aliasStarted <- struct{}{}:
				case <-r.Context().Done():
					return
				}

				select {
				case <-releaseAlias:
				case <-r.Context().Done():
					return
				}
			}

			var component templ.Component
			switch r.URL.Path {
			case "/instruments/retired-instrument":
				http.Redirect(w, r, "/instruments/synthetic-instrument", http.StatusSeeOther)
				return
			case "/instruments/retired-instrument/correct":
				http.Redirect(w, r, "/instruments/synthetic-instrument/correct?mode=decisions", http.StatusSeeOther)
				return
			case "/evidence/synthetic-report", "/evidence/empty-report":
				artifact, _ := provider.NewArtifact(
					"synthetic-xml",
					"application/xml",
					time.Now(),
					[]byte("<SyntheticReport/>"),
				)
				component = RetainedEvidence(
					host.Retained{
						ID:       strings.TrimPrefix(r.URL.Path, "/evidence/"),
						Provider: "Synthetic provider",
						Artifact: artifact,
					},
					application.EvidenceContext{ConnectionName: "Synthetic source"},
				)
			case "/test/grid", "/test/dense-grid":
				component = polishGrid(r.URL.Path == "/test/dense-grid")
			case "/test/instruments":
				component = Grid("instruments", "/test/grid-data?view=instruments", "Synthetic instruments")
			case "/test/observations", "/instruments/synthetic-instrument":
				component = polishObservations()
			case "/test/correction",
				"/activity/synthetic-activity/correct",
				"/instruments/synthetic-instrument/correct":
				snapshot := page(r.Context())
				correction := form
				if r.URL.Path == "/instruments/synthetic-instrument/correct" {
					correction.Kind = "instrument"
					correction.ID = "synthetic-instrument"
				} else {
					snapshot.URL = "/activity/synthetic-activity/correct?" + r.URL.RawQuery
				}

				r = r.WithContext(context.WithValue(r.Context(), pageKey{}, snapshot))
				component = CorrectionPage(csrf, correction)
			case "/test/source":
				component = ConnectionPage(csrf, fixtureSource(view), view.Connections[0])
			case "/test/access":
				page(r.Context()).Query.Set("view", "access")

				component = ConnectionPage(csrf, fixtureSource(view), view.Connections[0])
			case "/test/reports":
				page(r.Context()).Query.Set("view", "reports")

				component = ConnectionPage(csrf, fixtureSource(view), view.Connections[0])
			case "/test/new":
				component = ConnectionSetup(csrf, []provider.Registration{registration})
			case "/test/reference":
				component = ReferencePage(csrf, fixtureSource(view))
			case "/test/reference-evidence":
				component = EvidencePage(
					application.ReferenceEvidence{
						Retrieved: "2026-11-02",
						Request:   `[{"idType":"ID_BB_GLOBAL","idValue":"synthetic-composite"}]`,
						Response:  `[{"data":[{"figi":"synthetic-composite","compositeFIGI":"synthetic-composite","shareClassFIGI":"synthetic-share"}]}]`,
					},
				)
			case "/test/bill":
				component = templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
					if err := PageHeader("Treasury bill", "Synthetic debt terms").Render(ctx, w); err != nil {
						return err
					}

					return DebtFacts(bill).Render(ctx, w)
				})
			case "/test/instrument":
				component = InstrumentPage(identity)
			case "/test/connections":
				component = ConnectionsPage(csrf, fixtureSource(view))
			case "/test/recovery":
				copy := view
				copy.Connections = append([]application.ConnectionView{}, view.Connections...)
				copy.Connections[0].MissingAccess = true
				copy.Connections[0].SettingsStored = false
				copy.Connections[0].CredentialStored = false
				copy.Storage.Paused = true
				copy.Storage.ReferenceReview = true

				component = RecoveryPage(csrf, fixtureSource(copy))
			case "/test/overview", "/":
				component = DashboardOverview("synthetic-csrf", fixtureOverview(view), "")
			default:
				http.NotFound(w, r)
				return
			}

			if err := SectionShell(
				"Review workspace",
				csrf,
				"Activity",
				component,
			).Render(r.Context(), w); err != nil {
				t.Error(err)
			}

			return
		}

		base.ServeHTTP(w, r)
	})
	server := httptest.NewUnstartedServer(handler)

	_ = server.Listener.Close()
	server.Listener = listener
	server.Start()
	defer func() { close(eventShutdown); server.Close() }()

	allocator, stop := chromedp.NewExecAllocator(
		context.Background(),
		append(chromedp.DefaultExecAllocatorOptions[:], chromedp.ExecPath(browser))...)
	defer stop()

	ctx, cancel := chromedp.NewContext(allocator)
	defer cancel()

	ctx, deadline := context.WithTimeout(ctx, 240*time.Second)
	defer deadline()

	if err := chromedp.Run(ctx); err != nil {
		t.Fatal(err)
	}

	var (
		exceptions          atomic.Int64
		navigationTransport atomic.Bool
	)
	chromedp.ListenTarget(ctx, func(event any) {
		if _, ok := event.(*runtime.EventExceptionThrown); ok {
			exceptions.Add(1)
		}

		if request, ok := event.(*network.EventRequestWillBeSent); ok {
			u, err := url.Parse(request.Request.URL)
			if err == nil &&
				(u.Query().Has("origin") || u.Query().Has("parent") || u.Query().Has("page_url") || u.Query().Has("location")) {
				navigationTransport.Store(true)
			}
		}
	})

	check := func(actions ...chromedp.Action) {
		t.Helper()

		step, cancelStep := context.WithTimeout(ctx, 20*time.Second)
		defer cancelStep()

		if err := chromedp.Run(step, actions...); err != nil {
			var location, body, grid string

			_ = chromedp.Run(
				ctx,
				chromedp.Location(&location),
				chromedp.Text("body", &body, chromedp.ByQuery),
				chromedp.Evaluate(
					`JSON.stringify({ready:document.querySelector('[data-grid]')?.dataset.gridReady,size:window.Tabulator?.findTable('.tabulator')[0]?.getPageSize(),rows:document.querySelectorAll('.tabulator-row').length,choice:document.querySelector('.tabulator-page-size')?.value,active:document.activeElement.tagName})`,
					&grid,
				),
			)
			t.Fatal(err, location, grid, body)
		}
	}
	assert := func(script, message string) {
		t.Helper()

		var ok bool
		check(chromedp.Evaluate(script, &ok))

		if !ok {
			var overflow string

			_ = chromedp.Run(
				ctx,
				chromedp.Evaluate(
					`JSON.stringify({viewport:innerWidth,scroll:document.documentElement.scrollWidth,client:document.documentElement.clientWidth,body:document.body.scrollWidth,grid:[...document.querySelectorAll('.wake-grid-header,.tabulator,.tabulator-header,.tabulator-tableholder,.tabulator-footer,.tabulator-row:nth-child(-n+6)')].map(n=>({cls:n.className,rect:n.getBoundingClientRect().toJSON()})),elements:[...document.querySelectorAll('body *')].map(n=>({tag:n.tagName,cls:n.className,text:n.textContent.slice(0,60),scroll:n.scrollWidth,client:n.clientWidth,right:n.getBoundingClientRect().right,width:n.getBoundingClientRect().width})).filter(n=>n.width&&(n.right>innerWidth+1||n.scroll>n.client+1)).slice(0,12)})`,
					&overflow,
				),
			)
			t.Fatal(message, overflow)
		}
	}

	applyGridUpdate := chromedp.Tasks{
		chromedp.Poll(
			`!document.querySelector('[data-grid]').hasAttribute('aria-busy') && !document.querySelector('[data-grid-refresh]').hidden`,
			nil,
		),
		chromedp.Evaluate(`document.querySelector('[data-grid-refresh]').click()`, nil),
		chromedp.Poll(
			`document.querySelector('[data-grid-refresh]').hidden && document.querySelector('[data-grid]').dataset.gridReady==='true'`,
			nil,
		),
	}

	check(
		emulation.SetDeviceMetricsOverride(1440, 1000, 1, false),
		testbrowser.NewDocument(chromedp.Navigate(origin+"/login")),
		chromedp.WaitVisible("#unlock", chromedp.ByQuery),
		chromedp.SendKeys("#unlock", "synthetic-polish-unlock", chromedp.ByQuery),
		chromedp.Click(`button[type="submit"]`, chromedp.ByQuery),
		chromedp.WaitVisible("#main-navigation", chromedp.ByQuery),
		chromedp.Evaluate(`localStorage.setItem('tabulator-wake-activity-columns','invalid JSON')`, nil),
		testbrowser.NewDocument(chromedp.Navigate(origin+"/test/grid")),
		chromedp.WaitVisible(".tabulator", chromedp.ByQuery),
		chromedp.Poll(`document.querySelector('.tabulator-row')!==null`, nil),
	)
	assert(
		`(()=>{const icons=[...document.querySelectorAll('.wake-nav-link use')];return icons.length===7 && new Set(icons.map(icon=>icon.getAttribute('href'))).size===icons.length})()`,
		"sidebar destinations reuse an icon",
	)
	assert(
		`!document.querySelector('table') && !window.privateInjection && document.querySelector('[data-grid-status]').textContent==='70 of 70 results'`,
		"JSON grid or escaping",
	)

	// Direct corrections preserve the canonical parent, including after contextual history is cleared.
	for _, path := range []string{"/activity/synthetic-activity", "/instruments/synthetic-instrument"} {
		check(
			testbrowser.NewDocument(chromedp.Navigate(origin+path+"/correct?mode=decisions")),
			chromedp.WaitVisible("[data-parent-breadcrumb]", chromedp.ByQuery),
		)
		assert(
			`(()=>{const parent=document.querySelector('[data-parent-breadcrumb]');return !parent.hidden&&new URL(parent.querySelector('a').href).pathname===`+strconv.Quote(
				path,
			)+`})()`,
			"direct correction lost its canonical parent",
		)
		check(
			chromedp.Evaluate(
				`history.replaceState({wakeNavigation:{workspace:'/review',trail:['/review','/evidence/synthetic-report']}},'');dispatchEvent(new PopStateEvent('popstate'))`,
				nil,
			),
		)
		assert(
			`document.querySelector('[data-parent-breadcrumb] a').textContent==='Supporting evidence'`,
			"contextual parent was not applied",
		)
		check(
			chromedp.Evaluate(`history.replaceState(null,'');dispatchEvent(new PopStateEvent('popstate'))`, nil),
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
			chromedp.WaitVisible("h1", chromedp.ByQuery),
			chromedp.Evaluate(
				`(()=>{const a=document.createElement('a');a.id='alias-proof';a.href='/instruments/retired-instrument'+`+strconv.Quote(
					suffix,
				)+`;a.textContent='Inspect retired instrument';document.body.append(a)})()`,
				nil,
			),
			testbrowser.NewDocument(chromedp.Click("#alias-proof", chromedp.ByQuery)),
			chromedp.Poll(`history.state?.wakeNavigation?.workspace==='/review'`, nil),
		)
		assert(
			`location.pathname.startsWith('/instruments/synthetic-instrument')&&new URL(document.querySelector('[data-return]').href).pathname==='/review'&&new URL(document.querySelector('[data-return]').href).searchParams.get('q')==='retained-filter'&&document.querySelector('.wake-nav-link[aria-current="page"]').getAttribute('href')==='/review'`,
			"canonical redirect lost the originating workspace or filters",
		)
		check(
			testbrowser.NewDocument(chromedp.Reload()),
			chromedp.Poll(`history.state?.wakeNavigation?.workspace==='/review'`, nil),
		)
	}

	for _, action := range []string{"link", "form"} {
		holdAlias.Store(true)
		check(
			testbrowser.NewDocument(chromedp.Navigate(origin+"/review?q=delayed-filter&size=25")),
			chromedp.WaitVisible("h1", chromedp.ByQuery),
			chromedp.Evaluate(
				`(()=>{const a=document.createElement('a');a.id='delayed-alias';a.href='/instruments/retired-instrument';a.textContent='Open retired instrument';document.body.append(a)})()`,
				nil,
			),
			testbrowser.NewDocument(chromedp.Click("#delayed-alias", chromedp.ByQuery)),
			chromedp.ActionFunc(func(ctx context.Context) error {
				select {
				case <-aliasStarted:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			}),
			chromedp.Evaluate(
				`(()=>{const form=document.createElement('form');form.id='delayed-form';form.method='get';form.action='/instruments/synthetic-instrument/correct';form.innerHTML='<input name="mode" value="decisions" type="hidden"><button type="submit" id="delayed-submit">Open corrections</button>';document.body.append(form);const a=document.createElement('a');a.id='delayed-link';a.href='/instruments/synthetic-instrument/correct?mode=decisions';a.textContent='Correct instrument data';document.body.append(a)})()`,
				nil,
			),
		)

		selector := "#delayed-link"
		if action == "form" {
			selector = "#delayed-submit"
		}

		var waiting bool
		check(
			testbrowser.NewDocument(chromedp.Tasks{
				chromedp.Click(selector, chromedp.ByQuery),
				chromedp.Evaluate(
					`location.pathname==='/instruments/synthetic-instrument'&&document.querySelector('[aria-busy="true"]')!==null`,
					&waiting,
				),
				chromedp.ActionFunc(func(context.Context) error {
					holdAlias.Store(false)

					releaseAlias <- struct{}{}

					return nil
				}),
			}),
			chromedp.Poll(
				`history.state?.wakeNavigation?.workspace==='/review'&&location.pathname.endsWith('/correct')`,
				nil,
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
		chromedp.WaitVisible("h1", chromedp.ByQuery),
		chromedp.Evaluate(
			`(()=>{sessionStorage.setItem('wake.navigation.pending',JSON.stringify({from:location.pathname,to:'/instruments/unrelated-instrument',navigation:{workspace:'/review',trail:['/review']}}));const a=document.createElement('a');a.id='unrelated-proof';a.href='/instruments/synthetic-instrument';a.textContent='Open unrelated destination';a.addEventListener('click',event=>event.stopPropagation());document.body.append(a)})()`,
			nil,
		),
		testbrowser.NewDocument(chromedp.Click("#unrelated-proof", chromedp.ByQuery)),
		chromedp.Poll(
			`document.querySelector('[data-grid]')?.dataset.gridReady==='true'&&history.state?.wakeNavigation?.workspace==='/instruments'`,
			nil,
		),
	)
	assert(`history.state?.wakeNavigation?.trail.length===0`, "unrelated handoff adopted navigation context")

	check(
		testbrowser.NewDocument(chromedp.Navigate(origin+"/review?q=fragment-filter&size=25")),
		chromedp.WaitVisible("h1", chromedp.ByQuery),
		chromedp.Evaluate(
			`(()=>{const a=document.createElement('a');a.id='fragment-report';a.href='/evidence/synthetic-report';a.textContent='Inspect source report';document.body.append(a)})()`,
			nil,
		),
		testbrowser.NewDocument(chromedp.Click("#fragment-report", chromedp.ByQuery)),
		chromedp.Poll(`document.querySelector('[data-grid]')?.dataset.gridReady==='true'`, nil),
		chromedp.Evaluate(`window.fragmentContext=JSON.stringify(history.state.wakeNavigation)`, nil),
	)
	assert(
		`new URL(location.href).searchParams.get('sort')==='report'&&new URL(location.href).searchParams.get('dir')==='desc'&&[...document.querySelectorAll('.tabulator-col[tabulator-field]')].filter(column=>column.getClientRects().length).length===4`,
		"artifact table wrote invalid default ordering into its URL",
	)
	check(
		chromedp.Click(`a[href="#original-content"]`, chromedp.ByQuery),
		chromedp.Poll(`location.hash==='#original-content'`, nil),
	)
	assert(
		`JSON.stringify(history.state?.wakeNavigation)===window.fragmentContext&&document.querySelector('.wake-nav-link[aria-current="page"]').getAttribute('href')==='/review'&&new URL(document.querySelector('[data-return]').href).searchParams.get('q')==='fragment-filter'`,
		"in-page report navigation discarded its return context",
	)
	check(
		chromedp.Evaluate(`history.back()`, nil),
		chromedp.Poll(`location.hash===''`, nil),
	)
	assert(
		`JSON.stringify(history.state?.wakeNavigation)===window.fragmentContext`,
		"fragment Back lost navigation context",
	)
	check(
		chromedp.Evaluate(`history.forward()`, nil),
		chromedp.Poll(`location.hash==='#original-content'`, nil),
	)
	assert(
		`JSON.stringify(history.state?.wakeNavigation)===window.fragmentContext`,
		"fragment Forward lost navigation context",
	)
	check(
		testbrowser.NewDocument(chromedp.Reload()),
		chromedp.Poll(`document.querySelector('[data-grid]')?.dataset.gridReady==='true'`, nil),
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
		chromedp.SetValue("#observation-search", "TradeConfirm[75]", chromedp.ByQuery),
		chromedp.Click("[data-observation-filters] button", chromedp.ByQuery),
		chromedp.Poll(`document.querySelector('[data-grid-status]').textContent==='1 of 75 results'`, nil),
		chromedp.Evaluate(`window.Tabulator.findTable('.tabulator')[0].setSort('outcome','asc')`, nil),
		chromedp.Poll(
			`new URL(location.href).searchParams.get('sort')==='outcome'&&new URL(location.href).searchParams.get('dir')==='asc'&&document.querySelector('[data-grid]').dataset.gridReady==='true'`,
			nil,
		),
		testbrowser.NewDocument(chromedp.Reload()),
		chromedp.Poll(`document.querySelector('[data-grid-status]').textContent==='1 of 75 results'`, nil),
	)
	assert(
		`new URL(location.href).searchParams.get('sort')==='outcome'&&new URL(location.href).searchParams.get('dir')==='asc'&&document.querySelector('.tabulator-col[tabulator-field="outcome"]').getAttribute('aria-sort')==='ascending'`,
		"artifact selected ordering did not survive reload",
	)
	check(
		chromedp.Evaluate(`history.back()`, nil),
		chromedp.Poll(
			`!new URL(location.href).searchParams.has('q')&&new URL(location.href).searchParams.get('sort')==='report'&&document.querySelector('[data-grid-status]').textContent==='75 of 75 results'`,
			nil,
		),
		chromedp.Evaluate(`history.forward()`, nil),
		chromedp.Poll(
			`new URL(location.href).searchParams.get('q')==='TradeConfirm[75]'&&new URL(location.href).searchParams.get('sort')==='outcome'&&document.querySelector('[data-grid-status]').textContent==='1 of 75 results'`,
			nil,
		),
	)

	check(
		testbrowser.NewDocument(chromedp.Navigate(origin+"/test/grid")),
		chromedp.WaitVisible(".tabulator-row", chromedp.ByQuery),
	)

	visitGrid := func(path string, size, rows int) {
		t.Helper()
		check(
			testbrowser.NewDocument(chromedp.Navigate(origin+path)),
			chromedp.Poll(fmt.Sprintf(
				`document.querySelector('[data-grid]')?.dataset.gridReady==='true' && document.querySelector('.tabulator-page-size')?.value==='%d' && document.querySelectorAll('.tabulator-row').length===%d`,
				size,
				rows,
			), nil),
		)
	}
	chooseSize := func(size, rows int) {
		t.Helper()
		check(
			chromedp.Focus(".tabulator-page-size", chromedp.ByQuery),
			chromedp.SetValue(".tabulator-page-size", strconv.Itoa(size), chromedp.ByQuery),
			chromedp.Evaluate(
				`document.querySelector('.tabulator-page-size').dispatchEvent(new Event('change',{bubbles:true}))`,
				nil,
			),
			chromedp.Poll(fmt.Sprintf(
				`!document.querySelector('[data-grid]').hasAttribute('aria-busy') && document.querySelector('[data-grid]').dataset.gridReady==='true' && document.querySelectorAll('.tabulator-row').length===%d && document.querySelector('.tabulator-page-size').value==='%d'`,
				rows,
				size,
			), nil),
		)
		assert(`document.activeElement.matches('.tabulator-page-size')`, "page-size change lost keyboard focus")
	}

	// Resize the same populated document across the former width and height breakpoints.
	for _, theme := range []string{"light", "dark"} {
		check(chromedp.Evaluate(`localStorage.setItem('wake.theme',`+strconv.Quote(theme)+`)`, nil))

		for _, size := range []int{50, 200} {
			check(emulation.SetDeviceMetricsOverride(1280, 720, 1, false))
			visitGrid("/test/dense-grid?size="+strconv.Itoa(size), size, min(size, 70))
			check(chromedp.Evaluate(`window.resizedGrid=document.querySelector('[data-grid]')`, nil))

			for _, viewport := range [][2]int64{{1280, 720}, {768, 640}, {768, 639}, {320, 700}, {390, 844}, {768, 639}, {768, 640}, {1280, 720}} {
				width, height := viewport[0], viewport[1]
				check(
					emulation.SetDeviceMetricsOverride(width, height, 1, false),
					chromedp.Poll(fmt.Sprintf(`(()=>{
						const holder=document.querySelector('.tabulator-tableholder');
						const grid=document.querySelector('[data-grid]').getBoundingClientRect();
						const footer=document.querySelector('.tabulator-footer').getBoundingClientRect();
						return innerWidth===%d && innerHeight===%d && holder.clientHeight>100 && holder.scrollHeight>holder.clientHeight && footer.bottom<=grid.bottom+1;
					})()`, width, height), nil),
					chromedp.Evaluate(`document.querySelector('[data-grid]').scrollIntoView({block:'start'})`, nil),
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

				if folder := os.Getenv("WAKE_SCREENSHOT_DIR"); folder != "" && size == 50 {
					var png []byte
					check(chromedp.FullScreenshot(&png, 90))

					//nolint:gosec // Caller-selected synthetic screenshot directory.
					if err := os.MkdirAll(folder, 0o700); err != nil {
						t.Fatal(err)
					}

					name := fmt.Sprintf("grid-resize-%s-%dx%d.png", theme, width, height)
					//nolint:gosec // Caller-selected synthetic screenshot directory.
					if err := os.WriteFile(filepath.Join(folder, name), png, 0o600); err != nil {
						t.Fatal(err)
					}
				}
			}
		}

		check(
			chromedp.Focus(".wake-grid-help summary", chromedp.ByQuery),
			chromedp.SendKeys(".wake-grid-help summary", "\r", chromedp.ByQuery),
		)
		assert(
			`document.querySelector('.wake-grid-help').open && document.querySelector('.wake-grid-help').textContent.includes('Alt + arrow keys') && document.querySelector('.tabulator-tableholder').clientHeight>100`,
			"table keyboard help is unavailable or crowds out the rows",
		)
		check(chromedp.SendKeys(".wake-grid-help summary", "\r", chromedp.ByQuery))
		assert(
			`!document.querySelector('.wake-grid-help').open && document.activeElement.matches('.wake-grid-help summary')`,
			"closing table help lost focus",
		)
	}

	check(emulation.SetDeviceMetricsOverride(1440, 1000, 1, false))

	// Exercise the native selector, then carry its size through filtering and history.
	visitGrid("/test/grid?size=50&q=investment&sort=price&dir=asc&page=2&selected=precision-025", 50, 20)
	assert(
		`(()=>{const s=document.querySelector('.tabulator-page-size');return s.getAttribute('aria-label')==='Rows per page' && s.labels[0]?.textContent==='Rows per page' && [...s.options].map(o=>o.value).join(',')==='10,25,50,100,200'})()`,
		"page-size selector choices or label",
	)
	check(
		chromedp.Focus(".tabulator-page-size", chromedp.ByQuery),
		chromedp.SendKeys(".tabulator-page-size", kb.ArrowUp, chromedp.ByQuery),
		chromedp.Poll(
			`document.querySelectorAll('.tabulator-row').length===25 && new URL(location.href).searchParams.get('size')==='25' && !document.querySelector('[data-grid]').hasAttribute('aria-busy')`,
			nil,
		),
	)
	assert(
		`(()=>{const q=new URL(location.href).searchParams;return q.get('page')==='1' && q.get('q')==='investment' && q.get('sort')==='price' && q.get('dir')==='asc' && !q.has('selected') && document.activeElement.matches('.tabulator-page-size')})()`,
		"keyboard page-size change lost query state, focus, or retained the old page/selection",
	)
	check(
		chromedp.Evaluate(`window.Tabulator.findTable('.tabulator')[0].setPage(2)`, nil),
		chromedp.Poll(
			`new URL(location.href).searchParams.get('page')==='2' && !document.querySelector('[data-grid]').hasAttribute('aria-busy')`,
			nil,
		),
		chromedp.Evaluate(
			`(()=>{const url=new URL(location.href);url.searchParams.set('selected','precision-025');history.replaceState(history.state,'',url);document.querySelector('[data-list-filters]').requestSubmit()})()`,
			nil,
		),
		chromedp.Poll(
			`new URL(location.href).searchParams.get('page')==='1' && !document.querySelector('[data-grid]').hasAttribute('aria-busy')`,
			nil,
		),
	)
	chooseSize(100, 70)
	check(
		chromedp.Evaluate(`history.back()`, nil),
		chromedp.Poll(
			`new URL(location.href).searchParams.get('page')==='2' && new URL(location.href).searchParams.get('size')==='25' && document.querySelector('.tabulator-page-size').value==='25' && document.querySelectorAll('.tabulator-row').length===25 && !document.querySelector('[data-grid]').hasAttribute('aria-busy')`,
			nil,
		),
	)
	assert(
		`new URL(location.href).searchParams.get('selected')==='precision-025'`,
		"history size restoration cleared its selected record",
	)
	check(
		chromedp.Evaluate(`history.forward()`, nil),
		chromedp.Poll(
			`new URL(location.href).searchParams.get('size')==='100' && new URL(location.href).searchParams.get('page')==='1' && document.querySelectorAll('.tabulator-row').length===70 && !document.querySelector('[data-grid]').hasAttribute('aria-busy')`,
			nil,
		),
	)
	chooseSize(200, 70)
	assert(
		`new URL(location.href).searchParams.get('size')==='200'`,
		"selected size did not reach the owning URL",
	)
	chooseSize(25, 25)
	check(chromedp.Click("[data-grid-reset]", chromedp.ByQuery))
	visitGrid("/test/grid", 25, 25)
	assert(
		`Object.keys(JSON.parse(localStorage.getItem('tabulator-wake-activity-page'))).join(',')==='paginationSize'`,
		"page-size persistence also saved the page number",
	)

	// Each table type keeps its own page-size preference.
	visitGrid("/instruments/synthetic-instrument", 50, 2)
	chooseSize(10, 2)
	assert(
		`new URL(location.href).searchParams.get('size')==='10'`,
		"observation page size missing from URL",
	)
	assert(
		`(()=>{const link=new URL(document.querySelector('.tabulator-row a').href);return link.pathname.startsWith('/evidence/') && !link.search && new URL(location.href).searchParams.get('size')==='10'})()`,
		"evidence return link lost the selected observation page size",
	)
	visitGrid("/test/instruments", 50, 50)
	chooseSize(100, 70)
	visitGrid("/test/grid", 25, 25)
	visitGrid("/instruments/synthetic-instrument", 10, 2)
	visitGrid("/test/instruments", 100, 70)
	visitGrid("/test/grid?size=200", 200, 70)

	for _, saved := range []string{"invalid JSON", `{"paginationSize":201}`, `{"paginationSize":[25]}`} {
		check(
			chromedp.Evaluate(`localStorage.setItem('tabulator-wake-activity-page',`+strconv.Quote(saved)+`)`, nil),
		)
		visitGrid("/test/grid?size=invalid", 50, 50)
	}

	check(
		chromedp.Evaluate(
			`localStorage.setItem('tabulator-wake-activity-page','{"paginationSize":25,"paginationInitialPage":2}')`,
			nil,
		),
	)
	visitGrid("/test/grid?size=201", 25, 25)
	assert(`new URL(location.href).searchParams.get('page')==='1'`, "saved preference restored a page number")

	// Unsupported URL sizes must reach browser fallback through the production HTML route.
	for _, kind := range []string{"activity", "instruments"} {
		check(
			chromedp.Evaluate(`localStorage.setItem('tabulator-wake-`+kind+`-page','{"paginationSize":25}')`, nil),
		)

		for _, size := range []string{"invalid", "201", "20"} {
			visitGrid("/"+kind+"?size="+size, 25, 0)
		}

		check(chromedp.Evaluate(`localStorage.removeItem('tabulator-wake-`+kind+`-page')`, nil))
		visitGrid("/"+kind+"?size=invalid", 50, 0)
	}

	// Block storage before initialization as well as while changing the control.
	var blockedStorage cdppage.ScriptIdentifier

	check(chromedp.ActionFunc(func(ctx context.Context) error {
		var e error

		blockedStorage, e = cdppage.AddScriptToEvaluateOnNewDocument(`
			for(const method of ['getItem','setItem']) {
				const original=Storage.prototype[method];
				Storage.prototype[method]=function(key,...args) {
					if(key.startsWith('tabulator-')) throw new DOMException('Blocked','SecurityError');
					return original.call(this,key,...args);
				};
			}
		`).Do(ctx)

		return e
	}))
	visitGrid("/test/grid", 50, 50)
	chooseSize(100, 70)

	// Reset navigation retains the live size even when no browser preference can be saved.
	for _, path := range []string{"/activity?kind=trade&state=accepted", "/instruments?kind=equity&state=resolved"} {
		visitGrid(path+"&q=investment&size=50&page=2&selected=one", 50, 0)
		chooseSize(25, 0)
		check(
			testbrowser.NewDocument(chromedp.Click("[data-grid-reset-filters]", chromedp.ByQuery)),
			chromedp.Poll(
				`document.querySelector('[data-grid]')?.dataset.gridReady==='true' && document.querySelector('.tabulator-page-size')?.value==='25'`,
				nil,
			),
		)
		assert(
			`(()=>{const q=new URL(location.href).searchParams;return ['/activity','/instruments'].includes(location.pathname) && q.get('size')==='25' && q.get('page')==='1' && ['q','kind','state','selected'].every(key=>!q.has(key)) && (document.querySelector('[data-grid]').dataset.grid==='instruments')===(location.pathname==='/instruments')})()`,
			"resetting filters lost page size or retained filters/selection with blocked storage",
		)
	}

	check(cdppage.RemoveScriptToEvaluateOnNewDocument(blockedStorage))
	visitGrid("/test/grid?size=100", 100, 70)

	refreshFailed.Store(true)
	check(
		chromedp.SetValue(".tabulator-page-size", "25", chromedp.ByQuery),
		chromedp.Evaluate(
			`document.querySelector('.tabulator-page-size').dispatchEvent(new Event('change',{bubbles:true}))`,
			nil,
		),
		chromedp.WaitVisible("[data-grid-retry]", chromedp.ByQuery),
	)
	assert(
		`document.querySelectorAll('.tabulator-row').length===70`,
		"failed size request discarded the displayed rows",
	)
	refreshFailed.Store(false)
	check(
		chromedp.Click("[data-grid-retry]", chromedp.ByQuery),
		chromedp.Poll(
			`document.querySelectorAll('.tabulator-row').length===25 && new URL(location.href).searchParams.get('size')==='25' && !document.querySelector('[data-grid]').hasAttribute('aria-busy')`,
			nil,
		),
	)
	emptyGrid.Store(true)
	chooseSize(10, 0)
	emptyGrid.Store(false)
	chooseSize(50, 50)
	check(
		chromedp.Evaluate(
			`for(const kind of ['activity','instruments','observations']) localStorage.removeItem('tabulator-wake-'+kind+'-page')`,
			nil,
		),
	)

	check(
		testbrowser.NewDocument(chromedp.Navigate(origin+"/test/reference-evidence")),
		chromedp.WaitVisible("#original-content summary", chromedp.ByQuery),
	)
	assert(
		`!document.querySelector('#original-content').open&&!document.querySelector('a[href="#original-content"]')`,
		"duplicate evidence action",
	)
	check(
		chromedp.Focus("#original-content summary", chromedp.ByQuery),
		chromedp.SendKeys("#original-content summary", "\r", chromedp.ByQuery),
		chromedp.WaitVisible(`pre[aria-label="Retained response"]`, chromedp.ByQuery),
	)
	assert(
		`document.querySelector('#original-content').open&&document.querySelector('pre[aria-label="Retained request"]').textContent.includes('\n    "idType"')`,
		"keyboard evidence disclosure",
	)
	check(
		testbrowser.NewDocument(chromedp.Navigate(origin+"/test/grid")),
		chromedp.WaitVisible(".tabulator-row", chromedp.ByQuery),
	)
	check(
		chromedp.Evaluate(
			`window.proofGrid=window.Tabulator.findTable('.tabulator')[0];window.proofGrid.setSort('price','asc')`,
			nil,
		),
		chromedp.Poll(
			`document.querySelector('.tabulator-row [tabulator-field=price]')?.textContent==='-1.0000000000000001 USD' && !document.querySelector('[data-grid]').hasAttribute('aria-busy')`,
			nil,
		),
	)
	assert(
		`document.querySelectorAll('.tabulator-row').length===50`,
		"remote grid downloaded more than one bounded page",
	)
	check(
		chromedp.Evaluate(`window.proofGrid.setPage(2)`, nil),
		chromedp.Poll(
			`new URL(location.href).searchParams.get('page')==='2' && document.querySelectorAll('.tabulator-row').length===20 && Array.from(document.querySelectorAll('.tabulator-row')).at(-1)?.querySelector('[tabulator-field=price]')?.textContent==='9007199254740993.0000000000001 USD'`,
			nil,
		),
	)
	check(
		chromedp.Evaluate(
			`(()=>{const input=document.querySelector('#activity-search');input.value='Synthetic investment 69';input.form.requestSubmit()})()`,
			nil,
		),
		chromedp.Poll(
			`document.querySelector('[data-grid-status]').textContent==='1 of 70 results' && document.querySelectorAll('.tabulator-row').length===1`,
			nil,
		),
	)
	assert(`window.proofGrid.getPageMax()===1`, "server filtering did not update pagination")
	check(
		chromedp.Evaluate(
			`(()=>{const input=document.querySelector('#activity-search');input.value='investment';input.form.requestSubmit()})()`,
			nil,
		),
		chromedp.Poll(
			`document.querySelector('[data-grid-status]').textContent==='70 of 70 results' && new URL(location.href).searchParams.get('page')==='1'`,
			nil,
		),
	)
	// Same-document browser history restores the filter form, server query and page.
	check(
		chromedp.Evaluate(`history.back()`, nil),
		chromedp.Poll(
			`new URL(location.href).searchParams.get('q')==='Synthetic investment 69' && document.querySelector('#activity-search').value==='Synthetic investment 69' && document.querySelector('[data-grid-status]').textContent==='1 of 70 results' && document.querySelectorAll('.tabulator-row').length===1`,
			nil,
		),
		chromedp.Evaluate(`history.back()`, nil),
		chromedp.Poll(
			`!new URL(location.href).searchParams.has('q') && new URL(location.href).searchParams.get('page')==='2' && document.querySelector('#activity-search').value==='' && document.querySelectorAll('.tabulator-row').length===20`,
			nil,
		),
		chromedp.Evaluate(`history.go(2)`, nil),
		chromedp.Poll(
			`new URL(location.href).searchParams.get('q')==='investment' && new URL(location.href).searchParams.get('page')==='1' && document.querySelector('#activity-search').value==='investment' && document.querySelectorAll('.tabulator-row').length===50`,
			nil,
		),
	)
	// A denied preference write must leave keyboard/layout interactions usable.
	check(
		chromedp.Evaluate(
			`(()=>{const write=Storage.prototype.setItem;try{Storage.prototype.setItem=()=>{throw new DOMException('Blocked','SecurityError')};window.proofGrid.getColumn('label').setWidth(334)}finally{Storage.prototype.setItem=write}})()`,
			nil,
		),
	)
	assert(
		`window.proofGrid.getColumn('label').getWidth()===334 && document.querySelectorAll('.tabulator-row').length===50`,
		"blocked preference storage prevented table interactions",
	)
	check(
		chromedp.Evaluate(
			`window.proofGrid.getColumn('label').setWidth(310);window.proofGrid.setSort('price','desc');document.activeElement.blur()`,
			nil,
		),
		chromedp.Poll(
			`document.querySelector('.tabulator-row [tabulator-field=price]')?.textContent==='9007199254740993.0000000000001 USD' && !document.querySelector('[data-grid]').hasAttribute('aria-busy')`,
			nil,
		),
	)
	check(
		chromedp.Evaluate(`window.proofGrid.setPage(2);document.querySelector('#view-notes').open=true`, nil),
		chromedp.Poll(
			`new URL(location.href).searchParams.get('page')==='2' && document.querySelectorAll('.tabulator-row').length===20`,
			nil,
		),
	)

	// A real polling tick must leave an unchanged page in place throughout the
	// download and after completion, including its row nodes and scroll position.
	check(chromedp.Evaluate(`
		window.beforePollRow=document.querySelector('.tabulator-row');
		document.querySelector('.tabulator-tableholder').scrollTop=150;
		window.beforePollScroll=document.querySelector('.tabulator-tableholder').scrollTop;
	`, nil))
	holdRefresh.Store(true)

	select {
	case <-refreshStarted:
	case <-time.After(15 * time.Second):
		t.Fatal("polling did not request fresh table data")
	}

	assert(
		`window.beforePollRow.isConnected && document.querySelectorAll('.tabulator-row').length===20 && document.querySelector('.tabulator-tableholder').clientHeight>0 && document.querySelector('[data-grid]').getAttribute('aria-busy')==='true'`,
		"polling cleared the current rows while downloading",
	)

	releaseRefresh <- struct{}{}

	check(chromedp.Poll(`!document.querySelector('[data-grid]').hasAttribute('aria-busy')`, nil))
	assert(
		`window.beforePollRow===document.querySelector('.tabulator-row') && document.querySelector('.tabulator-tableholder').scrollTop===window.beforePollScroll && new URL(location.href).searchParams.get('page')==='2' && document.querySelector('[data-grid-refresh]').hidden`,
		"unchanged polling response rebuilt the table or reset the current view",
	)

	extra.Store(true)
	check(
		chromedp.Evaluate(`window.htmx.trigger(document.querySelector('#workspace-content'),'sse:refresh')`, nil),
		chromedp.WaitVisible("[data-grid-refresh]", chromedp.ByQuery),
	)
	assert(
		`window.beforePollRow===document.querySelector('.tabulator-row') && document.querySelectorAll('.tabulator-row').length===20 && document.querySelector('[data-grid-status]').textContent.includes('Updated results are available')`,
		"changed response replaced rows before explicit refresh",
	)
	// If fresh results return to the displayed state, there is no net change
	// left to accept and the notice disappears without touching the rows.
	check(chromedp.Focus("[data-grid-refresh]", chromedp.ByQuery))
	extra.Store(false)
	check(
		chromedp.Evaluate(`window.htmx.trigger(document.querySelector('#workspace-content'),'sse:refresh')`, nil),
		chromedp.Poll(
			`!document.querySelector('[data-grid]').hasAttribute('aria-busy') && document.querySelector('[data-grid-refresh]').hidden`,
			nil,
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
	extra.Store(true)
	check(
		chromedp.Evaluate(`window.htmx.trigger(document.querySelector('#workspace-content'),'sse:refresh')`, nil),
		chromedp.WaitVisible("[data-grid-refresh]", chromedp.

			// Refresh fetches the current response rather than installing the snapshot
			// that first raised the notice; a last-moment reversion keeps the rows intact.
			ByQuery),
	)

	extra.Store(false)
	check(
		chromedp.Focus("[data-grid-refresh]", chromedp.ByQuery),
		chromedp.KeyEvent("\r"),
		chromedp.Poll(
			`document.querySelector('[data-grid-refresh]').hidden && document.querySelector('[data-grid]').dataset.gridReady==='true'`,
			nil,
		),
	)
	assert(
		`window.beforePollRow===document.querySelector('.tabulator-row')`,
		"refresh applied a stale polling response",
	)
	extra.Store(true)
	check(
		chromedp.Evaluate(`window.htmx.trigger(document.querySelector('#workspace-content'),'sse:refresh')`, nil),
		chromedp.WaitVisible("[data-grid-refresh]", chromedp.ByQuery),
	)
	check(
		chromedp.Focus("[data-grid-refresh]", chromedp.ByQuery),
		chromedp.KeyEvent("\r"),
		chromedp.Poll(
			`document.querySelector('[data-grid-status]').textContent==='71 of 71 results' && document.querySelectorAll('.tabulator-row').length===21`,
			nil,
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
	extra.Store(false)
	holdRefresh.Store(true)
	check(
		chromedp.Focus(`.tabulator-col[tabulator-field="label"]`, chromedp.ByQuery),
		chromedp.Evaluate(`window.htmx.trigger(document.querySelector('#workspace-content'),'sse:refresh')`, nil),
	)

	select {
	case <-refreshStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("delayed JSON refresh did not start")
	}

	check(
		chromedp.Focus("#activity-search", chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelector('#activity-search').value='unsubmitted filter draft'`, nil),
	)

	releaseRefresh <- struct{}{}

	check(
		applyGridUpdate,
		chromedp.Poll(
			`document.querySelector('[data-grid-status]').textContent==='70 of 70 results' && document.querySelectorAll('.tabulator-row').length===20`,
			nil,
		),
	)
	assert(
		`document.activeElement.id==='activity-search' && document.activeElement.value==='unsubmitted filter draft' && document.querySelector('#view-notes').open`,
		"JSON refresh changed focused draft",
	)
	check(
		chromedp.Evaluate(
			`document.querySelector('#activity-search').value='investment';document.activeElement.blur()`,
			nil,
		),
	)
	extra.Store(true)
	refreshFailed.Store(true)
	check(
		chromedp.Evaluate(`window.htmx.trigger(document.querySelector('#workspace-content'),'sse:refresh')`, nil),
		chromedp.WaitVisible("[data-grid-retry]", chromedp.ByQuery),
	)
	assert(
		`document.querySelectorAll('.tabulator-row').length===20 && document.querySelector('[data-grid-status]').textContent.includes('Previous results') && document.querySelector('#view-notes').open`,
		"failed JSON request replaced displayed data",
	)
	refreshFailed.Store(false)
	check(
		chromedp.Click("[data-grid-retry]", chromedp.ByQuery),
		applyGridUpdate,
		chromedp.Poll(
			`document.querySelector('[data-grid-status]').textContent==='71 of 71 results' && new URL(location.href).searchParams.get('page')==='2'`,
			nil,
		),
	)
	// Focus follows the operator's latest choice within the grid, including
	// entering a row after a background request has already started.

	for _, initial := range []string{`document.querySelector('.tabulator-col[tabulator-field="label"]').focus()`, `document.activeElement.blur()`} {
		check(chromedp.Evaluate(initial, nil))
		holdMovedFocus.Store(true)
		check(
			chromedp.Evaluate(
				`window.htmx.trigger(document.querySelector('#workspace-content'),'sse:refresh')`,
				nil,
			),
		)

		select {
		case <-movedFocusStarted:
		case <-time.After(10 * time.Second):
			t.Fatal("focus movement did not hold a JSON request")
		}

		check(
			chromedp.Evaluate(
				`(()=>{const link=document.querySelectorAll('.tabulator-row a')[1];link.focus();document.querySelector('[data-grid]').dataset.focusTarget=link.closest('[data-record]').dataset.record})()`,
				nil,
			),
		)

		extra.Store(!extra.Load())

		select {
		case releaseMovedFocus <- struct{}{}:
		case <-time.After(10 * time.Second):
			t.Fatal("focus response was abandoned")
		}

		check(
			applyGridUpdate,
			chromedp.Poll(
				`!document.querySelector('[data-grid]').hasAttribute('aria-busy')&&document.querySelector('[data-grid]').dataset.gridReady==='true'`,
				nil,
			),
		)

		var (
			focusOK bool
			trace   string
		)

		check(
			chromedp.Evaluate(
				`document.activeElement.closest('[data-record]')?.dataset.record===document.querySelector('[data-grid]').dataset.focusTarget`,
				&focusOK,
			),
		)

		if !focusOK {
			check(
				chromedp.Evaluate(
					`JSON.stringify({active:document.activeElement.tagName,record:document.activeElement.closest('[data-record]')?.dataset.record,href:document.activeElement.getAttribute('href'),expected:document.querySelector('[data-grid]').dataset.focusTarget})`,
					&trace,
				),
			)
			t.Fatal("focus after delayed response", initial, trace)
		}
	}

	// Copying an exact displayed amount must survive both a queued refresh and a
	// response that was already downloading when selection began.
	check(chromedp.Poll(`!document.querySelector('[data-grid]').hasAttribute('aria-busy')`, nil))

	check(
		chromedp.Evaluate(
			`(()=>{const range=document.createRange();range.selectNodeContents(document.querySelector('.tabulator-row [tabulator-field="price"]'));document.getSelection().removeAllRanges();document.getSelection().addRange(range);window.copiedExactValue=document.getSelection().toString();window.htmx.trigger(document.querySelector('#workspace-content'),'sse:refresh')})()`,
			nil,
		),
		chromedp.Poll(
			`!document.querySelector('[data-grid]').hasAttribute('aria-busy')`,
			nil,
		),
	)

	assert(
		`document.getSelection().toString()===window.copiedExactValue && window.copiedExactValue!==''`,
		"background check disturbed selected exact text",
	)

	check(chromedp.Evaluate(`document.getSelection().removeAllRanges()`, nil))
	holdSelection.Store(true)
	check(
		chromedp.Evaluate(`window.htmx.trigger(document.querySelector('#workspace-content'),'sse:refresh')`, nil),
	)

	select {
	case <-selectionStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("selection test did not hold a JSON request")
	}

	extra.Store(false)
	check(
		chromedp.Evaluate(
			`(()=>{const range=document.createRange();range.selectNodeContents(document.querySelector('.tabulator-row [tabulator-field="price"]'));document.getSelection().removeAllRanges();document.getSelection().addRange(range);window.copiedExactValue=document.getSelection().toString()})()`,
			nil,
		),
	)
	close(releaseSelection)
	check(chromedp.WaitVisible("[data-grid-refresh]", chromedp.ByQuery))
	assert(
		`document.getSelection().toString()===window.copiedExactValue&&window.copiedExactValue!==''&&document.querySelectorAll('.tabulator-row').length===21`,
		"in-flight response erased selected exact text",
	)
	check(
		chromedp.Evaluate(`document.getSelection().removeAllRanges()`, nil),
		applyGridUpdate,
		chromedp.Poll(
			`document.querySelector('[data-grid-status]').textContent==='70 of 70 results'&&document.querySelectorAll('.tabulator-row').length===20`,
			nil,
		),
	)

	// A disappearing final page also waits for consent, then moves to the last
	// available page. A later increase updates the pagination controls on refresh.
	extra.Store(true)
	check(
		chromedp.Evaluate(`window.proofGrid.setPageSize(70).then(()=>window.proofGrid.setPage(2))`, nil),
		chromedp.Poll(
			`document.querySelectorAll('.tabulator-row').length===1 && !document.querySelector('[data-grid]').hasAttribute('aria-busy')`,
			nil,
		),
	)
	extra.Store(false)
	check(
		chromedp.Evaluate(`window.htmx.trigger(document.querySelector('#workspace-content'),'sse:refresh')`, nil),
		chromedp.WaitVisible("[data-grid-refresh]", chromedp.ByQuery),
	)
	assert(
		`window.proofGrid.getPage()===2 && document.querySelectorAll('.tabulator-row').length===1`,
		"background removal changed the displayed page",
	)
	check(
		applyGridUpdate,
		chromedp.Poll(
			`document.querySelector('.tabulator-page[data-page="1"]').classList.contains('active') && document.querySelector('.tabulator-page[data-page="next"]').disabled && document.querySelectorAll('.tabulator-row').length===70`,
			nil,
		),
	)
	extra.Store(true)
	check(
		chromedp.Evaluate(`window.htmx.trigger(document.querySelector('#workspace-content'),'sse:refresh')`, nil),
		applyGridUpdate,
	)
	assert(
		`document.querySelector('.tabulator-page[data-page="2"]')!==null && !document.querySelector('.tabulator-page[data-page="next"]').disabled`,
		"explicit refresh did not update page controls",
	)

	// Retry finishes the original foreground action with one click, while the
	// earlier background-error test still requires an explicit Refresh afterward.
	refreshFailed.Store(true)
	check(
		chromedp.Evaluate(
			`(()=>{const input=document.querySelector('#activity-search');input.value='Synthetic investment 69';input.form.requestSubmit()})()`,
			nil,
		),
		chromedp.WaitVisible("[data-grid-retry]", chromedp.ByQuery),
	)
	refreshFailed.Store(false)
	check(
		chromedp.Click("[data-grid-retry]", chromedp.ByQuery),
		chromedp.Poll(
			`document.querySelectorAll('.tabulator-row').length===1 && document.querySelector('[data-grid-status]').textContent==='1 of 71 results' && document.querySelector('[data-grid-refresh]').hidden`,
			nil,
		),
		chromedp.Evaluate(
			`(()=>{const input=document.querySelector('#activity-search');input.value='investment';input.form.requestSubmit()})()`,
			nil,
		),
		chromedp.Poll(
			`document.querySelectorAll('.tabulator-row').length===70 && !document.querySelector('[data-grid]').hasAttribute('aria-busy')`,
			nil,
		),
	)
	refreshFailed.Store(true)
	check(
		chromedp.Evaluate(`window.proofGrid.setSort('price','asc')`, nil),
		chromedp.WaitVisible("[data-grid-retry]", chromedp.ByQuery),
	)
	refreshFailed.Store(false)
	check(
		chromedp.Click("[data-grid-retry]", chromedp.ByQuery),
		chromedp.Poll(
			`new URL(location.href).searchParams.get('dir')==='asc' && document.querySelector('.tabulator-row [tabulator-field=price]').textContent==='-1.0000000000000001 USD' && document.querySelector('[data-grid-refresh]').hidden`,
			nil,
		),
	)
	refreshFailed.Store(true)
	check(
		chromedp.Evaluate(`window.proofGrid.setPage(2)`, nil),
		chromedp.WaitVisible("[data-grid-retry]", chromedp.ByQuery),
	)
	refreshFailed.Store(false)
	check(
		chromedp.Click("[data-grid-retry]", chromedp.ByQuery),
		chromedp.Poll(
			`new URL(location.href).searchParams.get('page')==='2' && document.querySelectorAll('.tabulator-row').length===1 && document.querySelector('[data-grid-refresh]').hidden`,
			nil,
		),
	)
	extra.Store(false)
	check(
		chromedp.Evaluate(`window.htmx.trigger(document.querySelector('#workspace-content'),'sse:refresh')`, nil),
		chromedp.WaitVisible("[data-grid-refresh]", chromedp.ByQuery),
	)
	refreshFailed.Store(true)
	check(
		chromedp.Click("[data-grid-refresh]", chromedp.ByQuery),
		chromedp.WaitVisible("[data-grid-retry]", chromedp.ByQuery),
	)
	refreshFailed.Store(false)
	check(
		chromedp.Click("[data-grid-retry]", chromedp.ByQuery),
		chromedp.Poll(
			`new URL(location.href).searchParams.get('page')==='1' && document.querySelectorAll('.tabulator-row').length===70 && document.querySelector('[data-grid-status]').textContent==='70 of 70 results' && document.querySelector('[data-grid-refresh]').hidden`,
			nil,
		),
	)

	// A response is not the displayed baseline until its rows are installed.
	// Empty results, a failed fallback and Retry must never retain stale rows.
	extra.Store(true)
	check(
		chromedp.Evaluate(
			`window.Tabulator.findTable('.tabulator')[0].setData().then(()=>window.Tabulator.findTable('.tabulator')[0].setPage(2))`,
			nil,
		),
		chromedp.Poll(
			`document.querySelectorAll('.tabulator-row').length===1 && !document.querySelector('[data-grid]').hasAttribute('aria-busy')`,
			nil,
		),
	)
	emptyGrid.Store(true)
	check(
		chromedp.Evaluate(`window.htmx.trigger(document.querySelector('#workspace-content'),'sse:refresh')`, nil),
		chromedp.WaitVisible("[data-grid-refresh]", chromedp.ByQuery),
	)
	failFirstPage.Store(true)
	check(
		chromedp.Click("[data-grid-refresh]", chromedp.ByQuery),
		chromedp.WaitVisible("[data-grid-retry]", chromedp.ByQuery),
	)
	assert(
		`document.querySelectorAll('.tabulator-row').length===1 && document.querySelector('[data-grid-status]').textContent.includes('Previous results')`,
		"failed page fallback obscured the retained results or its error",
	)
	failFirstPage.Store(false)
	check(
		chromedp.Click("[data-grid-retry]", chromedp.ByQuery),
		chromedp.Poll(
			`document.querySelectorAll('.tabulator-row').length===0 && document.querySelector('[data-grid-status]').textContent==='0 of 71 results' && document.querySelector('[data-grid-refresh]').hidden`,
			nil,
		),
		chromedp.Evaluate(`window.htmx.trigger(document.querySelector('#workspace-content'),'sse:refresh')`, nil),
		chromedp.Poll(`!document.querySelector('[data-grid]').hasAttribute('aria-busy')`, nil),
	)
	assert(
		`document.querySelectorAll('.tabulator-row').length===0 && document.querySelector('[data-grid-refresh]').hidden`,
		"background comparison lost the installed empty baseline",
	)
	emptyGrid.Store(false)

	offline.Store(true)
	check(
		chromedp.Evaluate(`document.dispatchEvent(new FocusEvent('focusout'))`, nil),
		chromedp.Poll(`document.body.dataset.liveState==='stale'`, nil),
	)
	offline.Store(false)
	expired.Store(true)
	check(
		emulation.SetDeviceMetricsOverride(320, 800, 1, false),
		chromedp.Evaluate(`document.dispatchEvent(new FocusEvent('focusout'))`, nil),
		chromedp.Poll(
			`document.querySelector('.wake-sidebar > .wake-contact > #status-sign-in')!==null && document.querySelector('#main-navigation').hidden`,
			nil,
		),
		chromedp.Focus(".wake-contact-trigger", chromedp.ByQuery),
		chromedp.KeyEvent(kb.Tab),
		chromedp.Poll(
			`document.activeElement.id==='status-sign-in' && document.activeElement.pathname==='/login'`,
			nil,
		),
		emulation.SetDeviceMetricsOverride(1440, 1000, 1, false),
	)
	expired.Store(false)

	check(
		testbrowser.NewDocument(chromedp.Navigate(
			origin+"/instruments/synthetic-instrument?q=synthetic&sort=location&dir=asc",
		)),
		chromedp.WaitVisible(".tabulator-row", chromedp.ByQuery),
	)
	check(
		chromedp.Evaluate(
			`window.observationGrid=window.Tabulator.findTable('.tabulator')[0];window.observationGrid.setPageSize(1).then(()=>window.observationGrid.setPage(2))`,
			nil,
		),
		chromedp.Poll(
			`new URL(location.href).searchParams.get('page')==='2' && document.querySelectorAll('.tabulator-row').length===1`,
			nil,
		),
	)
	assert(
		`(()=>{const link=new URL(document.querySelector('.tabulator-row a').href);return !link.search&&new URL(location.href).searchParams.get('page')==='2'&&new URL(location.href).searchParams.get('dir')==='asc'})()`,
		"evidence link retained the previous observation page",
	)
	check(
		chromedp.Evaluate(`window.observationGrid.setSort('location','desc')`, nil),
		chromedp.Poll(
			`new URL(location.href).searchParams.get('dir')==='desc' && new URL(location.href).searchParams.get('page')==='1' && !document.querySelector('[data-grid]').hasAttribute('aria-busy')`,
			nil,
		),
	)
	assert(
		`(()=>{const link=new URL(document.querySelector('.tabulator-row a').href);return !link.search&&new URL(location.href).searchParams.get('page')==='1'&&new URL(location.href).searchParams.get('dir')==='desc'})()`,
		"evidence link retained the previous observation order",
	)
	check(
		chromedp.Click(`[data-refresh-accepted]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.tabulator-row`, chromedp.ByQuery),
	)
	assert(
		`new URL(location.href).searchParams.get('q')==='synthetic'&&new URL(location.href).searchParams.get('dir')==='desc'&&document.querySelector('#observation-search').value==='synthetic'`,
		"accepted-detail refresh discarded current observation state",
	)

	check(
		testbrowser.NewDocument(chromedp.Navigate(origin+"/test/reports")),
		chromedp.WaitVisible(`[data-disclosure="receipt-synthetic-receipt"]`, chromedp.ByQuery),
	)
	check(
		chromedp.Evaluate(
			`document.querySelector('[data-disclosure="receipt-synthetic-receipt"]').open=true;document.querySelector('.wake-brand').focus();document.getSelection().removeAllRanges();document.querySelector('#workspace-content').dataset.probe='pending';window.htmx.trigger(document.querySelector('#workspace-content'),'sse:refresh')`,
			nil,
		),
		chromedp.Poll(`!document.querySelector('#workspace-content').hasAttribute('data-probe')`, nil),
	)
	assert(
		`document.querySelector('[data-disclosure="receipt-synthetic-receipt"]').open`,
		"live region replacement closed a retained receipt disclosure",
	)

	for _, theme := range []string{"light", "dark"} {
		for _, width := range []int64{1440, 390} {
			extra.Store(true)

			check(
				chromedp.Evaluate(`localStorage.setItem('wake.theme',`+strconv.Quote(theme)+`)`, nil),
				emulation.SetDeviceMetricsOverride(width, 1000, 1, false),
				testbrowser.NewDocument(chromedp.Navigate(origin+"/test/observations")),
				chromedp.Poll(
					`document.querySelector('[data-grid="observations"]')?.dataset.gridReady==='true'`,
					nil,
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

			if folder := os.Getenv("WAKE_SCREENSHOT_DIR"); folder != "" {
				//nolint:gosec // Caller-selected synthetic screenshot directory.
				if err := os.MkdirAll(folder, 0o700); err != nil {
					t.Fatal(err)
				}

				var png []byte
				check(chromedp.FullScreenshot(&png, 90))
				//nolint:gosec // Caller-selected synthetic screenshot directory and fixed names.
				if err := os.WriteFile(
					filepath.Join(folder, fmt.Sprintf("m5-observation-table-%s-%d.png", theme, width)),
					png,
					0o600,
				); err != nil {
					t.Fatal(err)
				}
			}

			extra.Store(false)
			check(
				chromedp.Evaluate(
					`document.activeElement.blur();window.htmx.trigger(document.querySelector('#workspace-content'),'sse:refresh')`,
					nil,
				),
				applyGridUpdate,
				chromedp.Poll(
					`document.querySelector('.tabulator-row:last-child').textContent.includes('needs review')`,
					nil,
				),
			)
			assert(
				`!document.querySelector('.tabulator details') && Array.from(document.querySelectorAll('.tabulator-cell')).every(cell=>cell.scrollHeight<=cell.clientHeight+1) && document.querySelector('.tabulator-row [tabulator-field="location"]').textContent.endsWith('/TradeConfirm[1]')`,
				"refreshed observation metadata clipped or hidden",
			)
		}
	}

	// Reset uses fresh definitions after native persistence has loaded a layout.
	check(
		emulation.SetDeviceMetricsOverride(1440, 1000, 1, false),
		testbrowser.NewDocument(chromedp.Navigate(origin+"/test/grid")),
		chromedp.Poll(`document.querySelector('[data-grid]').dataset.gridReady==='true'`, nil),
	)
	assert(
		`window.Tabulator.findTable('.tabulator')[0].getColumn('label').getWidth()===310`,
		"saved column layout was not loaded before reset",
	)
	check(
		chromedp.Click(`[data-grid-reset]`, chromedp.ByQuery),
		chromedp.Poll(
			`document.querySelector('.tabulator-col[tabulator-field="label"]').style.width!=='310px'`,
			nil,
		),
		testbrowser.NewDocument(chromedp.Navigate(origin+"/test/grid")),
		chromedp.Poll(`document.querySelector('[data-grid]').dataset.gridReady==='true'`, nil),
	)
	assert(
		`window.Tabulator.findTable('.tabulator')[0].getColumn('label').getWidth()!==310`,
		"reset columns restored the saved layout on reload",
	)

	check(
		testbrowser.NewDocument(chromedp.Navigate(origin+"/test/correction")),
		chromedp.WaitVisible("#decision-scope", chromedp.ByQuery),
	)
	assert(
		`document.querySelector('#decision-scope').value==='' && document.querySelector('[data-correction]').hidden && !document.querySelector('#decision-value')`,
		"explicit choice or comparison",
	)
	check(
		chromedp.SetValue("#decision-scope", "price", chromedp.ByQuery),
		chromedp.Evaluate(
			`document.querySelector('#decision-scope').dispatchEvent(new Event('change',{bubbles:true}))`,
			nil,
		),
	)
	assert(
		`document.querySelector('#decision-value').inputMode==='decimal' && !document.querySelector('#decision-currency').disabled`,
		"decimal control conversion",
	)
	assert(
		`!document.querySelector('#decision-current').hidden && document.querySelector('#decision-current').textContent.includes('9007199254740992.9999999999999 USD')`,
		"exact current value missing beside edit",
	)

	chooseField := func(field string) {
		t.Helper()
		check(
			chromedp.SetValue("#decision-scope", field, chromedp.ByQuery),
			chromedp.Evaluate(
				`document.querySelector('#decision-scope').dispatchEvent(new Event('change',{bubbles:true}))`,
				nil,
			),
		)
	}
	setCorrection := func(value string) {
		t.Helper()
		check(
			chromedp.SetValue("#decision-value", value, chromedp.ByQuery),
			chromedp.Evaluate(
				`document.querySelector('#decision-value').dispatchEvent(new Event('input',{bubbles:true}))`,
				nil,
			),
		)
	}
	ready := func() {
		t.Helper()
		check(
			chromedp.Poll(
				`document.querySelector('#decision-feedback').dataset.state==='valid' && !document.querySelector('[data-correction] button[type="submit"]').disabled`,
				nil,
			),
		)
	}

	setCorrection("12x")
	assert(
		`document.querySelector('[data-correction] button[type="submit"]').disabled && document.querySelector('#decision-feedback').dataset.state==='invalid'`,
		"malformed decimal accepted locally",
	)
	setCorrection("9007199254740993.0000000000001")
	ready()
	assert(
		`document.querySelector('#decision-value').value==='9007199254740993.0000000000001'`,
		"live validation rounded a decimal",
	)
	chooseField("quantity")
	setCorrection("0")
	check(chromedp.Poll(`document.querySelector('#decision-feedback').dataset.state==='invalid'`, nil))
	assert(
		`document.querySelector('[data-correction] button[type="submit"]').disabled`,
		"zero quantity accepted by live check",
	)
	chooseField("instrument")
	check(chromedp.SendKeys("#decision-link-search", "second", chromedp.ByQuery))
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
	chooseField("trade_date")
	assert(`document.querySelector('#decision-value').type==='date'`, "calendar date control missing")
	check(
		chromedp.Evaluate(
			`document.querySelector("#decision-value").value="2026-02-30";document.querySelector("#decision-value").dispatchEvent(new Event("input",{bubbles:true}))`,
			nil,
		),
	)
	assert(
		`document.querySelector('[data-correction] button[type="submit"]').disabled`,
		"invalid calendar date accepted",
	)
	setCorrection("2028-02-29")
	ready()
	chooseField("executed_at")
	assert(
		`document.querySelector('#decision-value').type==='hidden' && document.querySelector('#decision-time').value==='13:30:00.120000000' && document.querySelector('#decision-zone').value==='Z'`,
		"timestamp precision lost while configuring controls",
	)
	check(
		chromedp.SetValue("#decision-time", "14:05:06.120000000", chromedp.ByQuery),
		chromedp.SetValue("#decision-zone", "+01:30", chromedp.ByQuery),
		chromedp.Evaluate(
			`document.querySelector('#decision-zone').dispatchEvent(new Event('input',{bubbles:true}))`,
			nil,
		),
	)
	ready()
	assert(
		`document.querySelector('#decision-value').value==='2026-10-20T14:05:06.120000000+01:30'`,
		"timestamp offset or fractional zeros changed",
	)
	offline.Store(true)
	check(
		chromedp.Evaluate(
			`document.querySelector('#decision-zone').dispatchEvent(new Event('input',{bubbles:true}))`,
			nil,
		),
		chromedp.WaitVisible("[data-check-again]", chromedp.ByQuery),
	)
	assert(
		`document.querySelector('[data-correction] button[type="submit"]').disabled`,
		"validation failure allowed saving",
	)
	offline.Store(false)
	check(chromedp.Click("[data-check-again]", chromedp.ByQuery))
	ready()
	chooseField("price")
	assert(
		`document.querySelector('#decision-value').value==='9007199254740993.0000000000001' && document.querySelector('#decision-currency').value==='USD'`,
		"field-specific draft lost on switching",
	)
	ready()
	assert(
		`!document.querySelector('#workspace-content').hasAttribute('hx-trigger')`,
		"correction form is not a stable review snapshot",
	)
	check(
		testbrowser.NewDocument(chromedp.Navigate(origin+"/test/correction?mode=evidence")),
		chromedp.WaitVisible("#decision-candidate", chromedp.ByQuery),
	)
	assert(
		`document.querySelector('#decision-candidate').value==='' && !document.querySelector('[data-candidate]')`,
		"candidate selected implicitly",
	)
	check(
		chromedp.SetValue("#decision-candidate", "candidate-one", chromedp.ByQuery),
		chromedp.Click(`form[aria-label="Compare retained evidence"] button`, chromedp.ByQuery),
		chromedp.WaitVisible("[data-candidate]", chromedp.ByQuery),
	)
	assert(
		`document.querySelector('[data-different="true"]').textContent.includes('9007199254740993.0000000000001')`,
		"exact candidate comparison missing",
	)

	check(
		testbrowser.NewDocument(
			chromedp.Navigate(origin+"/evidence/synthetic-report"),
		),
		chromedp.Poll(`document.querySelector('[data-grid]').dataset.gridReady==='true'`, nil),
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
		chromedp.Evaluate(
			`window.reportTable=window.Tabulator.findTable('.tabulator')[0]; window.reportTable.setPageSize(25).then(()=>window.reportTable.setPage(2))`,
			nil,
		),
		chromedp.Poll(
			`document.querySelector('[data-grid-status]').textContent.includes('75 of 75') && new URL(location.href).searchParams.get('page')==='2'`,
			nil,
		),
	)
	assert(
		`document.querySelectorAll('.tabulator-row').length===25`,
		"report table paging lost bounds",
	)
	check(chromedp.SetValue("#observation-search", "no additional contribution", chromedp.ByQuery),
		chromedp.Click("[data-observation-filters] button", chromedp.ByQuery),
		chromedp.Poll(`document.querySelector('[data-grid-status]').textContent==='50 of 75 results'`, nil))
	assert(
		`[...document.querySelectorAll('.tabulator-row [tabulator-field="outcome"]')].every(cell=>cell.textContent==='no additional contribution')`,
		"report contribution label was not displayed or searchable",
	)
	check(chromedp.SetValue("#observation-search", "TradeConfirm[75]", chromedp.ByQuery),
		chromedp.Click("[data-observation-filters] button", chromedp.ByQuery),
		chromedp.Poll(`document.querySelector('[data-grid-status]').textContent==='1 of 75 results'`, nil))
	assert(
		`document.querySelector('.tabulator-row').textContent.includes('/r[1]'.repeat(200)+'/TradeConfirm[75]')`,
		"long source path is missing from filtered observation",
	)
	check(
		chromedp.Focus(`.tabulator-col[tabulator-field="location"]`, chromedp.ByQuery),
		chromedp.KeyEvent(kb.Enter),
		chromedp.Poll(
			`new URL(location.href).searchParams.get('sort')==='location' && document.querySelector('[data-grid]').dataset.gridReady==='true'`,
			nil,
		),
		chromedp.Evaluate(
			`window.previousObservationColumns=localStorage.getItem('tabulator-wake-observations-columns');window.reportTable.getColumn('location').setWidth(410)`,
			nil,
		),
	)
	assert(
		`localStorage.getItem('tabulator-wake-observations-artifact-columns')!==null && localStorage.getItem('tabulator-wake-observations-columns')===window.previousObservationColumns`,
		"report column preferences overwrote the ordinary observation layout",
	)
	check(
		testbrowser.NewDocument(chromedp.Navigate(origin+"/system")),
		chromedp.WaitVisible("h1", chromedp.ByQuery),
	)
	assert(
		`document.body.textContent.includes('128.4 MiB (134637824 bytes)') && !document.body.textContent.includes('Operator workspaces')`,
		"status size presentation missing",
	)
	warehouseBytes.Store(268435456)
	check(
		chromedp.Poll(
			`document.body.textContent.includes('256.0 MiB (268435456 bytes)')`,
			nil,
			chromedp.WithPollingTimeout(15*time.Second),
		),
	)
	check(
		testbrowser.NewDocument(
			chromedp.Click(`nav[aria-label="System views"] a[href="/system/configuration"]`, chromedp.ByQuery),
		),
		chromedp.WaitVisible(`//h1[text()='Configuration']`, chromedp.BySearch),
	)
	assert(
		`location.pathname==='/system/configuration'&&!location.search&&document.querySelector('.wake-nav-link[aria-current="page"]').getAttribute('href')==='/system'`,
		"configuration lost its resource path or System navigation",
	)
	check(
		testbrowser.NewDocument(
			chromedp.Click(`nav[aria-label="System views"] a[href="/system"]`, chromedp.ByQuery),
		),
		chromedp.WaitVisible(`//h1[text()='Daemon status']`, chromedp.BySearch),
	)

	// Review every representative page in both themes, with layout checks at all selected widths.
	for _, theme := range []string{"light", "dark"} {
		check(chromedp.Evaluate(`localStorage.setItem('wake.theme',`+strconv.Quote(theme)+`)`, nil))

		for _, width := range []int64{320, 768, 1024, 1440} {
			for _, path := range []string{"/login", "/test/overview", "/test/connections", "/test/source", "/test/access", "/test/reports", "/test/new", "/test/reference", "/test/reference-evidence", "/test/bill", "/test/instrument", "/test/recovery", "/evidence/synthetic-report", "/evidence/empty-report", "/test/grid", "/test/correction", "/test/correction?field=price", "/test/correction?field=instrument", "/test/correction?field=executed_at", "/test/correction?mode=evidence&candidate=candidate-one", "/test/correction?mode=decisions", "/system", "/system/configuration", "/system/settings"} {
				check(
					emulation.SetDeviceMetricsOverride(width, 1000, 1, false),
					testbrowser.NewDocument(chromedp.Navigate(origin+path)),
					chromedp.WaitVisible("h1", chromedp.ByQuery),
					chromedp.Poll(`document.documentElement.dataset.wakeReady==='true'`, nil),
				)

				if strings.Contains(path, "?field=") {
					check(chromedp.Poll(`document.querySelector('#decision-feedback').dataset.state==='valid'`, nil))
				}

				if strings.HasPrefix(path, "/evidence/") {
					check(chromedp.Poll(`document.querySelector('[data-grid]').dataset.gridReady==='true'`, nil))
					assert(
						`window.Tabulator.findTable('.tabulator')[0].getColumns().filter(c=>c.isVisible()).length===4`,
						"report table lost its four-column layout",
					)
				}

				if path == "/test/grid" {
					check(chromedp.Poll(`document.querySelector('[data-grid]').dataset.gridReady==='true'`, nil))
					assert(
						`(()=>{const s=document.querySelector('.tabulator-page-size');const r=s.getBoundingClientRect();const table=s.closest('.tabulator').getBoundingClientRect();return r.left>=0 && r.right<=innerWidth && r.top>=table.top && r.bottom<=table.bottom && r.height>=40 && getComputedStyle(s).color===getComputedStyle(document.body).color})()`,
						fmt.Sprintf("page-size control clipped or unthemed %s %d", theme, width),
					)

					if width == 1440 {
						check(
							chromedp.Evaluate(
								`document.querySelector('[data-grid]').scrollIntoView({block:'start'})`,
								nil,
							),
						)
						assert(
							`(()=>{const holder=document.querySelector('.tabulator-tableholder');const bounds=holder.getBoundingClientRect();return holder.clientHeight>512 && [...holder.querySelectorAll('.tabulator-row')].filter(row=>{const r=row.getBoundingClientRect();return r.top>=Math.max(0,bounds.top) && r.bottom<=Math.min(innerHeight,bounds.bottom)}).length>=10})()`,
							fmt.Sprintf("desktop grid did not show more rows %s", theme),
						)
						check(chromedp.Evaluate(`
							const holder=document.querySelector('.tabulator-tableholder');
							window.gridChrome=[...document.querySelectorAll('[data-list-filters],.tabulator-header,.tabulator-footer')].map(n=>n.getBoundingClientRect().top);
							holder.scrollTop=holder.scrollHeight;
						`, nil))
						assert(
							`(()=>{const holder=document.querySelector('.tabulator-tableholder');const last=holder.querySelector('.tabulator-row:last-child').getBoundingClientRect();return holder.scrollTop>0 && last.bottom<=holder.getBoundingClientRect().bottom+1 && [...document.querySelectorAll('[data-list-filters],.tabulator-header,.tabulator-footer')].every((n,i)=>Math.abs(n.getBoundingClientRect().top-window.gridChrome[i])<1)})()`,
							fmt.Sprintf("scrolling rows moved filters, headings or pagination %s", theme),
						)
						check(chromedp.Evaluate(`document.querySelector('.tabulator-tableholder').scrollTop=0`, nil))
					}
				}

				if path == "/test/reference-evidence" {
					check(
						chromedp.Click("#original-content summary", chromedp.ByQuery),
						chromedp.WaitVisible(`pre[aria-label="Retained response"]`, chromedp.ByQuery),
					)
				}

				if path == "/test/bill" {
					assert(
						`!document.body.textContent.includes('Unknown')&&!document.body.textContent.includes('0000-00-00')&&document.body.textContent.includes('None (Treasury bill)')&&!document.body.textContent.includes('Payments per year')`,
						"bill inapplicable fields",
					)
				}

				if path == "/system/configuration" {
					assert(
						`document.querySelector('dl').textContent.includes('/synthetic/operator configuration/private instance/settings.toml')`,
						"loaded TOML file path missing from configuration",
					)
				}

				if path == "/test/instrument" {
					check(
						chromedp.Click(
							`//summary[text()='Reported references and reference evidence']`,
							chromedp.BySearch,
						),
					)
					assert(
						`document.querySelector('details').textContent.includes('Composite FIGI: synthetic-composite')`,
						"reported FIGI classification",
					)
				}

				assert(
					`document.documentElement.scrollWidth<=innerWidth+1`,
					fmt.Sprintf("overflow %s %d %s", theme, width, path),
				)
				assert(
					`[...document.querySelectorAll('a[href],form[action]')].every(node=>{const q=new URL(node.href||node.action,location.href).searchParams;return !['origin','parent','page_url','location'].some(key=>q.has(key))})&&!document.querySelector('input[name="origin"],input[name="parent"],input[name="page_url"]')`,
					"rendered navigation contains referring-page parameters",
				)

				if strings.Contains(path, "mode=evidence") {
					assert(
						`document.querySelector('.wake-comparison caption').getBoundingClientRect().width>100`,
						fmt.Sprintf("collapsed comparison caption %s %d", theme, width),
					)
				}

				if width == 320 || width == 1440 || path == "/test/grid" ||
					width == 768 &&
						(strings.HasPrefix(path, "/evidence/") || path == "/system" || path == "/system/configuration") {
					if folder := os.Getenv("WAKE_SCREENSHOT_DIR"); folder != "" {
						//nolint:gosec // Caller-selected synthetic screenshot directory.
						if err := os.MkdirAll(folder, 0o700); err != nil {
							t.Fatal(err)
						}

						name := "m5-" + theme + "-" + fmt.Sprint(
							width,
						) + strings.NewReplacer("/", "-", "?", "-", "&", "-", "=", "-").
							Replace(path)
						capture := func(suffix string) {
							var png []byte
							check(chromedp.FullScreenshot(&png, 90))
							//nolint:gosec // Caller-selected synthetic screenshot directory and fixed page names.
							if err := os.WriteFile(filepath.Join(folder, name+suffix+".png"), png, 0o600); err != nil {
								t.Fatal(err)
							}
						}
						capture("")

						if path == "/system/settings" && width == 1440 {
							check(emulation.SetDeviceMetricsOverride(width, 420, 1, false))
							capture("-short")
						}

						if path == "/system/settings" && width == 320 {
							check(
								emulation.SetDeviceMetricsOverride(width, 1500, 1, false),
								chromedp.Evaluate(`document.documentElement.style.fontSize='200%'`, nil),
								testbrowser.SettleAnimations(),
							)
							capture("-text-200")
						}
					}
				}
			}
		}
	}

	check(
		emulation.SetDeviceMetricsOverride(1280, 420, 1, false),
		testbrowser.NewDocument(chromedp.Navigate(origin+"/system/settings")),
		chromedp.WaitVisible(".wake-sidebar .wake-contact", chromedp.ByQuery),
	)
	assert(`(()=>{
		const nav=document.querySelector('#main-navigation');
		const contact=document.querySelector('.wake-contact');
		const before=contact.getBoundingClientRect();
		nav.scrollTop=nav.scrollHeight;
		const after=contact.getBoundingClientRect();
		return nav.scrollTop>0 && before.top===after.top && after.bottom<=innerHeight && innerHeight-after.bottom<40;
	})()`, "status moved when scrolling short desktop navigation")
	check(chromedp.Focus(".wake-contact-trigger", chromedp.ByQuery))
	assert(`(()=>{
		const tooltip=document.querySelector('#status-contact-detail');
		const bounds=tooltip.getBoundingClientRect();
		const sidebar=tooltip.closest('.wake-sidebar').getBoundingClientRect();
		return getComputedStyle(tooltip).visibility==='visible' && bounds.top>=sidebar.top && bounds.right<=sidebar.right &&
			bounds.bottom<=document.querySelector('.wake-contact-trigger').getBoundingClientRect().top;
	})()`, "desktop contact tooltip clipped or below the trigger")
	check(chromedp.KeyEvent(kb.Escape))
	assert(
		`getComputedStyle(document.querySelector('#status-contact-detail')).visibility==='hidden' && document.activeElement.matches('.wake-contact-trigger')`,
		"Escape did not dismiss contact tooltip and retain focus",
	)

	check(
		emulation.SetDeviceMetricsOverride(320, 800, 1, false),
		testbrowser.NewDocument(chromedp.Navigate(origin+"/test/correction")),
		chromedp.WaitVisible("h1", chromedp.ByQuery),
	)
	assert(
		`document.querySelector('#main-navigation').hidden && document.querySelector('.wake-sidebar > .wake-contact').getBoundingClientRect().height>0`,
		"narrow navigation must collapse independently of status",
	)
	check(
		chromedp.Click(".wake-menu-toggle", chromedp.ByQuery),
		chromedp.Evaluate(
			`document.dispatchEvent(new KeyboardEvent('keydown',{key:'Escape',bubbles:true}))`,
			nil,
		),
	)
	assert(
		`document.querySelector('#main-navigation').hidden && document.activeElement.matches('.wake-menu-toggle')`,
		"Escape focus return",
	)
	check(chromedp.Focus(".wake-contact-trigger", chromedp.ByQuery))
	assert(`(()=>{
		const tooltip=document.querySelector('#status-contact-detail');
		const bounds=tooltip.getBoundingClientRect();
		return getComputedStyle(tooltip).visibility==='visible' && bounds.left>=0 && bounds.right<=innerWidth &&
			bounds.top>=document.querySelector('.wake-contact-trigger').getBoundingClientRect().bottom;
	})()`, "narrow contact tooltip clipped or above the trigger")
	// Text resizing stresses long exact values and native controls independently of display density.
	for _, path := range []string{"/test/correction", "/test/correction?field=price", "/test/correction?field=instrument", "/test/correction?field=executed_at", "/test/correction?mode=evidence&candidate=candidate-one", "/test/connections", "/test/grid", "/system/settings"} {
		check(
			testbrowser.NewDocument(chromedp.Navigate(origin+path)),
			chromedp.WaitVisible("h1", chromedp.ByQuery),
			chromedp.Evaluate(`document.documentElement.style.fontSize='200%'`, nil),
			chromedp.Evaluate(
				`new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve)))`,
				nil,
				func(p *runtime.EvaluateParams) *runtime.EvaluateParams { return p.WithAwaitPromise(true) },
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

	check(
		testbrowser.NewDocument(chromedp.Navigate(origin+"/test/grid")),
		chromedp.WaitVisible(".tabulator", chromedp.ByQuery),
	)
	check(
		chromedp.Evaluate(
			`(()=>{const h=document.querySelector('.tabulator-col[tabulator-field="label"]');h.focus();window.beforeWidth=window.Tabulator.findTable('.tabulator')[0].getColumn('label').getWidth();h.dispatchEvent(new KeyboardEvent('keydown',{key:'ArrowRight',altKey:true,bubbles:true}))})()`,
			nil,
		),
	)
	assert(
		`window.Tabulator.findTable('.tabulator')[0].getColumn('label').getWidth()===beforeWidth+24`,
		"keyboard column resize",
	)
	check(
		chromedp.Evaluate(
			`document.querySelector('.tabulator-col[tabulator-field="price"]').dispatchEvent(new KeyboardEvent('keydown',{key:'Enter',bubbles:true}))`,
			nil,
		),
	)
	assert(
		`window.Tabulator.findTable('.tabulator')[0].getSorters().some(s=>s.field==='price')`,
		"keyboard column sorting",
	)

	for _, disabled := range []bool{false, true} {
		noGrid.Store(!disabled)

		document, closeDocument := chromedp.NewContext(ctx)

		step, stopStep := context.WithTimeout(document, 20*time.Second)
		err := chromedp.Run(
			step,
			network.SetCacheDisabled(true),
			emulation.SetScriptExecutionDisabled(disabled),
			testbrowser.NewDocument(chromedp.Navigate(origin+"/test/grid")),
			chromedp.WaitVisible("[data-grid]", chromedp.ByQuery),
		)

		var clear bool
		if err == nil {
			predicate := `!document.querySelector('table') && document.querySelector('[data-grid-status]').textContent.includes('could not load') && !document.querySelector('[data-grid-retry]').hidden`
			if disabled {
				predicate = `!document.querySelector('table') && document.querySelector('noscript').textContent.includes('JavaScript is required')`
			}

			err = chromedp.Run(step, chromedp.Poll(predicate, &clear))
		}

		stopStep()
		closeDocument()

		if err != nil || !clear {
			t.Fatal("JS requirement or component retry was unclear", disabled, err, clear)
		}
	}

	if exceptions.Load() != 0 {
		t.Fatal("browser script exceptions", exceptions.Load())
	}

	if navigationTransport.Load() {
		t.Fatal("browser requests carried navigation ancestry")
	}
}

type browserNormalizer struct{}

func (browserNormalizer) Normalize(
	context.Context,
	provider.Artifact,
	provider.Claims,
) (provider.Result, error) {
	return provider.Result{}, provider.ErrUnavailable
}

type browserStatus struct{ size *atomic.Int64 }

func (s browserStatus) Status(context.Context) (Status, error) {
	value := s.size.Load()
	return Status{WarehouseBytes: &value, CredentialProtection: true}, nil
}
