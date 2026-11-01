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

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/source"
)

func TestNavigationContextStaysLocalAndBounded(t *testing.T) {
	for _, raw := range []string{"https://outside.invalid/", "//outside.invalid/activity", "/activity/../login", "/activity/%5coutside", "/assets/app.js", "/activity\r\nLocation: x"} {
		if got := localDestination(raw); got != "" {
			t.Errorf("accepted unsafe destination %q: %q", raw, got)
		}
	}

	root := "/activity?view=instruments&q=exact+name&page=2&selected=one"
	r := pageRequest(httptest.NewRequest(http.MethodGet, "/activity/one?origin="+url.QueryEscape(root), nil))

	dest, err := url.Parse(navURL(r.Context(), "/instruments/two"))
	if err != nil {
		t.Fatal(err)
	}

	if dest.Query().Get("origin") != localDestination(root) || dest.Query().Get("parent") != "/activity/one" {
		t.Fatal(dest)
	}

	next := pageRequest(httptest.NewRequest(http.MethodGet, dest.String(), nil))

	back, err := url.Parse(returnURL(next.Context()))
	if err != nil {
		t.Fatal(err)
	}

	if back.Path != "/activity/one" || back.Query().Get("origin") != localDestination(root) {
		t.Fatal(back)
	}

	if got := localDestination(
		"/activity?origin=" + url.QueryEscape(root) + "&parent=/activity/two&q=needle",
	); got != "/activity?q=needle" {
		t.Fatal("recursive navigation retained", got)
	}
}

func TestGridNavigationUsesOwningPage(t *testing.T) {
	root := "/activity?q=exact&sort=price&dir=desc&page=2&selected=one"
	owner := "/activity/one?observations_q=duplicate&observations_sort=location&observations_dir=asc&observations_page=2"
	request := httptest.NewRequest(
		http.MethodGet,
		"/grids/observations?page_url="+url.QueryEscape(owner)+"&origin="+url.QueryEscape(root),
		nil,
	)

	r, err := gridPageRequest(request, "observations")
	if err != nil {
		t.Fatal(err)
	}

	target, err := url.Parse(navURL(r.Context(), "/evidence/report"))
	if err != nil {
		t.Fatal(err)
	}

	if target.Query().Get("parent") != localDestination(owner) ||
		target.Query().Get("origin") != localDestination(root) {
		t.Fatal(target)
	}

	next := pageRequest(httptest.NewRequest(http.MethodGet, target.String(), nil))

	back, err := url.Parse(returnURL(next.Context()))
	if err != nil {
		t.Fatal(err)
	}

	if back.Path != "/activity/one" || back.Query().Get("observations_q") != "duplicate" ||
		back.Query().Get("observations_page") != "2" ||
		back.Query().Get("origin") != localDestination(root) {
		t.Fatal(back)
	}

	for _, owner := range []string{"https://outside.invalid/activity", "//outside.invalid/activity", "/assets/app.js"} {
		_, err := gridPageRequest(
			httptest.NewRequest(http.MethodGet, "/grids/activity?page_url="+url.QueryEscape(owner), nil),
			"activity",
		)
		if err == nil {
			t.Fatal("accepted unsafe grid owner", owner)
		}
	}
}

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
	dir := t.TempDir()
	if err := SetUnlock(dir, []byte("synthetic-workspace-unlock")); err != nil {
		t.Fatal(err)
	}

	auth, _, err := Open(dir, "local", "http://127.0.0.1:8787")
	if err != nil {
		t.Fatal(err)
	}
	defer auth.Close()

	session := httptest.NewRecorder()
	auth.issue(session)

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

	for _, route := range []string{"/", "/activity", "/activity?view=review", "/activity?view=instruments", "/connections", "/connections/new", "/connections/one?view=status", "/connections/one?view=reports", "/connections/one?view=access", "/connections/reference", "/connections/recovery", "/status", "/configuration"} {
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
		httptest.NewRequest(http.MethodGet, "/activity?view=instruments&q=operator+decision&state=resolved", nil),
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
		{application.PublishedActivity{Family: "trade", Side: &side}, "Buy Zebra"},
		{application.PublishedActivity{Family: "lifecycle", LifecycleKind: &lifecycle}, "Assignment · Zebra"},
		{application.PublishedActivity{Family: "conversion", Sent: &application.PublishedAmount{Currency: "EUR"}, Received: &application.PublishedAmount{Currency: "USD"}}, "Convert EUR → USD"},
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
		request := pageRequest(httptest.NewRequest(http.MethodGet, "/activity?view=review&q=unmatched", nil))

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
			httptest.NewRequest(http.MethodGet, "/activity?view=review&q="+url.QueryEscape(test.search), nil),
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

func TestObservationGridFallbackPreservesNamespacedState(t *testing.T) {
	root := "/activity?q=original&page=3"
	request := httptest.NewRequest(
		http.MethodGet,
		"/grids/observations?subject=activity&id=one&q=duplicate&sort=location&dir=asc&page=2&origin="+url.QueryEscape(
			root,
		),
		nil,
	)

	owned, err := gridPageRequest(request, "observations")
	if err != nil {
		t.Fatal(err)
	}

	target, err := url.Parse(navURL(owned.Context(), "/evidence/report"))
	if err != nil {
		t.Fatal(err)
	}

	next := pageRequest(httptest.NewRequest(http.MethodGet, target.String(), nil))

	returned, err := url.Parse(returnURL(next.Context()))
	if err != nil || returned.Path != "/activity/one" ||
		returned.Query().Get("observations_q") != "duplicate" ||
		returned.Query().Get("observations_sort") != "location" ||
		returned.Query().Get("observations_dir") != "asc" ||
		returned.Query().Get("observations_page") != "2" ||
		returned.Query().Get("origin") != localDestination(root) ||
		returned.Query().Get("q") != "" {
		t.Fatal(returned, err)
	}

	query, err := listQuery(owned.Context(), application.ObservationScope)
	if err != nil {
		t.Fatal(err)
	}

	for outcome, label := range query.Options().ObservationLabels {
		if label != observationStatus(outcome) {
			t.Fatal(outcome, label)
		}
	}

	if len(query.Options().ObservationLabels) != 7 {
		t.Fatal("incomplete outcome vocabulary", query.Options().ObservationLabels)
	}
}
