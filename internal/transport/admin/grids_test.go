// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"go.trippwill.dev/wake/financial"
	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/application/calculation"
	"go.trippwill.dev/wake/internal/host"
	"go.trippwill.dev/wake/source"
)

type gridReadStore struct {
	workspaceStore
	failure  error
	location string
}

func (s gridReadStore) ActivityList(
	context.Context,
	application.ListQuery,
) (application.ActivityList, error) {
	return application.ActivityList{}, s.failure
}

func (s gridReadStore) ObservationList(
	_ context.Context,
	_, _ string,
	query application.ListQuery,
) (application.ObservationList, error) {
	location := s.location
	if location == "" {
		location = "row[2]&field=value"
	}

	return application.ObservationList{
		PageInfo: application.PageInfo{Total: 3, Matched: 2, Page: 1, PageSize: query.Options().PageSize},
		Items: []application.Observation{
			{
				Artifact:   "synthetic-report",
				ActivityID: "synthetic-activity",
				Version:    "rule-v1",
				Location:   location,
				Rule:       "synthetic-rule",
				Retrieved:  "2026-10-31T10:00:00.120Z",
				Outcome:    calculation.ObservationAccepted,
			},
			{
				Artifact:  "synthetic-report",
				Version:   "rule-v1",
				Location:  "row[3]",
				Rule:      "synthetic-rule",
				Retrieved: "2026-10-31T10:00:00.120Z",
				Outcome:   calculation.ObservationEnriched,
			},
		},
	}, s.failure
}

func TestAuthenticatedGridResponsesAndFailures(t *testing.T) {
	dir := privateAuthDir(t)
	if err := SetPassword(dir, []byte("synthetic-grid-unlock")); err != nil {
		t.Fatal(err)
	}

	auth, _, err := Open(dir, "http://127.0.0.1:8787")
	if err != nil {
		t.Fatal(err)
	}

	session := httptest.NewRecorder()
	issueTestSession(t, auth, session)

	longLocation := strings.Repeat("/r[1]", 200)

	for _, tc := range []struct {
		name, route, location string
		status                int
		failure               error
	}{
		{name: "empty activity", route: "/grids/activity", status: 200},
		{name: "empty instruments", route: "/grids/instruments", status: 200},
		{name: "activity appearances", route: "/grids/observations?subject=activity&id=one", status: 200},
		{name: "instrument appearances", route: "/grids/observations?subject=instrument&id=two", status: 200},
		{name: "artifact appearances", route: "/grids/observations?subject=artifact&id=report", status: 200},
		{name: "navigation context is ignored", route: "/grids/activity?origin=https://outside.invalid&parent=//outside.invalid&page_url=https://outside.invalid/activity", status: 200},
		{name: "maximum artifact location", route: "/grids/observations?subject=artifact&id=report", location: strings.Repeat("/[", 512), status: 200},
		{name: "long artifact location", route: "/grids/observations?subject=artifact&id=report", location: longLocation, status: 200},
		{name: "removed reference ordering", route: "/grids/observations?subject=artifact&id=report&sort=referenced", status: 400},
		{name: "invalid page", route: "/grids/activity?page=abc", status: 400},
		{name: "invalid size", route: "/grids/instruments?size=abc", status: 400},
		{name: "unbounded size", route: "/grids/activity?size=201", status: 400},
		{name: "unknown ordering", route: "/grids/observations?sort=arbitrary", status: 400},
		{name: "missing record", route: "/grids/observations?subject=activity&id=missing", status: 404, failure: application.ErrNotFound},
		{name: "unavailable store", route: "/grids/activity", status: 503, failure: errors.New("synthetic-private-storage-diagnostic")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workflow := application.New(
				gridReadStore{failure: tc.failure, location: tc.location},
				nil,
				nil,
				nil,
				nil,
				nil,
			)

			handler, err := NewHandler(auth, Dependencies{Workflow: workflow})
			if err != nil {
				t.Fatal(err)
			}

			request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8787"+tc.route, nil)
			request.AddCookie(session.Result().Cookies()[0])
			request.Header.Set("HX-Request", "true")

			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)

			if response.Code != tc.status || response.Header().Get("Cache-Control") != "no-store" ||
				response.Header().Get("Content-Type") != "application/json" ||
				!json.Valid(response.Body.Bytes()) {
				t.Fatal(response.Code, response.Header(), response.Body.String())
			}

			if tc.status != 200 {
				var failure struct {
					Message string `json:"message"`
				}
				if err := json.Unmarshal(
					response.Body.Bytes(),
					&failure,
				); err != nil || failure.Message == "" ||
					strings.Contains(response.Body.String(), "synthetic-private") {
					t.Fatal("unsafe or incomplete error response", response.Body.String(), err)
				}

				return
			}

			var page gridPage[observationGridRow]
			if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil || page.LastPage != 1 {
				t.Fatal(response.Body.String(), err)
			}

			if !strings.Contains(tc.route, "observations") {
				if len(page.Data) != 0 || page.LastRow != 0 {
					t.Fatal(page)
				}

				return
			}

			location := tc.location
			if location == "" {
				location = "row[2]&field=value"
			}

			if len(page.Data) != 2 || page.LastRow != 2 || page.Total != 3 || page.Data[0].ID == page.Data[1].ID ||
				page.Data[0].Report.Text != "2026-10-31T10:00:00.120Z" ||
				page.Data[0].Location.Text != location ||
				page.Data[0].Rule.Text != "synthetic-rule · rule-v1" {
				t.Fatal(page)
			}

			link, err := url.Parse(page.Data[0].Report.Href)
			if err != nil || link.Path != "/evidence/synthetic-report" ||
				link.RawQuery != "" {
				t.Fatal(link, err)
			}

			if strings.Contains(tc.route, "subject=artifact") {
				if page.Data[0].Activity.Href == "" ||
					page.Data[1].Activity.Text != "Not linked" {
					t.Fatal("report observation links or context missing", page)
				}
			}

			if len(link.Query()) != 0 || page.Data[0].Activity.Href != "/activity/synthetic-activity" {
				t.Fatal("grid links contain navigation transport", link, page.Data[0].Activity)
			}
		})
	}
}

