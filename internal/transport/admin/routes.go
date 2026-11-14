// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package admin

import (
	"net/http"

	"github.com/a-h/templ"
)

func registerLoginRoutes(mux *http.ServeMux, auth *authenticator) {
	mux.HandleFunc("GET /login", func(w http.ResponseWriter, r *http.Request) {
		record, key, err := auth.state.Browser()
		clear(key[:])

		if err != nil {
			rejection(r, "invalid_auth_state")
			http.Error(w, "browser authentication unavailable", http.StatusServiceUnavailable)

			return
		}

		if record == "" {
			http.Error(w, "Set the operator unlock with wake admin set-unlock.", http.StatusServiceUnavailable)
			return
		}

		templ.Handler(Login("")).ServeHTTP(w, r)
	})

	mux.HandleFunc("POST /login", func(w http.ResponseWriter, r *http.Request) {
		if !auth.sameOrigin(r) {
			rejection(r, "invalid_origin")
			http.Error(w, "invalid origin", http.StatusForbidden)

			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, maxLoginBodyBytes)
		if err := r.ParseForm(); err != nil || !auth.login(w, r.PostForm.Get("unlock")) {
			rejection(r, "login_rejected")
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusUnauthorized)
			_ = Login("invalid credentials").Render(r.Context(), w)

			return
		}

		http.Redirect(w, r, "/", http.StatusSeeOther)
	})
}
