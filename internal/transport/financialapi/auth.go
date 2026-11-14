// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package financialapi

import (
	"errors"
	"net/http"
	"strings"

	"go.trippwill.dev/wake/internal/clientaccess"
)

// Authenticated gates every response, including redirects, validators and events.
func Authenticated(clients *clientaccess.Manager, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")

		values := r.Header.Values("Authorization")
		if len(values) != 1 || len(values[0]) < 7 || !strings.EqualFold(values[0][:7], "Bearer ") {
			unauthorized(w)
			return
		}

		ctx, release, err := clients.Admit(r.Context(), values[0][7:])
		if err != nil {
			if errors.Is(err, clientaccess.ErrUnauthorized) {
				unauthorized(w)
			} else {
				w.Header().Set("Retry-After", "1")
				http.Error(w, "financial access unavailable", http.StatusServiceUnavailable)
			}

			return
		}
		defer release()

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func unauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="wake"`)
	http.Error(w, "client authentication required", http.StatusUnauthorized)
}