type sourceEntryReadStore struct {
	workspaceStore

	detail application.ExecutionDetail
}

func (s sourceEntryReadStore) ExecutionDetail(context.Context, string) (application.ExecutionDetail, error) {
	return s.detail, nil
}

func TestReportEntryAndTradeShowExactTreasuryBillTerms(t *testing.T) {
	usd, _ := financial.ParseCurrency("USD")
	nominal, _ := financial.ParseQuantity("10000")
	price, _ := financial.ParsePrice("98.7654321", usd)
	principal, _ := financial.ParseMoney("9876.54", usd)
	date, _ := financial.ParseDate("2026-10-20")
	account, _ := source.NewReference("ibkr", "account", "", "synthetic")
	instrumentID, _ := source.NewReference("ibkr", "conid", "", "synthetic-bill")
	executionID, _ := source.NewReference("ibkr", "execution-id", "synthetic", "fill-123")
	tradeID, _ := source.NewReference("ibkr", "trade-id", "synthetic", "trade-456")
	debt, _ := source.NewDebtTerms(source.DebtInput{Type: source.TreasuryBill, Currency: usd})
	instrument, _ := source.NewInstrument(source.InstrumentInput{
		Kind: source.Debt, Name: "Synthetic bill", Reference: instrumentID, Debt: debt,
	})

	trade, err := source.NewTrade(source.TradeInput{
		Location: "synthetic-row", Side: source.Buy, Account: account, Instrument: instrument,
		Event: executionID, EventAliases: []source.Reference{tradeID}, EventDistinct: true,
		Date: date, Quantity: nominal, Price: price, GrossAmount: principal,
	})
	if err != nil {
		t.Fatal(err)
	}

	row := application.Observation{
		Artifact:          "synthetic-report",
		Version:           "report-v1",
		Location:          "synthetic-row",
		Reported:          trade.Observation(),
		EarlierAppearance: true,
		Outcome:           calculation.ObservationNoContribution,
	}
	grid := observationGrid(application.ObservationList{Items: []application.Observation{row}}, "artifact").Data[0]
	if grid.Date.Text != "2026-10-20" ||
		grid.Location.Note != "Execution ID fill-123\nBroker trade ID trade-456" ||
		grid.Terms.Text != "10000 nominal · 98.7654321 USD per 100 face value · Reported principal 9876.54 USD" ||
		!strings.Contains(grid.Outcome.Note, "earlier report") {
		t.Fatal(grid)
	}

	activity := application.ActivityList{Items: []application.ActivityListItem{{
		AccountProvider: "ibkr-flex",
		Resource: application.CanonicalResource{ID: "execution", Activity: &application.PublishedActivity{
			PublishedActivityHeader: application.PublishedActivityHeader{Family: "trade", Date: "2026-10-20"},
			Trade: &application.PublishedTrade{
				Price: &application.PublishedMoney{Amount: "98.7654321", Currency: "USD"},
				InstrumentTerms: application.PublishedInstrumentTerms{
					Family: "debt", Debt: &application.PublishedDebt{Type: new(string(source.TreasuryBill))},
				},
			},
		}},
	}}}
	if cell := activityGrid(activity).Data[0].Price; cell.Text != "98.7654321 USD" ||
		cell.Note != "per 100 face value" {
		t.Fatal("Activity grid lost exact Treasury bill price basis", cell)
	}

	entry := sourceEvidence(
		t.Context(),
		nil,
		host.Retained{},
		application.SourceObservation{Observation: row},
		"price",
	)

	var html strings.Builder
	if err := RetainedSourceEntry(entry).Render(t.Context(), &html); err != nil {
		t.Fatal(err)
	}

	facts := []string{
		"10000 nominal", "98.7654321 USD", "per 100 face value", "Reported principal", "9876.54 USD",
		"Execution ID fill-123", "Broker trade ID trade-456",
	}
	for _, fact := range facts {
		if !strings.Contains(html.String(), fact) {
			t.Fatal("missing reported term", fact, html.String())
		}
	}

	if strings.Contains(html.String(), ">Accepted</span>") {
		t.Fatal("unlinked report entry showed accepted terms", html.String())
	}

	state, _ := calculation.NewActivityState(calculation.Accepted, 0, calculation.AutomaticSelection)

	execution, err := application.NewExecution("execution", "account", "bill", "", "test-rule", state, trade, 1)
	if err != nil {
		t.Fatal(err)
	}

	html.Reset()

	if err := ExecutionPage(
		application.ExecutionDetail{Execution: execution},
		nil,
	).Render(t.Context(), &html); err != nil {
		t.Fatal(err)
	}

	for _, fact := range facts {
		if !strings.Contains(html.String(), fact) {
			t.Fatal("missing accepted term", fact, html.String())
		}
	}

	corrected := trade.Fields()
	corrected.Quantity, _ = financial.ParseQuantity("9000.000")
	corrected.GrossAmount, _ = financial.ParseMoney("8888.89", usd)

	acceptedTrade, err := source.NewTrade(corrected)
	if err != nil {
		t.Fatal(err)
	}

	accepted, err := application.NewExecution(
		"execution",
		"account",
		"bill",
		"",
		"test-rule",
		state,
		acceptedTrade,
		2,
	)
	if err != nil {
		t.Fatal(err)
	}

	reportedGain, _ := financial.ParseMoney("12.3456700", usd)
	reportDate, _ := financial.ParseDate("2026-10-21")
	gainField, _ := source.NewActivityField(source.ActivityFieldInput{
		Name: source.FieldRealizedPnL, Money: reportedGain, ReportedOn: reportDate,
	})
	costField, _ := source.NewActivityField(source.ActivityFieldInput{
		Name: source.FieldReportedCostBasis, Money: principal, ReportedOn: reportDate,
	})
	enriched := row.Reported.Fields()
	enriched.Fields = append(enriched.Fields, gainField, costField)

	row.Reported, err = source.NewActivityObservation(enriched)
	if err != nil {
		t.Fatal(err)
	}

	row.ActivityID = accepted.ID()
	store := sourceEntryReadStore{detail: application.ExecutionDetail{
		Execution: accepted,
		Context: application.ActivityContext{
			Realized: &application.PublishedRealizedPnL{
				Amount: &application.PublishedMoney{
					Amount:   "12.3456789",
					Currency: "USD",
				},
				ReportDate: new("2026-10-22"),
				CostBasis:  &application.PublishedMoney{Amount: "9876.54", Currency: "GBP"},
			},
		},
	}}
	workflow := application.New(store, nil, nil, nil, nil, nil)
	entry = sourceEvidence(
		t.Context(),
		workflow,
		host.Retained{},
		application.SourceObservation{Observation: row},
		"price",
	)

	html.Reset()

	if err := RetainedSourceEntry(entry).Render(t.Context(), &html); err != nil {
		t.Fatal(err)
	}

	for _, pair := range []struct{ field, reported, accepted string }{
		{"quantity", "10000 nominal", "9000.000 nominal"},
		{"gross_amount", "9876.54 USD", "8888.89 USD"},
		{"realized_pnl", "12.3456700 USD · 2026-10-21", "12.3456789 USD · 2026-10-22"},
		{"reported_cost_basis", "9876.54 USD", ""},
	} {
		var found bool
		for _, fact := range entry.Facts {
			if string(fact.Key) == pair.field {
				found = fact.Value == pair.reported && fact.Accepted == pair.accepted
				break
			}
		}

		if !found {
			t.Error("report term comparison lost exact value or basis", pair, entry.Facts)
		}
	}

	for _, value := range []string{"9000.000 nominal", "8888.89 USD", "12.3456789 USD · 2026-10-22"} {
		if !strings.Contains(html.String(), ">Accepted</span> "+value) {
			t.Error("accepted term missing from selected Source Reader entry", value, html.String())
		}
	}
}

