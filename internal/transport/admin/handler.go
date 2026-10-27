// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

// Package admin provides the authenticated administrative shell.
package admin

import (
	"context"
	"embed"
	"io/fs"
	"net/http"
	"strings"

	"github.com/a-h/templ"
	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/config"
)

//go:embed assets
var assets embed.FS

type (
	Status struct {
		CredentialProtection bool
		StartedAt            string
	}
	Queries interface {
		Status(context.Context) (Status, error)
	}
	Dependencies struct {
		Queries  Queries
		Config   config.Config
		Workflow *application.Workflow
	}
)

func NewHandler(auth *authenticator, deps Dependencies) (http.Handler, error) {
	if auth == nil {
		return nil, ErrInvalidAuthConfiguration
	}

	mux := http.NewServeMux()

	assetFS, err := fs.Sub(assets, "assets")
	if err != nil {
		return nil, err
	}

	mux.Handle("GET /assets/", cacheAssets(http.StripPrefix("/assets/", http.FileServerFS(assetFS))))
	registerLoginRoutes(mux, auth)

	if deps.Workflow != nil {
		registerWorkflowRoutes(mux, auth, deps.Workflow)
	}

	for _, path := range []string{"/{$}", "/configuration", "/status"} {
		mux.HandleFunc("GET "+path, func(w http.ResponseWriter, r *http.Request) {
			csrf, ok := auth.session(r)
			if !ok {
				if auth.mode == "local" {
					http.Redirect(w, r, "/login", http.StatusSeeOther)
				} else {
					http.Error(w, "authentication required", http.StatusUnauthorized)
				}

				return
			}

			status := Status{}
			if deps.Queries != nil {
				var statusErr error

				status, statusErr = deps.Queries.Status(r.Context())
				if statusErr != nil {
					http.Error(w, "status unavailable", http.StatusServiceUnavailable)
					return
				}
			}

			templ.Handler(Overview(csrf, r.URL.Path, status, deps.Config)).ServeHTTP(w, r)
		})
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setSecurityHeaders(w.Header())

		if !auth.correctHost(r) {
			http.Error(w, "invalid host", http.StatusBadRequest)
			return
		}

		if !auth.proxyAuthorized(r) {
			http.Error(w, "proxy authentication required", http.StatusUnauthorized)
			return
		}

		mux.ServeHTTP(w, r)
	}), nil
}

func setSecurityHeaders(headers http.Header) {
	headers.Set("Cache-Control", "no-store")
	headers.Set(
		"Content-Security-Policy",
		"default-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'; object-src 'none'",
	)
	headers.Set("Permissions-Policy", "camera=(), geolocation=(), microphone=()")
	// Preserve Origin on native form POSTs while withholding cross-origin referrers.
	headers.Set("Referrer-Policy", "same-origin")
	headers.Set("X-Content-Type-Options", "nosniff")
	headers.Set("X-Frame-Options", "DENY")
}

func cacheAssets(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		cacheControl := "no-cache"
		if strings.HasPrefix(request.URL.Path, "/assets/vendor/") {
			cacheControl = "public, max-age=31536000, immutable"
		}

		response.Header().Set("Cache-Control", cacheControl)
		response.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(response, request)
	})
}
