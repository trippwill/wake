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

	"go.trippwill.dev/wake/internal/application"
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
				Outcome:    application.ObservationAccepted,
			},
			{
				Artifact:  "synthetic-report",
				Version:   "rule-v1",
				Location:  "row[3]",
				Rule:      "synthetic-rule",
				Retrieved: "2026-10-31T10:00:00.120Z",
				Outcome:   application.ObservationEnriched,
			},
		},
	}, s.failure
}

func TestAuthenticatedGridResponsesAndFailures(t *testing.T) {
	dir := t.TempDir()
	if err := SetUnlock(dir, []byte("synthetic-grid-unlock")); err != nil {
		t.Fatal(err)
	}

	auth, _, err := Open(dir, "local", "http://127.0.0.1:8787")
	if err != nil {
		t.Fatal(err)
	}
	defer auth.Close()

	session := httptest.NewRecorder()
	auth.issue(session)

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
				Family:       "trade",
				Side:         new("buy"),
				InstrumentID: new("equity"),
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