func TestInstrumentIdentityLeadsWithTickerAcrossAdminViews(t *testing.T) {
	for _, tc := range []struct{ ticker, name, title, description string }{
		{"SYN", "Synthetic equity", "SYN", "Synthetic equity"},
		{"", "Synthetic equity", "Synthetic equity", ""},
		{"SYN", "", "SYN", ""},
		{"SYN", "SYN", "SYN", ""},
		{"", "", "Unknown", ""},
	} {
		t.Run(tc.ticker+"/"+tc.name, func(t *testing.T) {
			instruments := instrumentGrid(application.InstrumentList{Items: []application.InstrumentListItem{{
				Resource: application.CanonicalResource{ID: "equity", Instrument: &application.PublishedInstrument{
					Name: new(tc.name), Ticker: new(tc.ticker), Family: "equity",
				}},
			}}})
			if instruments.Data[0].Ticker.Text != tc.title ||
				instruments.Data[0].Ticker.Href != "/instruments/equity" ||
				instruments.Data[0].Name.Text != known(tc.name) ||
				instruments.Data[0].Name.Href != "" {
				t.Fatal(instruments.Data)
			}

			activity := &application.PublishedActivity{
				PublishedActivityHeader: application.PublishedActivityHeader{
					Family: "trade",
				},
				Trade: &application.PublishedTrade{
					Side:              new("buy"),
					PublishedSecurity: application.PublishedSecurity{InstrumentID: new("equity")},
				},
			}

			activities := activityGrid(application.ActivityList{Items: []application.ActivityListItem{{
				Resource:       application.CanonicalResource{ID: "trade", Activity: activity},
				InstrumentName: tc.name, InstrumentTicker: tc.ticker,
			}}})
			if activities.Data[0].Label.Text != "Buy "+tc.title ||
				activities.Data[0].Label.Note != describe(tc.description, activityKindLabel("trade")) {
				t.Fatal(activities.Data)
			}

			reviews := reviewItems(application.ReviewList{Items: []application.ReviewEntry{
				{Kind: application.ReviewInstrument, ID: "equity", Name: tc.name, Ticker: tc.ticker},
				{Kind: application.ReviewActivity, ID: "trade", Name: tc.name, Ticker: tc.ticker, Activity: activity},
			}})
			if reviews[0].Label != tc.title || reviews[1].Label != "Buy "+tc.title ||
				reviews[0].Description != tc.description || reviews[1].Description != tc.description {
				t.Fatal(reviews)
			}
		})
	}
}
