// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package admin

import (
	"errors"
	"net/http"

	"github.com/a-h/templ"
	"go.trippwill.dev/wake/internal/clientaccess"
)

const passwordPath = "/system/settings/password"

func registerLoginRoutes(mux *http.ServeMux, auth *authenticator) {
	mux.HandleFunc("GET /login", func(w http.ResponseWriter, r *http.Request) {
		access, err := auth.state.BrowserAccess()
		defer clear(access.Key[:])

		if err != nil {
			rejection(r, "invalid_auth_state")
			http.Error(w, "browser authentication unavailable", http.StatusServiceUnavailable)

			return
		}

		if access.Password == "" && len(access.Passkeys) == 0 {
			templ.Handler(PasswordRequired()).ServeHTTP(w, r)
			return
		}

		templ.Handler(Login("", auth.accessView(r, access))).ServeHTTP(w, r)
	})

	mux.HandleFunc("POST /login", func(w http.ResponseWriter, r *http.Request) {
		if !auth.parsePublicForm(w, r, maxLoginBodyBytes) {
			return
		}

		if !auth.login(w, r.PostForm.Get("password")) {
			rejection(r, "login_rejected")

			access, err := auth.state.BrowserAccess()
			defer clear(access.Key[:])

			if err != nil {
				http.Error(w, "browser authentication unavailable", http.StatusServiceUnavailable)
				return
			}

			renderAuthForm(
				w,
				r,
				http.StatusUnauthorized,
				Login("Incorrect password or too many attempts. Try again.", auth.accessView(r, access)),
			)

			return
		}

		http.Redirect(w, r, "/", http.StatusSeeOther)
	})

	auth.gate(mux, "GET "+passwordPath, func(w http.ResponseWriter, r *http.Request, csrf string) {
		templ.Handler(ChangePassword(csrf, "")).ServeHTTP(w, r)
	})
	auth.gate(mux, "POST "+passwordPath, auth.changePassword)
	auth.gate(mux, "POST /logout", func(w http.ResponseWriter, r *http.Request, csrf string) {
		auth.revokeSession(csrf)

		//nolint:gosec // Remove the existing cookie over Wake's HTTP loopback listener.
		http.SetCookie(w, &http.Cookie{
			Name: sessionCookie, Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode,
		})
		http.Redirect(w, r, "/login", http.StatusSeeOther)
	})
}

func (a *authenticator) parsePublicForm(w http.ResponseWriter, r *http.Request, limit int64) bool {
	if !a.sameOrigin(r) {
		rejection(r, "invalid_origin")
		http.Error(w, "invalid origin", http.StatusForbidden)

		return false
	}

	r.Body = http.MaxBytesReader(w, r.Body, limit)
	if r.ParseForm() != nil {
		rejection(r, "invalid_form")
		http.Error(w, "invalid form", http.StatusBadRequest)

		return false
	}

	return true
}

func (a *authenticator) changePassword(w http.ResponseWriter, r *http.Request, csrf string) {
	current, key, err := a.state.Browser()
	defer clear(key[:])

	if err != nil {
		renderAuthForm(
			w,
			r,
			http.StatusServiceUnavailable,
			ChangePassword(csrf, "Password change is temporarily unavailable."),
		)

		return
	}

	// Authorize against the same snapshot used to verify the current password.
	if _, ok := a.sessionWithKey(r, key[:]); !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	if !a.reserveAttempt() || !a.tryPasswordWork() {
		renderAuthForm(w, r, http.StatusTooManyRequests, ChangePassword(csrf, "Wait a moment and try again."))
		return
	}
	defer a.finishPasswordWork()

	if !matchesPassword(current, r.PostForm.Get("current")) {
		renderAuthForm(w, r, http.StatusBadRequest, ChangePassword(csrf, "The current password is incorrect."))
		return
	}

	verifier, message := replacementPassword(r)
	if message != "" {
		renderAuthForm(w, r, http.StatusBadRequest, ChangePassword(csrf, message))
		return
	}

	newKey, err := a.state.ChangeBrowserPassword(r.Context(), key, verifier)
	defer clear(newKey[:])

	if errors.Is(err, clientaccess.ErrBrowserChanged) {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	if err != nil {
		renderAuthForm(
			w,
			r,
			http.StatusServiceUnavailable,
			ChangePassword(csrf, "Password change is temporarily unavailable."),
		)

		return
	}

	a.issue(w, newKey[:])
	http.Redirect(w, r, "/system/settings", http.StatusSeeOther)
}

func replacementPassword(r *http.Request) (string, string) {
	value := r.PostForm.Get("password")
	if value != r.PostForm.Get("confirm") {
		return "", "The passwords do not match."
	}

	verifier, err := passwordVerifier([]byte(value))
	switch {
	case errors.Is(err, ErrWeakPassword):
		return "", "Use at least 12 bytes for the password."
	case errors.Is(err, ErrLongPassword):
		return "", "The password is too long."
	case errors.Is(err, ErrInvalidPassword):
		return "", "Use valid text on one line for the password."
	case err != nil:
		return "", "Wake could not prepare the password. Try again."
	default:
		return verifier, ""
	}
}

func renderAuthForm(w http.ResponseWriter, r *http.Request, status int, page templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_ = page.Render(r.Context(), w)
}
