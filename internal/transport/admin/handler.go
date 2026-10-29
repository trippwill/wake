// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

// Package admin provides the authenticated administrative shell.
package admin

import (
	"context"
	"embed"
	"io/fs"
	"log/slog"
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
		Logger   *slog.Logger
		Queries  Queries
		Config   config.Config
		Workflow *application.Workflow
		Stopping <-chan struct{}
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
		registerWorkflowRoutes(mux, auth, deps.Workflow, deps.Stopping)
	}

	mux.HandleFunc("GET /status/contact", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.session(r); !ok {
			rejection(r, "session_required")
			http.Error(w, "authentication required", http.StatusUnauthorized)

			return
		}

		if deps.Queries != nil {
			if _, err := deps.Queries.Status(r.Context()); err != nil {
				http.Error(w, "status unavailable", http.StatusServiceUnavailable)
				return
			}
		}

		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("Daemon connected"))
	})

	for _, path := range []string{"/{$}", "/configuration", "/status"} {
		mux.HandleFunc("GET "+path, func(w http.ResponseWriter, r *http.Request) {
			csrf, ok := auth.session(r)
			if !ok {
				rejection(r, "session_required")

				if r.Header.Get("HX-Request") == "true" {
					http.Error(w, "authentication required", http.StatusUnauthorized)
					return
				}

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
					servePageError(
						w,
						r,
						csrf,
						http.StatusServiceUnavailable,
						"Temporarily unavailable",
						"status unavailable. Retry or inspect daemon status.",
					)

					return
				}
			}

			if r.URL.Path == "/" && deps.Workflow != nil {
				view, err := deps.Workflow.Dashboard(r.Context())
				if err != nil {
					servePageError(
						w,
						r,
						csrf,
						http.StatusServiceUnavailable,
						"Temporarily unavailable",
						"overview unavailable. Retry or inspect daemon status.",
					)

					return
				}

				templ.Handler(SectionShell("Overview", csrf, "Overview", DashboardOverview(view))).ServeHTTP(w, r)

				return
			}

			templ.Handler(Overview(csrf, r.URL.Path, status, deps.Config)).ServeHTTP(w, r)
		})
	}

	return diagnostics(deps.Logger, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setSecurityHeaders(w.Header())

		if !auth.correctHost(r) {
			rejection(r, "invalid_host")
			http.Error(w, "invalid host", http.StatusBadRequest)

			return
		}

		if !auth.proxyAuthorized(r) {
			rejection(r, "proxy_auth_required")
			http.Error(w, "proxy authentication required", http.StatusUnauthorized)

			return
		}

		if auth.mode == "proxy" && documentNavigation(r) && r.URL.Path != "/login" {
			if _, ok := auth.session(r); !ok {
				r = auth.establishSession(w, r)
			}
		}

		w.Header().Set("Vary", "HX-Request")

		*r = *pageRequest(r)
		mux.ServeHTTP(w, r)
	})), nil
}

func documentNavigation(r *http.Request) bool {
	if (r.Method != http.MethodGet && r.Method != http.MethodHead) || r.Header.Get("HX-Request") == "true" {
		return false
	}

	if strings.HasPrefix(r.URL.Path, "/assets/") {
		return false
	}

	mode := r.Header.Get("Sec-Fetch-Mode")

	return mode == "navigate" || (mode == "" && strings.Contains(r.Header.Get("Accept"), "text/html"))
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
