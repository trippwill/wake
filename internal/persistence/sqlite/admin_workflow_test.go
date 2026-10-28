// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/host"
	"go.trippwill.dev/wake/internal/reference"
	"go.trippwill.dev/wake/internal/transport/admin"
	"go.trippwill.dev/wake/providers/ibkrflex"
)

func TestAuthenticatedAccountWorkflowHTTP(t *testing.T) {
	ctx := t.Context()
	store, _ := testStore(t)
	envelope := testEnvelope(t)
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)

	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/mapping":
			_, _ = fmt.Fprint(w, `[{"warning":"No identifier found."}]`)
		case strings.HasSuffix(r.URL.Path, "SendRequest"):
			_, _ = fmt.Fprint(
				w,
				`<FlexStatementResponse><Status>Success</Status><ReferenceCode>synthetic</ReferenceCode></FlexStatementResponse>`,
			)
		default:
			_, _ = fmt.Fprint(w, workflowReport(equityRow("U1", "http-fill", "401.000000001")))
		}
	}))
	defer upstream.Close()

	registration, err := ibkrflex.Registration(
		ibkrflex.Options{HTTPClient: upstream.Client(), BaseURL: upstream.URL},
	)
	if err != nil {
		t.Fatal(err)
	}

	h, err := host.New(ctx, store, envelope, nil, func() time.Time { return now }, registration)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()

	refs, err := reference.New(
		store,
		envelope,
		upstream.Client(),
		upstream.URL+"/mapping",
		func() time.Time { return now },
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}

	workflow := application.New(store, h, refs, "ibkr-flex", ibkrflex.Confirmations, ibkrflex.Version, nil, nil)

	dir := t.TempDir()
	if err = admin.SetUnlock(dir, []byte("synthetic-http-unlock")); err != nil {
		t.Fatal(err)
	}

	const origin = "http://127.0.0.1:8787"

	auth, enabled, err := admin.Open(dir, "local", origin)
	if err != nil || !enabled {
		t.Fatal(enabled, err)
	}
	defer auth.Close()

	handler, err := admin.NewHandler(auth, admin.Dependencies{Workflow: workflow})
	if err != nil {
		t.Fatal(err)
	}

	var cookie *http.Cookie

	csrf := ""
	request := func(method, path string, values url.Values, want int) *httptest.ResponseRecorder {
		t.Helper()

		if values == nil {
			values = url.Values{}
		}

		if csrf != "" {
			values.Set("csrf", csrf)
		}

		r := httptest.NewRequest(method, origin+path, strings.NewReader(values.Encode())).WithContext(ctx)
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Origin", origin)

		if cookie != nil {
			r.AddCookie(cookie)
		}

		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)

		if w.Code != want {
			t.Fatalf("%s %s: %d: %s", method, path, w.Code, w.Body.String())
		}

		if strings.Contains(w.Body.String(), "synthetic-token") {
			t.Fatal("credential exposed in response")
		}

		return w
	}
	login := request(
		http.MethodPost,
		"/login",
		url.Values{"unlock": {"synthetic-http-unlock"}},
		http.StatusSeeOther,
	)
	cookie = login.Result().Cookies()[0]
	page := request(http.MethodGet, "/connections/new", nil, http.StatusOK).Body.String()

	_, rest, found := strings.Cut(page, `name="csrf" value="`)
	if !found {
		t.Fatal("missing CSRF form")
	}

	csrf, _, found = strings.Cut(rest, `"`)
	if !found || csrf == "" {
		t.Fatal("invalid CSRF form")
	}

	request(
		http.MethodPost,
		"/connections",
		url.Values{"name": {"Synthetic"}, "query_id": {"invalid"}, "token": {"synthetic-token"}},
		http.StatusBadRequest,
	)
	request(
		http.MethodPost,
		"/connections",
		url.Values{"name": {"Synthetic"}, "query_id": {"123"}, "token": {"synthetic-token"}},
		http.StatusSeeOther,
	)

	view, err := workflow.Dashboard(ctx)
	if err != nil || len(view.Connections) != 1 {
		t.Fatal(view, err)
	}

	id := view.Connections[0].ID
	request(
		http.MethodPost,
		"/connections/"+id+"/settings",
		url.Values{"name": {"Updated synthetic"}, "query_id": {"123"}},
		http.StatusSeeOther,
	)
	request(
		http.MethodPost,
		"/connections/"+id+"/token",
		url.Values{"token": {"synthetic-token"}},
		http.StatusSeeOther,
	)
	request(http.MethodPost, "/connections/"+id+"/retry", nil, http.StatusSeeOther)
	request(http.MethodPost, "/reference/key", url.Values{"key": {"synthetic-reference"}}, http.StatusSeeOther)

	run := func() {
		t.Helper()

		if err = workflow.RunOnce(ctx, now); err != nil {
			t.Fatal(err)
		}

		now = now.Add(10 * time.Second)
	}
	run()
	run()

	view, err = workflow.Dashboard(ctx)
	if err != nil || len(view.Connections[0].Candidates) != 2 {
		t.Fatal(view, err)
	}

	candidate := view.Connections[0].Candidates[0]
	ref := candidate.Candidate.Reference()
	path := "/connections/" + id + "/confirm"
	request(
		http.MethodGet,
		path+"?"+url.Values{"artifact": {candidate.Artifact}, "account": {ref.Value()}}.Encode(),
		nil,
		http.StatusOK,
	)
	values := url.Values{
		"artifact":  {candidate.Artifact},
		"authority": {ref.Authority()},
		"kind":      {ref.Kind()},
		"scope":     {ref.Scope()},
		"account":   {ref.Value()},
		"name":      {"Synthetic account"},
		"currency":  {"USD"},
	}
	request(http.MethodPost, path, values, http.StatusSeeOther)

	if view, err = workflow.Dashboard(ctx); err != nil || view.Account.ID() == "" || len(view.Executions) != 0 {
		t.Fatal("confirmation did not retain authority boundary", view, err)
	}

	run()
	run()

	view, err = workflow.Dashboard(ctx)
	if err != nil || len(view.Executions) != 1 || len(view.Instruments) != 1 {
		t.Fatal(view, err)
	}

	for _, path = range []string{"/connections", "/connections/" + id, "/activity", "/executions/" + view.Executions[0].ID(), "/instruments/" + view.Instruments[0].ID(), "/evidence/" + candidate.Artifact} {
		request(http.MethodGet, path, nil, http.StatusOK)
	}

	for _, path = range []string{"/connections/missing/confirm", "/executions/missing", "/instruments/missing", "/evidence/missing", "/reference-evidence/missing"} {
		request(http.MethodGet, path, nil, http.StatusNotFound)
	}

	request(http.MethodPost, "/connections/"+id+"/retry", nil, http.StatusSeeOther)

	if err = store.Close(); err != nil {
		t.Fatal(err)
	}

	for _, path = range []string{"/connections", "/connections/" + id, "/activity", "/connections/" + id + "/confirm"} {
		request(http.MethodGet, path, nil, http.StatusServiceUnavailable)
	}
}
