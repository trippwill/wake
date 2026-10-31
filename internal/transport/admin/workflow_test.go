// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package admin

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"go.trippwill.dev/wake/internal/application"
)

func TestWorkflowCommandsRequireSessionOriginAndCSRF(t *testing.T) {
	dir := t.TempDir()
	if err := SetUnlock(dir, []byte("synthetic-workflow-unlock")); err != nil {
		t.Fatal(err)
	}

	auth, _, err := Open(dir, "local", "http://127.0.0.1:8787")
	if err != nil {
		t.Fatal(err)
	}
	defer auth.Close()
	// No application dependencies: every request below must stop at the gate.
	handler, err := NewHandler(auth, Dependencies{Workflow: new(application.Workflow)})
	if err != nil {
		t.Fatal(err)
	}

	login := httptest.NewRecorder()

	csrf := auth.issue(login)

	cookie := login.Result().Cookies()[0]
	for _, path := range []string{"/connections", "/connections/id/settings", "/connections/id/secrets", "/connections/id/retry", "/connections/id/confirm", "/reference/key", "/activity/id/correct", "/instruments/id/correct", "/processing/id/retry"} {
		for _, test := range []struct {
			name, origin, csrf string
			authenticated      bool
			want               int
		}{{"anonymous", "http://127.0.0.1:8787", csrf, false, http.StatusSeeOther}, {"origin", "http://attacker.invalid", csrf, true, http.StatusForbidden}, {"csrf", "http://127.0.0.1:8787", "invalid", true, http.StatusForbidden}} {
			t.Run(path+"/"+test.name, func(t *testing.T) {
				r := httptest.NewRequest(
					http.MethodPost,
					"http://127.0.0.1:8787"+path,
					strings.NewReader(url.Values{"csrf": {test.csrf}}.Encode()),
				)
				r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				r.Header.Set("Origin", test.origin)

				if test.authenticated {
					r.AddCookie(cookie)
				}

				w := httptest.NewRecorder()
				handler.ServeHTTP(w, r)

				if w.Code != test.want {
					t.Fatal(w.Code, w.Body.String())
				}
			})
		}
	}
}
