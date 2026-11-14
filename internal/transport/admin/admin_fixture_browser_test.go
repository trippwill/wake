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
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/a-h/templ"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"go.trippwill.dev/wake/financial"
	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/host"
	"go.trippwill.dev/wake/internal/testbrowser"
	"go.trippwill.dev/wake/provider"
)

// The independent browser fixture exposes only a remote page of exact values.
func syntheticActivityGrid(notes bool) templ.Component {
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

func syntheticActivityGridData(w http.ResponseWriter, r *http.Request, extra bool) {
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

func syntheticObservationGrid() templ.Component {
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

func syntheticObservationData(w http.ResponseWriter, r *http.Request, changed bool) {
	rows := []application.Observation{}

	count := 2
	artifact := r.URL.Query().Get("subject") == "artifact"
	if artifact {
		count = 75
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

const adminBrowserUnlock = "synthetic-admin-browser-unlock"

type adminBrowserFixture struct {
	t      *testing.T
	ctx    context.Context
	origin string

	warehouseBytes                                        atomic.Int64
	extra, offline, expired, noGrid, refreshFailed        atomic.Bool
	holdRefresh, holdSelection, holdMovedFocus, holdAlias atomic.Bool
	emptyGrid, failFirstPage                              atomic.Bool
	refreshStarted, releaseRefresh                        chan struct{}
	selectionStarted, releaseSelection                    chan struct{}
	movedFocusStarted, releaseMovedFocus                  chan struct{}
	aliasStarted, releaseAlias                            chan struct{}
	correctionCurrencies                                  atomic.Pointer[[]string]
}

func newAdminBrowserFixture(t *testing.T) *adminBrowserFixture {
	t.Helper()
	f := &adminBrowserFixture{
		t:                 t,
		refreshStarted:    make(chan struct{}, 1),
		releaseRefresh:    make(chan struct{}, 1),
		selectionStarted:  make(chan struct{}, 1),
		releaseSelection:  make(chan struct{}),
		movedFocusStarted: make(chan struct{}, 1),
		releaseMovedFocus: make(chan struct{}),
		aliasStarted:      make(chan struct{}, 1),
		releaseAlias:      make(chan struct{}, 1),
	}
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

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = listener.Close() })

	origin := "http://" + listener.Addr().String()

	dir := privateAuthDir(t)
	if err = SetUnlock(dir, []byte(adminBrowserUnlock)); err != nil {
		t.Fatal(err)
	}

	auth, _, err := Open(dir, origin)
	if err != nil {
		t.Fatal(err)
	}

	f.warehouseBytes.Store(134637824)
	f.origin = origin

	base, err := NewHandler(auth, Dependencies{
		Queries: browserStatus{size: &f.warehouseBytes},
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
			"date",
			"at",
			"side",
			"option.right",
			"option.expiry",
		},
		Values: map[string]application.CorrectionValue{
			"price":      {Value: "9007199254740992.9999999999999", Currency: "USD"},
			"quantity":   {Value: "0.000000000000123", Unit: "shares"},
			"instrument": {Value: "instrument-one", Label: "Synthetic first option (ONE)"},
			"date": {
				Value: "2026-10-20",
			},
			"at":           {Value: "2026-10-20T13:30:00.120000000Z"},
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

		if strings.HasSuffix(r.URL.Path, "tabulator.min.js") && f.noGrid.Load() {
			http.NotFound(w, r)
			return
		}

		if r.URL.Path == "/system/contact" {
			if f.expired.Load() {
				http.Error(w, "expired", http.StatusUnauthorized)
				return
			}

			if f.offline.Load() {
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
			f.holdRefresh.CompareAndSwap(true, false) {
			f.refreshStarted <- struct{}{}

			select {
			case <-f.releaseRefresh:
			case <-r.Context().Done():
				return
			}
		}

		if r.URL.Path == "/test/grid-data" && f.holdMovedFocus.CompareAndSwap(true, false) {
			f.movedFocusStarted <- struct{}{}

			select {
			case <-f.releaseMovedFocus:
			case <-r.Context().Done():
				return
			}
		}

		if r.URL.Path == "/test/grid-data" && f.holdSelection.CompareAndSwap(true, false) {
			f.selectionStarted <- struct{}{}

			select {
			case <-f.releaseSelection:
			case <-r.Context().Done():
				return
			}
		}

		if r.URL.Path == "/test/grid-data" && r.Header.Get("HX-Request") == "true" &&
			(f.refreshFailed.Load() || (f.failFirstPage.Load() && r.URL.Query().Get("page") == "1")) {
			http.Error(w, "Read temporarily unavailable", http.StatusServiceUnavailable)
			return
		}

		if r.URL.Path == "/activity/synthetic-activity/correct/check" {
			if _, ok := auth.session(r); !ok {
				http.Error(w, "expired", http.StatusUnauthorized)
				return
			}

			if f.offline.Load() {
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
					_, e = financial.ParsePrice(value, currency)
				}

				valid = e == nil
			case "quantity":
				q, e := financial.ParseQuantity(value)
				valid = e == nil && q.Positive()
			case "date", "option.expiry":
				_, e := financial.ParseDate(value)
				valid = e == nil || field == "option.expiry" && value == ""
			case "at":
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
			if _, ok := auth.session(r); !ok || f.expired.Load() {
				http.Error(w, "expired", http.StatusUnauthorized)
				return
			}

			if r.URL.Path == "/test/grid-data" {
				if f.emptyGrid.Load() {
					gridJSON(w, gridPage[activityGridRow]{Data: []activityGridRow{}, LastPage: 1, Total: 71}, nil)
				} else {
					syntheticActivityGridData(w, r, f.extra.Load())
				}
			} else {
				syntheticObservationData(w, r, !f.extra.Load())
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

			if r.Method == http.MethodHead && r.URL.Path == "/instruments/retired-instrument" &&
				f.holdAlias.Load() {
				select {
				case f.aliasStarted <- struct{}{}:
				case <-r.Context().Done():
					return
				}

				select {
				case <-f.releaseAlias:
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
			case "/evidence/synthetic-report":
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
				component = syntheticActivityGrid(r.URL.Path == "/test/dense-grid")
			case "/test/instruments":
				component = Grid("instruments", "/test/grid-data?view=instruments", "Synthetic instruments")
			case "/test/observations", "/instruments/synthetic-instrument":
				component = syntheticObservationGrid()
			case "/test/correction",
				"/activity/synthetic-activity/correct",
				"/instruments/synthetic-instrument/correct":
				snapshot := page(r.Context())
				if codes := f.correctionCurrencies.Load(); codes != nil {
					snapshot.CurrencyCodes = *codes
				}

				correction := form
				if r.URL.Path == "/instruments/synthetic-instrument/correct" {
					correction.Kind = "instrument"
					correction.ID = "synthetic-instrument"
				} else {
					snapshot.URL = "/activity/synthetic-activity/correct?" + r.URL.RawQuery
				}

				r = r.WithContext(context.WithValue(r.Context(), pageKey{}, snapshot))
				component = CorrectionPage(csrf, correction)
			case "/test/reports":
				page(r.Context()).Query.Set("view", "reports")

				component = ConnectionPage(csrf, fixtureSource(view), view.Connections[0])
			case "/test/reference-evidence":
				component = EvidencePage(
					application.ReferenceEvidence{
						Retrieved: "2026-11-02",
						Request:   `[{"idType":"ID_BB_GLOBAL","idValue":"synthetic-composite"}]`,
						Response:  `[{"data":[{"figi":"synthetic-composite","compositeFIGI":"synthetic-composite","shareClassFIGI":"synthetic-share"}]}]`,
					},
				)
			case "/":
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
	t.Cleanup(func() { close(eventShutdown); server.Close() })

	f.ctx = testbrowser.Start(t)

	var (
		exceptions          atomic.Int64
		navigationTransport atomic.Bool
	)
	if err := chromedp.Listen(f.ctx, func(event any) {
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
	}); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if exceptions.Load() != 0 {
			t.Error("browser script exceptions", exceptions.Load())
		}

		if navigationTransport.Load() {
			t.Error("browser requests carried navigation ancestry")
		}
	})

	f.check(
		testbrowser.SetViewport(1440, 1000),
		testbrowser.NewDocument(chromedp.Navigate(origin+"/login")),
		chromedp.WaitVisible(chromedp.CSS("#unlock")),
		chromedp.SendKeys(chromedp.CSS("#unlock"), adminBrowserUnlock),
		testbrowser.NewDocument(chromedp.Click(chromedp.CSS(`button[type="submit"]`))),
		chromedp.WaitVisible(chromedp.CSS("#main-navigation")),
	)

	return f
}

func (f *adminBrowserFixture) check(actions ...chromedp.Action[chromedp.Void]) {
	f.t.Helper()

	step, cancelStep := context.WithTimeout(f.ctx, 20*time.Second)
	defer cancelStep()

	if err := chromedp.Do(step, actions...); err != nil {
		var location, body, grid string

		diagnostic, cancel := context.WithTimeout(f.ctx, 2*time.Second)
		defer cancel()

		_ = chromedp.Do(
			diagnostic,
			testbrowser.Capture(&location, chromedp.Location()),
			testbrowser.Capture(&body, chromedp.Text(chromedp.CSS("body"))),
			testbrowser.Evaluate(
				`JSON.stringify({ready:document.querySelector('[data-grid]')?.dataset.gridReady,size:window.Tabulator?.findTable('.tabulator')[0]?.getPageSize(),rows:document.querySelectorAll('.tabulator-row').length,choice:document.querySelector('.tabulator-page-size')?.value,active:document.activeElement.tagName})`,
				&grid,
			),
		)
		f.t.Fatal("browser action:", err, location, grid, body)
	}
}

func (f *adminBrowserFixture) assert(script, message string) {
	f.t.Helper()

	var ok bool
	f.check(testbrowser.Evaluate(script, &ok))

	if !ok {
		f.t.Fatal(message)
	}
}

func (f *adminBrowserFixture) visitGrid(path string, size, rows int) {
	f.t.Helper()
	f.check(
		testbrowser.NewDocument(chromedp.Navigate(f.origin+path)),
		chromedp.Poll[chromedp.Void](fmt.Sprintf(
			`document.querySelector('[data-grid]')?.dataset.gridReady==='true' && document.querySelector('.tabulator-page-size')?.value==='%d' && document.querySelectorAll('.tabulator-row').length===%d`,
			size,
			rows,
		)),
	)
}

func (f *adminBrowserFixture) screenshot(name string) {
	f.t.Helper()

	folder := os.Getenv("WAKE_SCREENSHOT_DIR")
	if folder == "" {
		return
	}

	var png []byte
	f.check(testbrowser.Capture(&png, chromedp.FullScreenshot(90)))

	//nolint:gosec // Caller-selected synthetic screenshot directory.
	if err := os.MkdirAll(folder, 0o700); err != nil {
		f.t.Fatal(err)
	}
	//nolint:gosec // Caller-selected synthetic screenshot directory and fixed page names.
	if err := os.WriteFile(filepath.Join(folder, name+".png"), png, 0o600); err != nil {
		f.t.Fatal(err)
	}
}

func applyBrowserGridUpdate() chromedp.Action[chromedp.Void] {
	return chromedp.Steps(
		chromedp.Poll[chromedp.Void](
			`!document.querySelector('[data-grid]').hasAttribute('aria-busy') && !document.querySelector('[data-grid-refresh]').hidden`,
		),
		chromedp.Evaluate[chromedp.Void](`document.querySelector('[data-grid-refresh]').click()`),
		chromedp.Poll[chromedp.Void](
			`document.querySelector('[data-grid-refresh]').hidden && document.querySelector('[data-grid]').dataset.gridReady==='true'`,
		),
	)
}

type browserStatus struct{ size *atomic.Int64 }

func (s browserStatus) Status(context.Context) (Status, error) {
	value := s.size.Load()
	return Status{WarehouseBytes: &value, CredentialProtection: true}, nil
}
