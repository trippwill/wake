// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"go.trippwill.dev/wake/financial"
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

func TestExactDisplayOrderingRetainsCurrencyAndUnits(t *testing.T) {
	for _, test := range []struct {
		a, b string
		want int
	}{
		{"9007199254740992.9999999999999 USD", "9007199254740993.0000000000001 USD", -1},
		{"-1.000000000000001 shares", "-1 shares", -1},
		{"0.00000 EUR", "-0 EUR", 0},
		{"100 EUR", "1 USD", -1},
		{"1 USD per share", "1 USD per nominal unit", 1},
	} {
		got := decimalOrder(test.a, test.b)
		if got < 0 {
			got = -1
		} else if got > 0 {
			got = 1
		}

		if got != test.want {
			t.Errorf("%s / %s: %d", test.a, test.b, got)
		}
	}
}

func TestReviewClassificationDoesNotTreatHistoryOrGapsAsTasks(t *testing.T) {
	v := application.Dashboard{Corrections: []application.CorrectionReview{
		{Artifact: "applied", Status: "applied", Kind: "cancellation"},
		{
			Artifact: "unresolved",
			Status:   "unresolved",
			Kind:     "replacement",
			Location: "row-one",
		},
	}}

	items := reviews(v)
	if len(items) != 1 || !strings.HasPrefix(items[0].Href, "/evidence/unresolved") {
		t.Fatal(items)
	}

	for _, state := range []string{"provisional", "no-match", "accepted", "cancelled", "superseded", ""} {
		if application.ReferenceStatus(state).NeedsReview() {
			t.Error("non-review state counted", state)
		}
	}

	for _, state := range []string{"conflict", "ambiguous"} {
		if !application.ReferenceStatus(state).NeedsReview() {
			t.Error("review state omitted", state)
		}
	}
}

type workspaceStore struct {
	application.Store
	view application.Dashboard
}

func (s workspaceStore) Dashboard(context.Context) (application.Dashboard, error) { return s.view, nil }

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

	view := application.Dashboard{
		Connections: []application.ConnectionView{
			{ID: "one", Name: "Synthetic source", SettingsStored: true, CredentialStored: true},
		},
	}
	workflow := application.New(workspaceStore{view: view}, nil, nil, "", "", "", nil, nil)

	h, err := NewHandler(auth, Dependencies{Workflow: workflow})
	if err != nil {
		t.Fatal(err)
	}

	for _, route := range []string{"/", "/activity", "/activity?view=review", "/activity?view=instruments", "/connections", "/connections/new", "/connections/one?view=status", "/connections/one?view=reports", "/connections/one?view=access", "/connections/reference", "/connections/recovery", "/status", "/configuration", "/system/maintenance"} {
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
	account, _ := source.NewReference("synthetic", "account", "", "one")

	instrument, err := source.NewInstrument(source.InstrumentInput{Kind: source.Equity, Name: "Synthetic"})
	if err != nil {
		t.Fatal(err)
	}

	quantity, _ := financial.ParseQuantity("10")
	currency, _ := financial.ParseCurrency("USD")
	price, _ := financial.ParsePrice("1.00", currency)
	date, _ := financial.ParseDate("2026-10-20")

	trade, err := source.NewTrade(
		source.TradeInput{
			Location:   "row",
			Account:    account,
			Instrument: instrument,
			Side:       source.Buy,
			Quantity:   quantity,
			Price:      price,
			TradeDate:  date,
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	state, _ := application.NewActivityState(
		application.Accepted,
		application.ConflictingClaims,
		application.OperatorSelection,
	)

	execution, err := application.NewExecution("one", "account", "instrument", "", "rule", state, trade, 1)
	if err != nil {
		t.Fatal(err)
	}

	view := application.Dashboard{Executions: []application.Execution{execution}}
	for _, query := range []string{"q=needs+review", "q=operator+decision", "state=review", "state=accepted"} {
		request := pageRequest(httptest.NewRequest(http.MethodGet, "/activity?"+query, nil))
		if got := filteredActivity(request.Context(), view); len(got) != 1 {
			t.Errorf("%s omitted rendered activity: %v", query, got)
		}
	}

	request := pageRequest(httptest.NewRequest(http.MethodGet, "/activity?state=cancelled", nil))
	if got := filteredActivity(request.Context(), view); len(got) != 0 {
		t.Fatal("structured filter ignored lifecycle", got)
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
	)
	if err != nil {
		t.Fatal(err)
	}

	view := application.Dashboard{Instruments: []application.Instrument{instrument}}
	if got := instrumentLabel(view, "one"); !strings.Contains(got, "operator decision") {
		t.Fatal(got)
	}

	if got := instrumentState(view, "one"); got != "resolved; operator decision" {
		t.Fatal(got)
	}

	request := pageRequest(
		httptest.NewRequest(http.MethodGet, "/activity?view=instruments&q=operator+decision&state=resolved", nil),
	)
	if got := filteredInstruments(request.Context(), view); len(got) != 1 {
		t.Fatal("operator label search or structured status filter omitted instrument", got)
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
