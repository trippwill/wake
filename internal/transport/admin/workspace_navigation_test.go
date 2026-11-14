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

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/config"
)

func TestWorkspaceDestinationsHaveDistinctTitlesAndNavigation(t *testing.T) {
	dir := privateAuthDir(t)
	if err := SetUnlock(dir, []byte("synthetic-navigation")); err != nil {
		t.Fatal(err)
	}

	auth, _, err := Open(dir, "http://127.0.0.1:8787")
	if err != nil {
		t.Fatal(err)
	}

	h, err := NewHandler(auth, Dependencies{Workflow: application.New(workspaceStore{}, nil, nil, nil, nil)})
	if err != nil {
		t.Fatal(err)
	}

	session := httptest.NewRecorder()
	issueTestSession(t, auth, session)

	for _, tc := range []struct{ path, title string }{
		{"/activity", "Activity"},
		{"/instruments", "Instruments"},
		{"/positions", "Positions"},
		{"/review", "Needs review"},
		{"/system", "Daemon status"},
		{"/system/configuration", "Configuration"},
		{"/system/settings", "Settings"},
	} {
		r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8787"+tc.path, nil)
		r.AddCookie(session.Result().Cookies()[0])

		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)

		html := w.Body.String()
		if w.Code != 200 || !strings.Contains(html, "<h1>"+tc.title+"</h1>") ||
			!strings.Contains(html, `href="`+tc.path+`" aria-current="page"`) {
			t.Fatal("destination lost its title or active navigation", tc, w.Code, html)
		}

		if strings.Contains(html, "Activity views") || strings.Contains(html, "Financial evidence") ||
			strings.Contains(
				html,
				`name="view"`,
			) || strings.Contains(html, `/activity?view=`) || strings.Contains(html, "wake-toolbar") {
			t.Fatal("redundant chrome retained", tc)
		}

		if strings.Contains(html, "data-theme-preference") != (tc.path == "/system/settings") {
			t.Fatal("theme selector must appear only in Settings", tc)
		}

		previous := -1
		for _, label := range []string{"Overview", "Activity", "Instruments", "Positions", "Needs review", "Connections", "System"} {
			index := strings.Index(html, "<span>"+label+"</span>")
			if index <= previous {
				t.Fatal("sidebar order changed", label)
			}

			previous = index
		}

		if strings.Index(html, ">Administration</li>") < strings.Index(html, "<span>Needs review</span>") ||
			strings.Index(html, ">Administration</li>") > strings.Index(html, "<span>Connections</span>") {
			t.Fatal("Connections is outside Administration")
		}
	}
}

func TestSettingsRequiresSessionAndDoesNotQueryStatus(t *testing.T) {
	dir := privateAuthDir(t)
	if err := SetUnlock(dir, []byte("synthetic-settings")); err != nil {
		t.Fatal(err)
	}

	const origin = "http://127.0.0.1:8787"

	auth, _, err := Open(dir, origin)
	if err != nil {
		t.Fatal(err)
	}

	h, err := NewHandler(auth, Dependencies{Queries: unavailableSettingsStatus{}})
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		fragment      bool
		authenticated bool
		want          int
	}{
		{false, false, http.StatusSeeOther},
		{true, false, http.StatusUnauthorized},
		{false, true, http.StatusOK},
		{true, true, http.StatusOK},
	} {
		r := httptest.NewRequest(http.MethodGet, origin+"/system/settings", nil)
		if tc.fragment {
			r.Header.Set("HX-Request", "true")
		}

		if tc.authenticated {
			session := httptest.NewRecorder()
			issueTestSession(t, auth, session)
			r.AddCookie(session.Result().Cookies()[0])
		}

		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)

		if w.Code != tc.want || (tc.want == http.StatusSeeOther && w.Header().Get("Location") != "/login") {
			t.Fatal(tc, w.Code, w.Header().Get("Location"))
		}

		html := w.Body.String()
		if tc.authenticated && (!strings.Contains(html, "data-theme-preference") ||
			strings.Contains(html, `hx-get="/system/settings"`) || strings.Contains(html, "wake-sidebar") == tc.fragment) {
			t.Fatal("unexpected Settings response", tc, html)
		}
	}
}

