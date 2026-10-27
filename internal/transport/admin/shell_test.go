// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestShellRoutesAndNavigation(t *testing.T) {
	dir := t.TempDir()
	if err := SetUnlock(dir, []byte("synthetic-shell-unlock")); err != nil {
		t.Fatal(err)
	}

	auth, _, err := Open(dir, "local", "http://127.0.0.1:8787")
	if err != nil {
		t.Fatal(err)
	}
	defer auth.Close()

	h, err := NewHandler(auth, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	if _, err = auth.issue(w); err != nil {
		t.Fatal(err)
	}

	cookie := w.Result().Cookies()[0]
	for _, path := range []string{"/", "/configuration", "/status", "/trades", "/documents", "/connections", "/processing"} {
		r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8787"+path, nil)
		r.AddCookie(cookie)

		w = httptest.NewRecorder()
		h.ServeHTTP(w, r)

		want := http.StatusOK
		if path != "/" && path != "/configuration" && path != "/status" {
			want = http.StatusNotFound
		}

		if w.Code != want {
			t.Fatal(path, w.Code)
		}

		for _, retired := range []string{`href="/trades"`, `href="/documents"`, `href="/processing"`} {
			if strings.Contains(w.Body.String(), retired) {
				t.Fatal("retired navigation", retired)
			}
		}
	}
}
