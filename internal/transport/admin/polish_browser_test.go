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
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"go.trippwill.dev/wake/financial"
	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/provider"
)

// The independent browser fixture exposes only a remote page of exact values.
func polishGrid() templ.Component {
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

		if err := ListFilters(false).Render(ctx, w); err != nil {
			return err
		}

		return Grid("activity", "/test/grid-data", "Precision activity").Render(ctx, w)
	})
}

func polishGridData(w http.ResponseWriter, r *http.Request, extra bool) {
	params := r.URL.Query()
	page, _ := strconv.Atoi(params.Get("page"))
	size, _ := strconv.Atoi(params.Get("size"))

	query, err := application.NewListQuery(
		application.ListOptions{
			Page:      page,
			PageSize:  size,
			Search:    params.Get("q"),
			Sort:      params.Get("sort"),
			Direction: params.Get("dir"),
		},
		application.ActivityScope,
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
				Label:     gridCell{Text: name, Href: "/test/grid"},
				Date:      gridCell{Text: "2026-10-20"},
				Quantity:  gridCell{Text: "1 shares"},
				Price:     gridCell{Text: value + " USD"},
				State:     gridCell{Text: "accepted", Tone: "neutral"},
				Reference: gridCell{Text: "provisional"},
			},
		)
	}

	slices.SortFunc(rows, func(a, b activityGridRow) int {
		order := 0
		switch options.Sort {
		case "price":
			av, _ := new(big.Rat).SetString(strings.TrimSuffix(a.Price.Text, " USD"))
			bv, _ := new(big.Rat).SetString(strings.TrimSuffix(b.Price.Text, " USD"))
			order = av.Cmp(bv)
		case "label":
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
	gridJSON(
		w,
		gridResult(
			rows[start:end],
			application.PageInfo{
				Page:     options.Page,
				PageSize: options.PageSize,
				Total:    int64(count),
				Matched:  int64(matched),
			},
		),
		nil,
	)
}

func polishObservations() templ.Component {
	return templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		if err := PageHeader(
			"Instrument observations",
			"Inspect retained source locations and acceptance rules.",
		).Render(ctx, w); err != nil {
			return err
		}

		return ObservationHistory("instrument", "synthetic-instrument").Render(ctx, w)
	})
}

