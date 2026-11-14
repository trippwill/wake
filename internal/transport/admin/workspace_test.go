// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/source"
)

func TestReviewClassificationDoesNotTreatHistoryOrGapsAsTasks(t *testing.T) {
	for _, state := range []application.ReferenceStatus{"provisional", "no-match", "resolved", ""} {
		if state.NeedsReview() {
			t.Fatal(state)
		}
	}

	for _, state := range []application.ReferenceStatus{"conflict", "ambiguous"} {
		if !state.NeedsReview() {
			t.Fatal(state)
		}
	}

	items := reviewItems(
		application.ReviewList{
			Items: []application.ReviewEntry{
				{
					Kind:     application.ReviewInstruction,
					Artifact: "unresolved",
					Location: "row-one",
					Name:     "replacement",
					Status:   application.CorrectionUnresolved,
				},
			},
		},
	)
	if len(items) != 1 || !strings.HasPrefix(items[0].Href, "/evidence/unresolved") {
		t.Fatal(items)
	}
}

type workspaceStore struct {
	application.Store
	view fixtureView
}

func TestWorkspaceRoutesAndFragments(t *testing.T) {
	dir := privateAuthDir(t)
	if err := SetUnlock(dir, []byte("synthetic-workspace-unlock")); err != nil {
		t.Fatal(err)
	}

	auth, _, err := Open(dir, "http://127.0.0.1:8787")
	if err != nil {
		t.Fatal(err)
	}

	session := httptest.NewRecorder()
	issueTestSession(t, auth, session)

	view := fixtureView{
		Connections: []application.ConnectionView{
			{ID: "one", Name: "Synthetic source", SettingsStored: true, CredentialStored: true},
		},
	}
	workflow := application.New(workspaceStore{view: view}, nil, nil, nil, nil)

	h, err := NewHandler(auth, Dependencies{Workflow: workflow})
	if err != nil {
		t.Fatal(err)
	}

	for _, route := range []string{"/", "/activity", "/activity?size=invalid", "/activity?size=201", "/review", "/instruments", "/instruments?size=invalid", "/instruments?size=201", "/connections", "/connections/new", "/connections/one?view=status", "/connections/one?view=reports", "/connections/one?view=access", "/connections/reference", "/connections/recovery", "/system", "/system/configuration"} {
		for _, fragment := range []bool{false, true} {
			r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8787"+route, nil)
			r.AddCookie(session.Result().Cookies()[0])

			if fragment {
				r.Header.Set("HX-Request", "true")
			}

			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)

			if w.Code != http.StatusOK {
				t.Fatalf("%s fragment=%v: %d %s", route, fragment, w.Code, w.Body.String())
			}

			body := w.Body.String()
			if strings.Contains(strings.ToLower(body), "<!doctype html>") == fragment ||
				!strings.Contains(body, `id="workspace-content"`) {
				t.Fatalf("incorrect shell/fragment %s %v", route, fragment)
			}

			if !fragment && !strings.Contains(body, ">System</span>") {
				t.Fatal("system navigation missing", route)
			}
		}
	}

	r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8787/activity", nil)
	r.Header.Set("HX-Request", "true")

	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Fatal("fragment expiry redirected into login", w.Code)
	}
}

func TestActivitySearchMatchesRenderedFinancialState(t *testing.T) {
	state, _ := application.NewActivityState(
		application.Accepted,
		application.ConflictingClaims,
		application.OperatorSelection,
	)
	for _, search := range []string{"needs review", "operator decision"} {
		request := pageRequest(httptest.NewRequest(http.MethodGet, "/activity?q="+url.QueryEscape(search), nil))

		query, err := listQuery(request.Context(), application.ActivityScope)
		if err != nil || !slices.Contains(query.Options().StateMatches, state) {
			t.Fatal("rendered label did not produce typed query", query, err)
		}
	}
}

