// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package admin

import (
	"crypto/subtle"
	"net/http"
)

func (auth *authenticator) gate(
	mux *http.ServeMux,
	pattern string,
	handler func(http.ResponseWriter, *http.Request, string),
) {
	mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		csrf, ok := auth.session(r)
		if !ok {
			rejection(r, "session_required")

			if r.Header.Get("HX-Request") == "true" {
				http.Error(w, "authentication required", http.StatusUnauthorized)
			} else {
				http.Redirect(w, r, "/login", http.StatusSeeOther)
			}

			return
		}

		if r.Method == http.MethodPost {
			if !auth.sameOrigin(r) {
				rejection(r, "invalid_origin")
				http.Error(w, "invalid origin", http.StatusForbidden)

				return
			}

			limit := int64(16 << 10)
			switch r.Pattern {
			case "POST /connections", "POST /connections/{id}/settings", "POST /connections/{id}/secrets":
				// The SDK admits 64 fields of up to 4096 bytes, plus field names,
				// host metadata and at most three URL-encoded bytes per input byte.
				limit = 1 << 20
			}

			r.Body = http.MaxBytesReader(w, r.Body, limit)
			if r.ParseForm() != nil {
				rejection(r, "invalid_form")
				http.Error(w, "invalid form", http.StatusBadRequest)

				return
			}

			if subtle.ConstantTimeCompare([]byte(csrf), []byte(r.PostForm.Get("csrf"))) != 1 {
				rejection(r, "invalid_csrf")
				http.Error(w, "invalid CSRF token", http.StatusForbidden)

				return
			}
		}

		handler(w, r, csrf)
	})
}
