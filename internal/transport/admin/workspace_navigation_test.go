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
	dir := t.TempDir()
	if err := SetUnlock(dir, []byte("synthetic-navigation")); err != nil {
		t.Fatal(err)
	}

	auth, _, err := Open(dir, "local", "http://127.0.0.1:8787")
	if err != nil {
		t.Fatal(err)
	}
	defer auth.Close()

	h, err := NewHandler(auth, Dependencies{Workflow: application.New(workspaceStore{}, nil, nil, nil, nil)})
	if err != nil {
		t.Fatal(err)
	}

	session := httptest.NewRecorder()
	auth.issue(session)

	for _, tc := range []struct{ path, title string }{
		{"/activity", "Activity"}, {"/activity?view=instruments", "Instruments"}, {"/activity?view=review", "Needs review"},
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

		if strings.Contains(html, "Activity views") || strings.Contains(html, "Financial evidence") {
			t.Fatal("redundant chrome retained", tc)
		}

		previous := -1
		for _, label := range []string{"Overview", "Activity", "Instruments", "Needs review", "Connections", "System"} {
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

func TestConfigurationShowsLoadedFileOrAbsence(t *testing.T) {
	for _, path := range []string{"", "/synthetic/operator settings.toml"} {
		var html strings.Builder
		if err := OverviewBody("/configuration", Status{}, config.Config{
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

func TestWorkspaceContextLabelsFollowOriginAndDirectInstrumentLinks(t *testing.T) {
	for _, tc := range []struct{ path, origin, section string }{
		{"/instruments/one", "", "Instruments"},
		{"/instruments/one/correct", "/activity?view=review&q=conflict&page=2&size=25&selected=one", "Needs review"},
		{"/evidence/report", "/activity?view=instruments&q=exact&page=3", "Instruments"},
		{"/activity/one", "/activity?sort=amount&dir=asc&page=2", "Activity"},
	} {
		path := tc.path
		if tc.origin != "" {
			path += "?origin=" + url.QueryEscape(tc.origin)
		}

		r := pageRequest(httptest.NewRequest(http.MethodGet, path, nil))
		if workspaceSection(r.Context(), "Activity") != tc.section || originLabel(r.Context()) != tc.section ||
			parentLabel(r.Context()) != tc.section {
			t.Fatal("workspace context lost", tc, origin(r.Context()), returnURL(r.Context()))
		}

		if tc.origin != "" && returnURL(r.Context()) != localDestination(tc.origin) {
			t.Fatal("return filters changed", tc, returnURL(r.Context()))
		}
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
		"/status",
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