func TestInstrumentPresentationPreservesOperatorAuthority(t *testing.T) {
	reported, err := source.NewInstrument(source.InstrumentInput{Kind: source.Equity, Name: "Synthetic"})
	if err != nil {
		t.Fatal(err)
	}

	instrument, err := application.NewInstrument(
		"one",
		"Synthetic",
		"SYN",
		"equity",
		"resolved",
		"",
		"",
		reported,
		"rule",
		1,
		application.OperatorSelection,
		application.ReferenceMetadata{},
	)
	if err != nil {
		t.Fatal(err)
	}

	if got := referenceLabel(
		instrument.Status(),
		instrument.Authority(),
	); got != "resolved; operator decision" {
		t.Fatal(got)
	}

	request := pageRequest(
		httptest.NewRequest(http.MethodGet, "/instruments?q=operator+decision&state=resolved", nil),
	)

	query, queryErr := listQuery(request.Context(), application.InstrumentScope)
	if queryErr != nil ||
		!slices.Contains(
			query.Options().ReferenceMatches,
			application.ReferenceMatch{Status: "resolved", Authority: application.OperatorSelection},
		) {
		t.Fatal(query, queryErr)
	}

	var rendered strings.Builder
	if err = InstrumentBadge(instrument).Render(context.Background(), &rendered); err != nil {
		t.Fatal(err)
	}

	if got := rendered.String(); !strings.Contains(got, "operator decision") ||
		!strings.Contains(got, `data-state="success"`) {
		t.Fatal(got)
	}
}

func TestReviewPresentationRetainsStructuredLabelsAndFilteredEmptyState(t *testing.T) {
	side, lifecycle := "buy", "assignment"
	for _, test := range []struct {
		activity application.PublishedActivity
		want     string
	}{
		{application.PublishedActivity{
			PublishedActivityHeader: application.PublishedActivityHeader{
				Family: "trade",
			},
			Trade: &application.PublishedTrade{
				Side: &side,
			},
		}, "Buy Zebra"},
		{application.PublishedActivity{
			PublishedActivityHeader: application.PublishedActivityHeader{
				Family: "lifecycle",
			},
			Lifecycle: &application.PublishedLifecycle{
				LifecycleKind: &lifecycle,
			},
		}, "Assignment · Zebra"},
		{application.PublishedActivity{
			PublishedActivityHeader: application.PublishedActivityHeader{
				Family: "conversion",
			},
			Conversion: &application.PublishedConversion{
				Sent:     &application.PublishedAmount{Currency: "EUR"},
				Received: &application.PublishedAmount{Currency: "USD"},
			},
		}, "Convert EUR → USD"},
	} {
		rows := reviewItems(
			application.ReviewList{
				Items: []application.ReviewEntry{
					{Kind: application.ReviewActivity, Name: "Zebra", Activity: &test.activity},
				},
			},
		)
		if len(rows) != 1 || rows[0].Label != test.want {
			t.Fatal(rows, test.want)
		}
	}

	for _, info := range []application.PageInfo{{Total: 2, Matched: 0, Page: 1, PageSize: 50}, {Total: 2, Matched: 2, Page: 9, PageSize: 50}} {
		request := pageRequest(httptest.NewRequest(http.MethodGet, "/review?q=unmatched", nil))

		var out strings.Builder
		if err := ReviewQueue(activityView{PageInfo: info}).Render(request.Context(), &out); err != nil {
			t.Fatal(err)
		}

		if strings.Contains(out.String(), "No activity or identity conflicts are awaiting review") ||
			!strings.Contains(out.String(), "No review items match") {
			t.Fatal(out.String())
		}
	}
}

func TestReviewItemHasOneDescriptiveLink(t *testing.T) {
	const href = "/executions/synthetic-fill"

	item := reviewItem{Href: href, Label: "Buy SYN", Kind: "Activity", Reason: "Reported prices disagree"}
	request := pageRequest(httptest.NewRequest(http.MethodGet, "/review", nil))

	var out strings.Builder
	if err := ReviewQueue(
		activityView{Reviews: []reviewItem{item}},
	).Render(request.Context(), &out); err != nil {
		t.Fatal(err)
	}

	markup := out.String()
	if strings.Count(markup, `href="`+href+`"`) != 1 ||
		!strings.Contains(markup, `href="`+href+`">Buy SYN</a>`) ||
		!strings.Contains(markup, ">Activity</p>") || !strings.Contains(markup, item.Reason) {
		t.Fatal(markup)
	}
}

func TestReviewSearchUsesRenderedCategoriesReasonsAndCounts(t *testing.T) {
	for _, test := range []struct {
		search     string
		kind       application.ReviewKind
		status     application.CorrectionStatus
		countKnown bool
		count      int64
	}{
		{"Identity", application.ReviewInstrument, "", false, 0},
		{"unresolved", application.ReviewInstruction, application.CorrectionUnresolved, false, 0},
		{"needs review", application.ReviewInstruction, application.CorrectionNeedsReview, false, 0},
		{"Source claims", application.ReviewIdentity, "", false, 0},
		{"3 source claims", application.ReviewIdentity, "", true, 3},
		{"Reported values", application.ReviewActivity, "", false, 0},
	} {
		request := pageRequest(
			httptest.NewRequest(http.MethodGet, "/review?q="+url.QueryEscape(test.search), nil),
		)

		query, err := listQuery(request.Context(), application.ReviewScope)
		if err != nil {
			t.Fatal(err)
		}

		expected := application.ReviewMatch{
			Kind:       test.kind,
			Status:     test.status,
			CountKnown: test.countKnown,
			Count:      test.count,
		}
		if !slices.Contains(query.Options().ReviewMatches, expected) {
			t.Fatal("rendered review search lost typed predicate", test.search, query.Options().ReviewMatches)
		}
	}
}

