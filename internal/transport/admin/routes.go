// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package admin

import (
	"net/http"

	"github.com/a-h/templ"
)

func registerLoginRoutes(mux *http.ServeMux, auth *authenticator) {
	mux.HandleFunc("GET /login", func(w http.ResponseWriter, r *http.Request) {
		if auth.mode != "local" {
			http.NotFound(w, r)
			return
		}

		templ.Handler(Login("")).ServeHTTP(w, r)
	})

	mux.HandleFunc("POST /login", func(w http.ResponseWriter, r *http.Request) {
		if auth.mode != "local" {
			http.NotFound(w, r)
			return
		}

		if !auth.sameOrigin(r) {
			http.Error(w, "invalid origin", http.StatusForbidden)
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, maxLoginBodyBytes)
		if err := r.ParseForm(); err != nil || !auth.verifyUnlock(r.PostForm.Get("unlock")) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusUnauthorized)
			_ = Login("invalid credentials").Render(r.Context(), w)

			return
		}

		if _, err := auth.issue(w); err != nil {
			http.Error(w, "session error", http.StatusInternalServerError)
			return
		}

		http.Redirect(w, r, "/", http.StatusSeeOther)
	})
}
