// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/host"
	"go.trippwill.dev/wake/internal/transport/admin"
	"go.trippwill.dev/wake/providers/ibkrflex"
)

func TestCombinedInstrumentHTTPRedirectPreservesNavigation(t *testing.T) {
	s := coverageStore(t)
	coverageReport(t, s, "first", equityRow("U1", "a", "100"))
	coverageReport(
		t,
		s,
		"second",
		strings.Replace(equityRow("U1", "b", "200"), `conid="100"`, `conid="200"`, 1),
	)

	view, err := s.Dashboard(t.Context())
	if err != nil || len(view.Instruments) != 2 {
		t.Fatal(view, err)
	}

	first, second := view.Instruments[0].ID(), view.Instruments[1].ID()
	applyEquityIdentity(t, s, first)
	applyEquityIdentity(t, s, second)

	dir := t.TempDir()
	if err = admin.SetUnlock(dir, []byte("synthetic-alias-review")); err != nil {
		t.Fatal(err)
	}

	const origin = "http://127.0.0.1:8787"

	auth, _, err := admin.Open(dir, "local", origin)
	if err != nil {
		t.Fatal(err)
	}
	defer auth.Close()

	registration, err := ibkrflex.Registration(ibkrflex.Options{})
	if err != nil {
		t.Fatal(err)
	}

	h, err := host.New(t.Context(), s, nil, nil, nil, registration)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()

	handler, err := admin.NewHandler(auth, admin.Dependencies{Workflow: application.New(s, h, nil, nil, nil)})
	if err != nil {
		t.Fatal(err)
	}

	login := httptest.NewRequest(
		http.MethodPost,
		origin+"/login",
		strings.NewReader(url.Values{"unlock": {"synthetic-alias-review"}}.Encode()),
	)
	login.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	login.Header.Set("Origin", origin)

	loggedIn := httptest.NewRecorder()
	handler.ServeHTTP(loggedIn, login)

	if loggedIn.Code != http.StatusSeeOther || len(loggedIn.Result().Cookies()) != 1 {
		t.Fatal("synthetic login", loggedIn.Code, loggedIn.Body.String())
	}

	const listOrigin = "/activity?q=synthetic&view=instruments"

	for _, suffix := range []string{"", "/correct"} {
		r := httptest.NewRequest(
			http.MethodGet,
			origin+"/instruments/"+second+suffix+"?origin="+url.QueryEscape(listOrigin),
			nil,
		)
		r.AddCookie(loggedIn.Result().Cookies()[0])

		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)

		target, e := url.Parse(w.Header().Get("Location"))
		if e != nil || w.Code != http.StatusSeeOther || target.Path != "/instruments/"+first+suffix ||
			target.Query().Get("origin") != listOrigin || target.Query().Get("parent") != "" {
			t.Fatal("combined instrument lost canonical URL or list navigation", w.Code, w.Header(), e)
		}

		canonical := httptest.NewRequest(http.MethodGet, origin+target.String(), nil)
		canonical.AddCookie(loggedIn.Result().Cookies()[0])

		resolved := httptest.NewRecorder()
		handler.ServeHTTP(resolved, canonical)

		if resolved.Code != http.StatusOK || !strings.Contains(resolved.Body.String(), "Verified issuer") {
			t.Fatal("canonical page unavailable", resolved.Code, resolved.Body.String())
		}
	}
}