func TestObservationQueriesKeepTypedOutcomeLabels(t *testing.T) {
	request := pageRequest(httptest.NewRequest(
		http.MethodGet,
		"/grids/observations?subject=artifact&id=one&q=no+additional+contribution&sort=location&dir=asc&page=2",
		nil,
	))

	query, err := listQuery(request.Context(), application.ObservationScope)
	if err != nil {
		t.Fatal(err)
	}

	for outcome, label := range query.Options().ObservationLabels {
		if label != observationStatus(outcome) {
			t.Fatal(outcome, label)
		}
	}

	if !slices.Contains(query.Options().ObservationMatches, application.ObservationNoContribution) ||
		observationStatus(application.ObservationNoContribution) != "no additional contribution" {
		t.Fatal("report contribution label was not searchable", query.Options())
	}

	if len(query.Options().ObservationLabels) != 10 {
		t.Fatal("incomplete outcome vocabulary", query.Options().ObservationLabels)
	}
}

func (workspaceStore) HistoryUpdating() bool { return false }

type historyWorkspaceStore struct{ workspaceStore }

func (historyWorkspaceStore) HistoryUpdating() bool { return true }

func (historyWorkspaceStore) Overview(context.Context) (application.OverviewView, error) {
	return application.OverviewView{Status: application.SystemStatus{
		HistoryUpdate: application.HistoryUpdateStatus{
			Rule: string(application.RetainedClaimsRule), Phase: "activity", Completed: 50,
			RetryAt: time.Date(2026, 11, 12, 12, 1, 0, 0, time.UTC),
		},
	}}, nil
}

func (historyWorkspaceStore) ObservationList(
	_ context.Context,
	kind, id string,
	_ application.ListQuery,
) (application.ObservationList, error) {
	if kind != "artifact" || id != "example" {
		return application.ObservationList{}, application.ErrInvalid
	}

	return application.ObservationList{
		PageInfo: application.PageInfo{Page: 1, PageSize: 50, Total: 1, Matched: 1},
		Items: []application.Observation{
			{Artifact: id, Location: "synthetic report row", Outcome: application.ObservationAccepted},
		},
	}, nil
}

func TestHistoryUpdateOverviewAndMaintenanceRoutes(t *testing.T) {
	dir := privateAuthDir(t)
	if err := SetUnlock(dir, []byte("synthetic-history-unlock")); err != nil {
		t.Fatal(err)
	}

	auth, _, err := Open(dir, "http://127.0.0.1:8787")
	if err != nil {
		t.Fatal(err)
	}

	session := httptest.NewRecorder()
	issueTestSession(t, auth, session)

	handler, err := NewHandler(
		auth,
		Dependencies{Workflow: application.New(historyWorkspaceStore{}, nil, nil, nil, nil)},
	)
	if err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{
		"/", "/activity", "/activity/example", "/instruments/example", "/review",
		"/positions", "/grids/activity", "/grids/positions", "/activity/example/correct", "/connections",
		"/grids/observations?subject=artifact&id=example",
		"/grids/observations?subject=activity&id=example",
		"/grids/observations?subject=instrument&id=example",
	} {
		request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8787"+path, nil)
		request.AddCookie(session.Result().Cookies()[0])

		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)

		switch path {
		case "/":
			if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "50 items processed") ||
				!strings.Contains(response.Body.String(), "2026-11-12T12:01:00Z") {
				t.Fatal("overview did not show progress and retry", response.Code)
			}
		case "/connections":
			if response.Code != http.StatusOK {
				t.Fatal("source inspection withheld", response.Code)
			}
		case "/grids/observations?subject=artifact&id=example":
			if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "synthetic report row") {
				t.Fatal("retained observations withheld", response.Code, response.Body.String())
			}
		default:
			if response.Code != http.StatusServiceUnavailable ||
				!strings.Contains(strings.ToLower(response.Body.String()), "updating retained") {
				t.Fatal(path, response.Code)
			}
		}
	}
}
