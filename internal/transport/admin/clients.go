// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package admin

import (
	"errors"
	"net/http"

	"github.com/a-h/templ"
	"go.trippwill.dev/wake/internal/clientaccess"
)

func registerClientRoutes(mux *http.ServeMux, auth *authenticator, clients *clientaccess.Manager) {
	render := func(w http.ResponseWriter, r *http.Request, csrf, issued, message string) {
		if clients == nil {
			http.Error(w, "client access unavailable", http.StatusServiceUnavailable)
			return
		}

		list, err := clients.Clients(r.Context())
		if err != nil {
			http.Error(w, "client access unavailable", http.StatusServiceUnavailable)
			return
		}

		templ.Handler(SectionShell("API clients", csrf, "System", ClientsPage(csrf, list, issued, message))).
			ServeHTTP(w, r)
	}
	auth.gate(mux, "GET /system/clients", func(w http.ResponseWriter, r *http.Request, csrf string) {
		render(w, r, csrf, "", "")
	})
	auth.gate(mux, "POST /system/clients", func(w http.ResponseWriter, r *http.Request, csrf string) {
		if clients == nil {
			http.Error(w, "client access unavailable", http.StatusServiceUnavailable)
			return
		}

		_, token, err := clients.Create(r.Context(), r.PostForm.Get("name"))
		if err != nil {
			if errors.Is(err, clientaccess.ErrInvalidClient) {
				w.WriteHeader(http.StatusBadRequest)
				render(
					w,
					r,
					csrf,
					"",
					"Use a client name of 1–100 bytes without surrounding whitespace or control characters.",
				)

				return
			}

			if errors.Is(err, clientaccess.ErrBusy) {
				w.WriteHeader(http.StatusConflict)
				render(w, r, csrf, "", "Revoke an unused client before creating another.")

				return
			}

			http.Error(w, "client could not be created", http.StatusServiceUnavailable)

			return
		}
		defer token.Clear()

		value, err := token.Copy()
		if err != nil {
			http.Error(
				w,
				"client token unavailable; revoke the new client and try again",
				http.StatusInternalServerError,
			)

			return
		}
		defer clear(value)

		render(w, r, csrf, string(value), "")
	})
	auth.gate(mux, "POST /system/clients/{id}/revoke", func(w http.ResponseWriter, r *http.Request, _ string) {
		if clients == nil || clients.Revoke(r.Context(), r.PathValue("id")) != nil {
			http.Error(w, "client could not be revoked", http.StatusServiceUnavailable)
			return
		}

		http.Redirect(w, r, "/system/clients", http.StatusSeeOther)
	})
}
