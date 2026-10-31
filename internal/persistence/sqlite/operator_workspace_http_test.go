// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"crypto/rand"
	"encoding/json"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/transport/admin"
)

func TestOperatorWorkspacesInspectAndValidateRetainedHistory(t *testing.T) {
	s := coverageStore(t)
	lifecycle := strings.ReplaceAll(
		strings.ReplaceAll(
			strings.ReplaceAll(syntheticOption, "option-fill", "outcome-fill"),
			"option-trade",
			"outcome-trade",
		),
		`transactionType="ExchTrade"`,
		`transactionType="BookTrade" code="A"`,
	)
	coverageReport(t, s, "operator-history", syntheticOption+syntheticDebt+syntheticFX+lifecycle)
	coverageReport(
		t,
		s,
		"operator-conflict",
		strings.ReplaceAll(syntheticOption, `quantity="2"`, `quantity="3"`),
	)

	view, err := s.Dashboard(t.Context())
	if err != nil || len(view.Executions) != 2 || len(view.Lifecycles) != 1 || len(view.Conversions) != 1 {
		t.Fatal(view, err)
	}

	dir := t.TempDir()
	if err = admin.SetUnlock(dir, []byte("synthetic-workspace-review")); err != nil {
		t.Fatal(err)
	}

	const origin = "http://127.0.0.1:8787"

	auth, _, err := admin.Open(dir, "local", origin)
	if err != nil {
		t.Fatal(err)
	}
	defer auth.Close()

	workflow := application.New(s, nil, nil, "", "", "", nil, nil)

	handler, err := admin.NewHandler(auth, admin.Dependencies{Workflow: workflow})
	if err != nil {
		t.Fatal(err)
	}

	var cookie *http.Cookie

	csrf := ""
	request := func(method, path string, form url.Values, fragment bool) *httptest.ResponseRecorder {
		t.Helper()

		if form == nil {
			form = url.Values{}
		}

		if csrf != "" {
			form.Set("csrf", csrf)
		}

		r := httptest.NewRequest(method, origin+path, strings.NewReader(form.Encode())).WithContext(t.Context())
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Origin", origin)

		if cookie != nil {
			r.AddCookie(cookie)
		}

		if fragment {
			r.Header.Set("HX-Request", "true")
		}

		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)

		return w
	}

	login := request(http.MethodPost, "/login", url.Values{"unlock": {"synthetic-workspace-review"}}, false)
	if login.Code != http.StatusSeeOther {
		t.Fatal(login.Code, login.Body.String())
	}

	cookie = login.Result().Cookies()[0]

	optionID := ""
	for _, e := range view.Executions {
		if e.Terms().Fields().Instrument.Kind() == "option" {
			optionID = e.ID()
		}
	}

	if optionID == "" {
		t.Fatal("missing option")
	}

	correctionPath := "/activity/" + optionID + "/correct"
	page := request(http.MethodGet, correctionPath, nil, false).Body.String()

	_, tail, ok := strings.Cut(page, `name="csrf" value="`)
	if !ok {
		t.Fatal("missing CSRF")
	}

	csrf, _, ok = strings.Cut(tail, `"`)
	if !ok || csrf == "" {
		t.Fatal("invalid CSRF")
	}

	for _, test := range []struct{ path, want string }{
		{"/", "Synthetic account"},
		{"/activity", "Convert EUR"},
		{"/activity?kind=trade&state=review", "Synthetic option"},
		{"/activity?kind=conversion", "EUR"},
		{"/activity?view=review", "Reported values need review"},
		{"/activity?view=instruments&state=gaps", "Synthetic option"},
		{"/activity?q=unmatched-synthetic-query", "0 of"},
		{"/connections/synthetic?view=access", "Confirmation Query ID"},
		{"/connections/synthetic?view=reports", "operator-history"},
		{"/connections/recovery", "Access review complete"},
		{"/system/maintenance", "Rebuild accepted views"},
		{"/executions/" + optionID, "1.234567890123456789"},
		{"/activity/" + view.Lifecycles[0].ID(), "Assignment"},
		{"/activity/" + view.Conversions[0].ID(), "100.123456789"},
		{"/evidence/operator-history", "TradeConfirm"},
		{correctionPath + "?mode=evidence", "Compare a source candidate"},
		{correctionPath + "?field=quantity", "Enter a decimal greater than zero."},
	} {
		for _, fragment := range []bool{false, true} {
			response := request(http.MethodGet, test.path, nil, fragment)
			if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), test.want) {
				t.Fatalf(
					"%s fragment=%v: %d: wanted %q in %s",
					test.path,
					fragment,
					response.Code,
					test.want,
					response.Body.String()[:min(250, len(response.Body.String()))],
				)
			}

			if strings.Contains(strings.ToLower(response.Body.String()), "<!doctype html>") == fragment {
				t.Fatal("incorrect full-page/fragment response", test.path)
			}
		}
	}
	// The same retained records remain available through every sortable grid view.
	for _, sort := range []string{"label", "quantity", "price", "state", "reference", "date"} {
		response := request(http.MethodGet, "/activity?sort="+sort+"&dir=asc", nil, true)
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), optionID) {
			t.Fatal("activity ordering lost record", sort, response.Code)
		}
	}

	for _, sort := range []string{"kind", "ticker", "state", "name"} {
		response := request(http.MethodGet, "/activity?view=instruments&sort="+sort+"&dir=desc", nil, true)
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Synthetic option") {
			t.Fatal("instrument ordering lost record", sort, response.Code)
		}
	}

	form, err := s.CorrectionForm(t.Context(), "activity", optionID)
	if err != nil || len(form.Candidates) < 2 {
		t.Fatal("missing competing retained observations", form, err)
	}

	for _, candidate := range form.Candidates {
		response := request(
			http.MethodGet,
			correctionPath+"?mode=evidence&candidate="+url.QueryEscape(candidate.Value),
			nil,
			true,
		)
		if response.Code != http.StatusOK ||
			!strings.Contains(response.Body.String(), "Save candidate selection") ||
			!strings.Contains(response.Body.String(), html.EscapeString(candidate.Value)) {
			t.Fatal(
				"candidate comparison unavailable",
				response.Code,
				strings.Contains(response.Body.String(), "Save candidate selection"),
				candidate.Value,
			)
		}
	}

	for _, instrument := range view.Instruments {
		response := request(
			http.MethodGet,
			"/instruments/"+instrument.ID()+"?origin=%2Factivity%3Fview%3Dinstruments",
			nil,
			true,
		)
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), instrument.ID()) {
			t.Fatal("instrument provenance unavailable", response.Code)
		}
	}
	// Validation is authenticated, typed, revision-bound and read-only across kinds.
	targets := []struct{ kind, id, field, value string }{
		{"activity", optionID, "quantity", "2.500000000000001"},
		{"activity", view.Lifecycles[0].ID(), "quantity", "3"},
		{"activity", view.Conversions[0].ID(), "sent_amount", "12.123456789"},
		{"instrument", view.Executions[0].Instrument(), "name", "Reviewed synthetic name"},
	}
	for _, target := range targets {
		form, e := s.CorrectionForm(t.Context(), target.kind, target.id)
		if e != nil {
			t.Fatal(e)
		}

		path := "/activity/" + target.id + "/correct/check"
		if target.kind == "instrument" {
			path = "/instruments/" + target.id + "/correct/check"
		}

		values := url.Values{
			"scope":      {target.field},
			"value":      {target.value},
			"action":     {"set"},
			"revision":   {strconv.FormatInt(form.Revision, 10)},
			"submission": {rand.Text()},
		}
		response := request(http.MethodPost, path, values, false)

		var result struct{ Valid, Stale bool }
		if e = json.Unmarshal(
			response.Body.Bytes(),
			&result,
		); e != nil || !result.Valid || result.Stale ||
			response.Code != http.StatusOK {
			t.Fatal("valid preflight", target, response.Code, response.Body.String(), e)
		}

		values.Set("revision", strconv.FormatInt(form.Revision+1, 10))

		response = request(http.MethodPost, path, values, false)
		if e = json.Unmarshal(response.Body.Bytes(), &result); e != nil || result.Valid || !result.Stale {
			t.Fatal("stale preflight", response.Body.String(), e)
		}

		after, e := s.CorrectionForm(t.Context(), target.kind, target.id)
		if e != nil || after.Revision != form.Revision || len(after.Decisions) != len(form.Decisions) {
			t.Fatal("validation changed retained state", after, e)
		}
	}

	retainDecision(t, s, "activity", optionID, "quantity", "4", "", "set")

	response := request(http.MethodGet, correctionPath+"?mode=decisions", nil, true)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Release") {
		t.Fatal("retained decision unavailable for release", response.Code, response.Body.String())
	}
}