type unavailableSettingsStatus struct{}

func (unavailableSettingsStatus) Status(context.Context) (Status, error) {
	return Status{}, context.DeadlineExceeded
}

func TestConfigurationShowsLoadedFileOrAbsence(t *testing.T) {
	for _, path := range []string{"", "/synthetic/operator settings.toml"} {
		var html strings.Builder
		if err := OverviewBody("/system/configuration", Status{}, config.Config{
			Paths: config.Paths{ConfigFile: path},
		}).Render(context.Background(), &html); err != nil {
			t.Fatal(err)
		}

		want := path
		if path == "" {
			want = "None · No TOML file loaded at startup."
		}

		if !strings.Contains(html.String(), "<dt>Configuration file</dt>") ||
			!strings.Contains(html.String(), want) {
			t.Fatal("configuration file missing", html.String())
		}
	}
}

func TestDirectResourceNavigationUsesCanonicalFallbacks(t *testing.T) {
	for _, tc := range []struct{ path, section, back, label string }{
		{"/instruments/one", "Instruments", "/instruments", "Instruments"},
		{"/instruments/one/correct", "Instruments", "/instruments/one", "Instrument"},
		{"/evidence/report", "Activity", "/activity", "Activity"},
		{"/activity/one", "Activity", "/activity", "Activity"},
		{"/activity/one/correct", "Activity", "/activity/one", "Activity detail"},
		{"/review", "Needs review", "/review", "Needs review"},
		{"/instruments", "Instruments", "/instruments", "Instruments"},
		{"/positions", "Positions", "/positions", "Positions"},
		{"/system/configuration", "System", "/system", "System"},
	} {
		r := pageRequest(httptest.NewRequest(http.MethodGet, tc.path, nil))
		if workspaceSection(r.Context(), "Activity") != tc.section || workspaceLabel(r.Context()) != tc.section ||
			returnURL(r.Context()) != tc.back || parentLabel(r.Context()) != tc.label {
			t.Fatal("canonical navigation lost", tc, workspaceURL(r.Context()), returnURL(r.Context()))
		}
	}
}

func TestCorrectionURLsRetainOnlyTheirViewParameters(t *testing.T) {
	r := pageRequest(httptest.NewRequest(
		http.MethodGet,
		"/instruments/one/correct?mode=evidence&candidate=two&field=name&origin=https://outside.invalid&parent=/activity&page_url=/activity",
		nil,
	))

	for _, target := range []string{taskURL(r.Context(), "/instruments/one/correct"), modeURL(r.Context(), "decisions")} {
		u, err := url.Parse(target)
		if err != nil || u.Path != "/instruments/one/correct" {
			t.Fatal(target, err)
		}

		for _, key := range []string{"origin", "parent", "page_url"} {
			if u.Query().Has(key) {
				t.Fatal("navigation transport retained", target)
			}
		}
	}

	if taskURL(
		r.Context(),
		"/instruments/one/correct",
	) != "/instruments/one/correct?candidate=two&mode=evidence" ||
		modeURL(r.Context(), "decisions") != "/instruments/one/correct?field=name&mode=decisions" {
		t.Fatal("correction view parameters lost")
	}
}

func TestWarehouseSizeFormattingAndStatusPresentation(t *testing.T) {
	for _, tc := range []struct {
		bytes int64
		text  string
	}{
		{0, "0 B (0 bytes)"}, {1023, "1023 B (1023 bytes)"}, {1024, "1.0 KiB (1024 bytes)"}, {134637824, "128.4 MiB (134637824 bytes)"}, {-1, "Unavailable"},
	} {
		if got := warehouseSizeText(&tc.bytes); got != tc.text {
			t.Fatal(tc, got)
		}
	}

	if warehouseSizeText(nil) != "Unavailable" {
		t.Fatal("unknown size became zero")
	}

	var page strings.Builder

	bytes := int64(134637824)
	if err := OverviewBody(
		"/system",
		Status{WarehouseBytes: &bytes},
		config.Config{},
	).Render(context.Background(), &page); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(page.String(), "128.4 MiB (134637824 bytes)") ||
		strings.Contains(page.String(), "Operator workspaces") {
		t.Fatal(page.String())
	}
}
