// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.trippwill.dev/wake/internal/config"
)

func TestShellRoutesAndNavigation(t *testing.T) {
	dir := privateAuthDir(t)
	if err := SetUnlock(dir, []byte("synthetic-shell-unlock")); err != nil {
		t.Fatal(err)
	}

	auth, _, err := Open(dir, "http://127.0.0.1:8787")
	if err != nil {
		t.Fatal(err)
	}

	h, err := NewHandler(auth, Dependencies{Config: config.Config{Admin: config.Admin{CurrencyRefresh: true}}})
	if err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	issueTestSession(t, auth, w)

	cookie := w.Result().Cookies()[0]
	for _, path := range []string{"/", "/system/configuration", "/system", "/trades", "/documents", "/connections", "/processing"} {
		r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8787"+path, nil)
		r.AddCookie(cookie)

		w = httptest.NewRecorder()
		h.ServeHTTP(w, r)

		want := http.StatusOK
		if path != "/" && path != "/system/configuration" && path != "/system" {
			want = http.StatusNotFound
		}

		if w.Code != want {
			t.Fatal(path, w.Code)
		}

		if want == http.StatusOK {
			for _, destination := range []string{"/", "/activity", "/connections", "/system", "/system/configuration"} {
				if !strings.Contains(w.Body.String(), `href="`+destination+`"`) {
					t.Errorf("%s has no navigation to %s", path, destination)
				}
			}

			if path == "/system/configuration" {
				for _, text := range []string{"Configuration", "Manage source and reference access", "<dt>Currency list refresh</dt><dd>Enabled</dd>"} {
					if !strings.Contains(w.Body.String(), text) {
						t.Errorf("configuration page missing %q", text)
					}
				}
			} else if !strings.Contains(w.Body.String(), "Daemon running") {
				t.Errorf("%s missing daemon status", path)
			}
		}

		for _, retired := range []string{`href="/trades"`, `href="/documents"`, `href="/processing"`} {
			if strings.Contains(w.Body.String(), retired) {
				t.Fatal("retired navigation", retired)
			}
		}
	}
}