func polishObservationData(w http.ResponseWriter, r *http.Request, changed bool) {
	rows := []application.Observation{}
	for i := range 2 {
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

	gridJSON(
		w,
		observationGrid(
			r.Context(),
			application.ObservationList{
				Items:    rows,
				PageInfo: application.PageInfo{Page: 1, PageSize: 50, Total: 2, Matched: 2},
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

	base, err := NewHandler(auth, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}

	var extra, offline, expired, noGrid, refreshFailed, holdRefresh atomic.Bool

	refreshStarted, releaseRefresh := make(chan struct{}, 1), make(chan struct{})

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
					{Operation: "discover", Progress: "waiting", Next: "2026-10-26T12:00:00Z"},
				},
				Processing: []application.ProcessingStatus{
					{Artifact: "synthetic-report", TradeClaims: 70, Complete: true},
				},
			},
		},
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = pageRequest(r)
		if strings.HasSuffix(r.URL.Path, "tabulator.min.js") && noGrid.Load() {
			http.NotFound(w, r)
			return
		}

		if r.URL.Path == "/status/contact" {
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

		if r.URL.Path == "/test/grid-data" && r.Header.Get("HX-Request") == "true" && refreshFailed.Load() {
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
				polishGridData(w, r, extra.Load())
			} else {
				polishObservationData(w, r, !extra.Load())
			}

			return
		}

		if strings.HasPrefix(r.URL.Path, "/test/") || r.URL.Path == "/" ||
			r.URL.Path == "/activity/synthetic-activity/correct" {
			csrf, ok := auth.session(r)
			if !ok {
				http.Redirect(w, r, "/login", http.StatusSeeOther)
				return
			}

			setSecurityHeaders(w.Header())

			var component templ.Component
			switch r.URL.Path {
			case "/test/grid":
				component = polishGrid()
			case "/test/observations":
				component = polishObservations()
			case "/test/correction", "/activity/synthetic-activity/correct":
				snapshot := page(r.Context())
				snapshot.URL = "/activity/synthetic-activity/correct?" + r.URL.RawQuery
				r = r.WithContext(context.WithValue(r.Context(), pageKey{}, snapshot))
				component = CorrectionPage(csrf, form)
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
				component = DashboardOverview(fixtureOverview(view))
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

	ctx, deadline := context.WithTimeout(ctx, 150*time.Second)
	defer deadline()

	if err := chromedp.Run(ctx); err != nil {
		t.Fatal(err)
	}

	var exceptions atomic.Int64
	chromedp.ListenTarget(ctx, func(event any) {
		if _, ok := event.(*runtime.EventExceptionThrown); ok {
			exceptions.Add(1)
		}
	})

	check := func(actions ...chromedp.Action) {
		t.Helper()

		step, cancelStep := context.WithTimeout(ctx, 20*time.Second)
		defer cancelStep()

		if err := chromedp.Run(step, actions...); err != nil {
			var location, body string

			_ = chromedp.Run(ctx, chromedp.Location(&location), chromedp.Text("body", &body))
			t.Fatal(err, location, body)
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
					`JSON.stringify({viewport:innerWidth,scroll:document.documentElement.scrollWidth,client:document.documentElement.clientWidth,body:document.body.scrollWidth,elements:[...document.querySelectorAll('body *')].map(n=>({tag:n.tagName,cls:n.className,text:n.textContent.slice(0,60),scroll:n.scrollWidth,client:n.clientWidth,right:n.getBoundingClientRect().right,width:n.getBoundingClientRect().width})).filter(n=>n.width&&(n.right>innerWidth+1||n.scroll>n.client+1)).slice(0,12)})`,
					&overflow,
				),
			)
			t.Fatal(message, overflow)
		}
	}

	check(
		emulation.SetDeviceMetricsOverride(1440, 1000, 1, false),
		chromedp.Navigate(origin+"/login"),
		chromedp.WaitVisible("#unlock"),
		chromedp.SendKeys("#unlock", "synthetic-polish-unlock"),
		chromedp.Click(`button[type="submit"]`),
		chromedp.WaitVisible("#main-navigation"),
		chromedp.Evaluate(`localStorage.setItem('tabulator-wake-activity-columns','invalid JSON')`, nil),
		chromedp.Navigate(origin+"/test/grid"),
		chromedp.WaitVisible(".tabulator"),
		chromedp.Poll(`document.querySelector('.tabulator-row')!==null`, nil),
	)
	assert(
		`!document.querySelector('table') && !window.privateInjection && document.querySelector('[data-grid-status]').textContent==='70 of 70 results'`,
		"JSON grid or escaping",
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
	extra.Store(true)
	check(
		chromedp.Evaluate(`window.htmx.trigger(document.querySelector('#workspace-content'),'sse:refresh')`, nil),
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
	// A response may update rows while an unsubmitted filter draft keeps focus.
	extra.Store(false)
	holdRefresh.Store(true)
	check(
		chromedp.Evaluate(`window.htmx.trigger(document.querySelector('#workspace-content'),'sse:refresh')`, nil),
	)

	select {
	case <-refreshStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("delayed JSON refresh did not start")
	}

	check(
		chromedp.Focus("#activity-search"),
		chromedp.Evaluate(`document.querySelector('#activity-search').value='unsubmitted filter draft'`, nil),
	)
	close(releaseRefresh)
	check(
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
		chromedp.WaitVisible("[data-grid-retry]"),
	)
	assert(
		`document.querySelectorAll('.tabulator-row').length===20 && document.querySelector('[data-grid-status]').textContent.includes('Previous results') && document.querySelector('#view-notes').open`,
		"failed JSON request replaced displayed data",
	)
	refreshFailed.Store(false)
	check(
		chromedp.Click("[data-grid-retry]"),
		chromedp.Poll(
			`document.querySelector('[data-grid-status]').textContent==='71 of 71 results' && new URL(location.href).searchParams.get('page')==='2'`,
			nil,
		),
	)
	offline.Store(true)
	check(
		chromedp.Evaluate(`document.dispatchEvent(new FocusEvent('focusout'))`, nil),
		chromedp.Poll(`document.body.dataset.liveState==='stale'`, nil),
	)
	offline.Store(false)
	expired.Store(true)
	check(
		chromedp.Evaluate(`document.dispatchEvent(new FocusEvent('focusout'))`, nil),
		chromedp.Poll(
			`document.querySelector('#status-sign-in')!==null && document.querySelector('#status-sign-in').parentElement.classList.contains('wake-contact')`,
			nil,
		),
	)
	expired.Store(false)

	for _, theme := range []string{"light", "dark"} {
		for _, width := range []int64{1440, 390} {
			extra.Store(true)

			check(
				chromedp.Evaluate(`localStorage.setItem('wake.theme',`+strconv.Quote(theme)+`)`, nil),
				emulation.SetDeviceMetricsOverride(width, 1000, 1, false),
				chromedp.Navigate(origin+"/test/observations"),
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

	check(chromedp.Navigate(origin+"/test/correction"), chromedp.WaitVisible("#decision-scope"))
	assert(
		`document.querySelector('#decision-scope').value==='' && document.querySelector('[data-correction]').hidden && !document.querySelector('#decision-value')`,
		"explicit choice or comparison",
	)
	check(
		chromedp.SetValue("#decision-scope", "price"),
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
			chromedp.SetValue("#decision-scope", field),
			chromedp.Evaluate(
				`document.querySelector('#decision-scope').dispatchEvent(new Event('change',{bubbles:true}))`,
				nil,
			),
		)
	}
	setCorrection := func(value string) {
		t.Helper()
		check(
			chromedp.SetValue("#decision-value", value),
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
	check(chromedp.SendKeys("#decision-link-search", "second"))
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
		chromedp.SetValue("#decision-time", "14:05:06.120000000"),
		chromedp.SetValue("#decision-zone", "+01:30"),
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
		chromedp.WaitVisible("[data-check-again]"),
	)
	assert(
		`document.querySelector('[data-correction] button[type="submit"]').disabled`,
		"validation failure allowed saving",
	)
	offline.Store(false)
	check(chromedp.Click("[data-check-again]"))
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
		chromedp.Navigate(origin+"/test/correction?mode=evidence"),
		chromedp.WaitVisible("#decision-candidate"),
	)
	assert(
		`document.querySelector('#decision-candidate').value==='' && !document.querySelector('[data-candidate]')`,
		"candidate selected implicitly",
	)
	check(
		chromedp.SetValue("#decision-candidate", "candidate-one"),
		chromedp.Click(`form[aria-label="Compare retained evidence"] button`),
		chromedp.WaitVisible("[data-candidate]"),
	)
	assert(
		`document.querySelector('[data-different="true"]').textContent.includes('9007199254740993.0000000000001')`,
		"exact candidate comparison missing",
	)
	// Review every representative page in both themes, with layout checks at all selected widths.
	for _, theme := range []string{"light", "dark"} {
		check(chromedp.Evaluate(`localStorage.setItem('wake.theme',`+strconv.Quote(theme)+`)`, nil))

		for _, width := range []int64{320, 768, 1024, 1440} {
			for _, path := range []string{"/login", "/test/overview", "/test/connections", "/test/source", "/test/access", "/test/reports", "/test/new", "/test/reference", "/test/recovery", "/test/grid", "/test/correction", "/test/correction?field=price", "/test/correction?field=instrument", "/test/correction?field=executed_at", "/test/correction?mode=evidence&candidate=candidate-one", "/test/correction?mode=decisions", "/status", "/configuration"} {
				check(
					emulation.SetDeviceMetricsOverride(width, 1000, 1, false),
					chromedp.Navigate(origin+path),
					chromedp.WaitVisible("h1"),
					chromedp.Poll(`document.documentElement.dataset.wakeReady==='true'`, nil),
				)

				if strings.Contains(path, "?field=") {
					check(chromedp.Poll(`document.querySelector('#decision-feedback').dataset.state==='valid'`, nil))
				}

				assert(
					`document.documentElement.scrollWidth<=innerWidth+1`,
					fmt.Sprintf("overflow %s %d %s", theme, width, path),
				)

				if strings.Contains(path, "mode=evidence") {
					assert(
						`document.querySelector('.wake-comparison caption').getBoundingClientRect().width>100`,
						fmt.Sprintf("collapsed comparison caption %s %d", theme, width),
					)
				}

				if width == 320 || width == 1440 {
					if folder := os.Getenv("WAKE_SCREENSHOT_DIR"); folder != "" {
						var png []byte
						check(chromedp.FullScreenshot(&png, 90))

						//nolint:gosec // Caller-selected synthetic screenshot directory.
						if err := os.MkdirAll(folder, 0o700); err != nil {
							t.Fatal(err)
						}

						name := "m5-" + theme + "-" + fmt.Sprint(
							width,
						) + strings.NewReplacer("/", "-", "?", "-", "&", "-", "=", "-").
							Replace(path) +
							".png"
						//nolint:gosec // Caller-selected synthetic screenshot directory and fixed page names.
						if err := os.WriteFile(filepath.Join(folder, name), png, 0o600); err != nil {
							t.Fatal(err)
						}
					}
				}
			}
		}
	}

	check(
		emulation.SetDeviceMetricsOverride(320, 800, 1, false),
		chromedp.Navigate(origin+"/test/correction"),
		chromedp.WaitVisible("h1"),
	)
	assert(`document.querySelector('#main-navigation').hidden`, "narrow navigation consumes viewport")
	check(
		chromedp.Click(".wake-menu-toggle"),
		chromedp.Evaluate(
			`document.dispatchEvent(new KeyboardEvent('keydown',{key:'Escape',bubbles:true}))`,
			nil,
		),
	)
	assert(
		`document.querySelector('#main-navigation').hidden && document.activeElement.matches('.wake-menu-toggle')`,
		"Escape focus return",
	)
	// Text resizing stresses long exact values and native controls independently of display density.
	for _, path := range []string{"/test/correction", "/test/correction?field=price", "/test/correction?field=instrument", "/test/correction?field=executed_at", "/test/correction?mode=evidence&candidate=candidate-one", "/test/connections", "/test/grid"} {
		check(
			chromedp.Navigate(origin+path),
			chromedp.WaitVisible("h1"),
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

	check(chromedp.Navigate(origin+"/test/grid"), chromedp.WaitVisible(".tabulator"))
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
			chromedp.Navigate(origin+"/test/grid"),
			chromedp.WaitVisible("[data-grid]"),
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
}

type browserNormalizer struct{}

func (browserNormalizer) Normalize(
	context.Context,
	provider.Artifact,
	provider.Claims,
) (provider.Result, error) {
	return provider.Result{}, provider.ErrUnavailable
}
