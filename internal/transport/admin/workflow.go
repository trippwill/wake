// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package admin

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/a-h/templ"
	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/credential"
	"go.trippwill.dev/wake/internal/host"
	"go.trippwill.dev/wake/provider"
	"go.trippwill.dev/wake/source"
)

func registerWorkflowRoutes(
	mux *http.ServeMux,
	auth *authenticator,
	workflow *application.Workflow,
	stopping <-chan struct{},
) {
	const invalidDetailsMessage = "Check the correction details, including the optional rationale. Text must be within the allowed length and contain no leading/trailing whitespace or control characters."

	gate := func(pattern string, handler func(http.ResponseWriter, *http.Request, string)) {
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
	registerGridRoutes(gate, workflow)

	failure := func(w http.ResponseWriter, r *http.Request, csrf string, err error) {
		message := "The operation could not be saved. Retry after checking daemon status."
		switch err {
		case host.ErrBusy:
			message = "Connection work is finishing. Wait for its progress to update, then retry."
		case application.ErrInvalid:
			message = "Enter an account display name and a valid currency, or leave currency unknown."

		case application.ErrAccountLimit:
			message = "One account is already activated. Other discovered accounts remain candidates."
		case application.ErrStale, application.ErrAccessReview:
			message = err.Error()
		case application.ErrCandidate:
			message = "This account was not discovered through the selected connection."
		case provider.ErrInvalid:
			message = "Check the connection name and the selected provider’s required settings."
		case provider.ErrUnavailable:
			message = "This source provider or capability is unavailable in this build."
		case provider.ErrUnauthorized:
			message = "Credential protection is unavailable or the credential is invalid. Configure a master key with wake setup and retry."
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusBadRequest)
		_ = SectionShell(
			"Unable to save",
			csrf,
			"Connections",
			SubmissionFailure(
				csrf,
				r.URL.EscapedPath(),
				message,
				r.PostForm,
				requestDescriptor(r, workflow),
				formSubmission(r.Pattern),
			),
		).Render(r.Context(), w)
	}
	gate("GET /connections", func(w http.ResponseWriter, r *http.Request, csrf string) {
		query, err := listQuery(r.Context(), application.SourceScope)
		if err != nil {
			http.Error(w, "invalid list query", http.StatusBadRequest)
			return
		}

		result, err := workflow.SourcePage(r.Context(), query)
		view := sourcePage(result)

		if err != nil {
			servePageError(
				w,
				r,
				csrf,
				http.StatusServiceUnavailable,
				"Temporarily unavailable",
				"workflow unavailable. Retry or inspect daemon status.",
			)

			return
		}

		templ.Handler(SectionShell("Connections", csrf, "Connections", ConnectionsPage(csrf, view))).
			ServeHTTP(w, r)
	})
	gate("GET /connections/new", func(w http.ResponseWriter, r *http.Request, csrf string) {
		templ.Handler(SectionShell("Add source connection", csrf, "Connections", ConnectionSetup(csrf, workflow.Providers()))).
			ServeHTTP(w, r)
	})

	for _, route := range []string{"/connections/reference", "/connections/recovery", "/connections/{id}"} {
		gate("GET "+route, func(w http.ResponseWriter, r *http.Request, csrf string) {
			var (
				view sourceView
				err  error
			)
			if id := r.PathValue("id"); id != "" {
				query, e := listQuery(r.Context(), application.SourceScope)
				if e != nil {
					http.Error(w, "invalid list query", http.StatusBadRequest)
					return
				}

				result, e := workflow.SourceDetail(r.Context(), id, query)
				err = e
				view = sourceDetail(result)
			} else if r.URL.Path == "/connections/recovery" {
				query, e := listQuery(r.Context(), application.SourceScope)
				if e != nil {
					http.Error(w, "invalid list query", http.StatusBadRequest)
					return
				}

				result, ready, e := workflow.Recovery(r.Context(), query)
				err = e
				view = sourcePage(result)
				view.MissingRecoveryAccess = !ready
			} else {
				result, e := workflow.SystemStatus(r.Context())
				err = e
				view = statusView(result)
			}

			if errors.Is(err, application.ErrNotFound) {
				http.NotFound(w, r)
				return
			}

			if err != nil {
				http.Error(w, "source information unavailable", http.StatusServiceUnavailable)
				return
			}

			var (
				content templ.Component
				title   string
			)

			section := "Connections"

			switch r.URL.Path {
			case "/connections/reference":
				title = "Instrument reference"
				content = ReferencePage(csrf, view)
			case "/connections/recovery":
				title = "Recovery"
				content = RecoveryPage(csrf, view)
			default:
				c, ok := connectionByID(view, r.PathValue("id"))
				if !ok {
					http.NotFound(w, r)
					return
				}

				title = c.Name
				content = ConnectionPage(csrf, view, c)
			}

			templ.Handler(SectionShell(title, csrf, section, content)).ServeHTTP(w, r)
		})
	}

	gate("POST /connections", func(w http.ResponseWriter, r *http.Request, csrf string) {
		descriptor := requestDescriptor(r, workflow)
		settings, err := formSettings(r, descriptor)

		secrets := formSecrets(r, descriptor)
		defer clearSecrets(secrets)

		if err != nil {
			failure(w, r, csrf, err)
			return
		}

		connectionID, err := workflow.CreateConnection(
			r.Context(),
			r.PostForm.Get("connection_name"),
			descriptor.ID(),
			settings,
			secrets,
		)
		if err != nil {
			failure(w, r, csrf, err)
			return
		}
		//nolint:gosec // connectionURL fixes the local prefix and escapes the ID.
		http.Redirect(w, r, connectionURL(connectionID, "status"), http.StatusSeeOther)
	})
	gate("POST /connections/{id}/settings", func(w http.ResponseWriter, r *http.Request, csrf string) {
		settings, err := formSettings(r, requestDescriptor(r, workflow))
		if err == nil {
			err = workflow.UpdateConnection(
				r.Context(),
				r.PathValue("id"),
				r.PostForm.Get("connection_name"),
				settings,
			)
		}

		if err != nil {
			failure(w, r, csrf, err)
			return
		}

		destination := connectionURL(r.PathValue("id"), "access")
		if view, e := workflow.Prerequisites(r.Context()); e == nil && view.Storage.Paused {
			destination = "/connections/recovery"
		}
		//nolint:gosec // Destination is either a fixed recovery route or connectionURL's local prefix and escaped ID.
		http.Redirect(w, r, destination, http.StatusSeeOther)
	})

	gate("POST /connections/{id}/secrets", func(w http.ResponseWriter, r *http.Request, csrf string) {
		descriptor := requestDescriptor(r, workflow)

		secrets := formSecrets(r, descriptor)
		defer clearSecrets(secrets)

		if err := workflow.ReplaceSecret(
			r.Context(),
			r.PathValue("id"),
			r.PostForm.Get("secret_key"),
			secrets[r.PostForm.Get("secret_key")],
		); err != nil {
			failure(w, r, csrf, err)
			return
		}

		destination := connectionURL(r.PathValue("id"), "access")
		if view, e := workflow.Prerequisites(r.Context()); e == nil && view.Storage.Paused {
			destination = "/connections/recovery"
		}
		//nolint:gosec // Destination is either a fixed recovery route or connectionURL's local prefix and escaped ID.
		http.Redirect(w, r, destination, http.StatusSeeOther)
	})
	gate("POST /connections/{id}/retry", func(w http.ResponseWriter, r *http.Request, csrf string) {
		if err := workflow.Retry(r.Context(), r.PathValue("id")); err != nil {
			failure(w, r, csrf, err)
			return
		}

		//nolint:gosec // connectionURL fixes the local prefix and escapes the ID as one path segment.
		http.Redirect(w, r, connectionURL(r.PathValue("id"), "status"), http.StatusSeeOther)
	})
	gate("GET /connections/{id}/confirm", func(w http.ResponseWriter, r *http.Request, csrf string) {
		ref, err := source.NewReference(
			r.URL.Query().Get("authority"),
			r.URL.Query().Get("kind"),
			r.URL.Query().Get("scope"),
			r.URL.Query().Get("account"),
		)
		if err == nil {
			candidate, e := workflow.SourceCandidate(
				r.Context(),
				r.PathValue("id"),
				r.URL.Query().Get("artifact"),
				ref,
			)
			if e == nil {
				templ.Handler(SectionShell("Confirm account", csrf, "Connections", ConfirmAccount(csrf, r.PathValue("id"), candidate))).
					ServeHTTP(w, r)

				return
			}

			if !errors.Is(e, application.ErrCandidate) {
				http.Error(w, "accounts unavailable", http.StatusServiceUnavailable)
				return
			}
		}

		servePageError(
			w,
			r,
			csrf,
			http.StatusNotFound,
			"Item unavailable",
			"This item is unavailable. Return to its workspace to inspect current records.",
		)
	})
	gate("POST /connections/{id}/confirm", func(w http.ResponseWriter, r *http.Request, csrf string) {
		details, err := application.NewAccountDetails(r.PostForm.Get("name"), r.PostForm.Get("currency"))
		if err != nil {
			failure(w, r, csrf, err)
			return
		}

		ref, err := source.NewReference(
			r.PostForm.Get("authority"),
			r.PostForm.Get("kind"),
			r.PostForm.Get("scope"),
			r.PostForm.Get("account"),
		)
		if err != nil {
			failure(w, r, csrf, application.ErrCandidate)
			return
		}

		if _, err = workflow.Confirm(
			r.Context(),
			r.PathValue("id"),
			r.PostForm.Get("artifact"),
			ref,
			details,
		); err != nil {
			failure(w, r, csrf, err)
			return
		}

		http.Redirect(w, r, "/activity", http.StatusSeeOther)
	})
	gate("POST /reference/key", func(w http.ResponseWriter, r *http.Request, csrf string) {
		key := credential.NewSecret([]byte(r.PostForm.Get("key")))
		defer key.Clear()

		r.PostForm.Del("key")

		if err := workflow.SetReferenceKey(r.Context(), key); err != nil {
			failure(w, r, csrf, err)
			return
		}

		destination := "/connections/reference"
		if view, e := workflow.Prerequisites(r.Context()); e == nil && view.Storage.Paused {
			destination = "/connections/recovery"
		}

		http.Redirect(w, r, destination, http.StatusSeeOther)
	})
	gate("GET /activity", func(w http.ResponseWriter, r *http.Request, csrf string) {
		var (
			view activityView
			err  error
		)

		scope := application.ActivityScope
		switch activeView(r.Context(), "all") {
		case "instruments":
			scope = application.InstrumentScope
		case "review":
			scope = application.ReviewScope
		}

		query, err := listQuery(r.Context(), scope)
		if err != nil {
			http.Error(w, "invalid list query", http.StatusBadRequest)
			return
		}

		switch scope {
		case application.ActivityScope:
			result, e := workflow.Prerequisites(r.Context())
			err = e
			view.Account = result.Account
		case application.InstrumentScope:
			result, e := workflow.Prerequisites(r.Context())
			err = e
			view.Account = result.Account
		case application.ReviewScope:
			result, e := workflow.ReviewList(r.Context(), query)
			err = e
			view = activityView{PageInfo: result.PageInfo, Reviews: reviewItems(result)}
		}

		if err != nil {
			servePageError(
				w,
				r,
				csrf,
				http.StatusServiceUnavailable,
				"Temporarily unavailable",
				"activity unavailable. Retry or inspect daemon status.",
			)

			return
		}

		templ.Handler(SectionShell("Activity", csrf, "Activity", ActivityPage(view))).ServeHTTP(w, r)
	})
	gate("GET /executions/{id}", func(w http.ResponseWriter, r *http.Request, csrf string) {
		detail, err := workflow.ExecutionDetail(r.Context(), r.PathValue("id"))
		if err != nil {
			servePageError(
				w,
				r,
				csrf,
				http.StatusNotFound,
				"Item unavailable",
				"This item is unavailable. Return to its workspace to inspect current records.",
			)

			return
		}

		templ.Handler(SectionShell(stringTitle(string(detail.Execution.Terms().Fields().Side))+" "+known(detail.Context.InstrumentName), csrf, "Activity", ExecutionPage(detail))).
			ServeHTTP(w, r)
	})

	gate("GET /activity/{id}", func(w http.ResponseWriter, r *http.Request, csrf string) {
		if d, err := workflow.ExecutionDetail(r.Context(), r.PathValue("id")); err == nil {
			templ.Handler(SectionShell(stringTitle(string(d.Execution.Terms().Fields().Side))+" "+known(d.Context.InstrumentName), csrf, "Activity", ExecutionPage(d))).
				ServeHTTP(w, r)

			return
		}

		if d, err := workflow.ConversionDetail(r.Context(), r.PathValue("id")); err == nil {
			templ.Handler(SectionShell("Currency conversion", csrf, "Activity", ConversionPage(d))).ServeHTTP(w, r)

			return
		}

		d, err := workflow.LifecycleDetail(r.Context(), r.PathValue("id"))
		if err != nil {
			servePageError(
				w,
				r,
				csrf,
				http.StatusNotFound,
				"Item unavailable",
				"This item is unavailable. Return to its workspace to inspect current records.",
			)

			return
		}

		templ.Handler(SectionShell(stringTitle(string(d.Lifecycle.Terms().Fields().Kind))+" "+known(d.Context.InstrumentName), csrf, "Activity", LifecyclePage(d))).
			ServeHTTP(w, r)
	})
	gate("GET /instruments/{id}", func(w http.ResponseWriter, r *http.Request, csrf string) {
		detail, err := workflow.InstrumentDetail(r.Context(), r.PathValue("id"))
		if err != nil {
			servePageError(
				w,
				r,
				csrf,
				http.StatusNotFound,
				"Item unavailable",
				"This item is unavailable. Return to its workspace to inspect current records.",
			)

			return
		}

		if detail.Instrument.ID() != r.PathValue("id") {
			http.Redirect(w, r, taskURL(r.Context(), "/instruments/"+detail.Instrument.ID()), http.StatusSeeOther)
			return
		}

		templ.Handler(SectionShell(known(detail.Instrument.Name()), csrf, "Activity", InstrumentPage(detail))).
			ServeHTTP(w, r)
	})
	gate("GET /evidence/{id}", func(w http.ResponseWriter, r *http.Request, csrf string) {
		evidence, err := workflow.Evidence(r.Context(), r.PathValue("id"))
		if err != nil {
			servePageError(
				w,
				r,
				csrf,
				http.StatusNotFound,
				"Item unavailable",
				"This item is unavailable. Return to its workspace to inspect current records.",
			)

			return
		}

		metadata, err := workflow.EvidenceContext(r.Context(), r.PathValue("id"))
		if err != nil {
			servePageError(
				w,
				r,
				csrf,
				http.StatusServiceUnavailable,
				"Evidence temporarily unavailable",
				"Retained evidence context could not be read. Retry this page.",
			)

			return
		}

		templ.Handler(SectionShell("Source report", csrf, "Activity", RetainedEvidence(evidence, metadata))).
			ServeHTTP(w, r)
	})
	gate("GET /reference-evidence/{id}", func(w http.ResponseWriter, r *http.Request, csrf string) {
		evidence, err := workflow.ReferenceEvidence(r.Context(), r.PathValue("id"))
		if err != nil {
			servePageError(
				w,
				r,
				csrf,
				http.StatusNotFound,
				"Item unavailable",
				"This item is unavailable. Return to its workspace to inspect current records.",
			)

			return
		}

		templ.Handler(SectionShell("Reference evidence", csrf, "Activity", EvidencePage("OpenFIGI mapping", evidence.Retrieved, evidence.Request+"\n"+evidence.Response))).
			ServeHTTP(w, r)
	})

	gate("POST /processing/{id}/retry", func(w http.ResponseWriter, r *http.Request, csrf string) {
		if err := workflow.RetryProcessing(r.Context(), r.PathValue("id")); err != nil {
			failure(w, r, csrf, err)
			return
		}

		destination := "/connections"
		if retained, e := workflow.Evidence(r.Context(), r.PathValue("id")); e == nil {
			destination = connectionURL(retained.Connection, "reports")
		}

		http.Redirect(w, r, destination, http.StatusSeeOther)
	})
	gate("POST /recovery/resume", func(w http.ResponseWriter, r *http.Request, csrf string) {
		revision, err := strconv.ParseInt(r.PostForm.Get("revision"), 10, 64)
		if err != nil {
			http.Error(w, "invalid recovery revision", http.StatusBadRequest)
			return
		}

		digest := sha256.Sum256([]byte(csrf))
		actor := "admin/" + auth.mode + "/" + hex.EncodeToString(digest[:12])
		if err = workflow.ResumeStorage(r.Context(), revision, actor); err != nil {
			failure(w, r, csrf, err)
			return
		}

		http.Redirect(w, r, "/connections", http.StatusSeeOther)
	})

	for _, kind := range []string{"activity", "instrument"} {
		path := "/activity/{id}/correct"
		if kind == "instrument" {
			path = "/instruments/{id}/correct"
		}

		gate("GET "+path, func(w http.ResponseWriter, r *http.Request, csrf string) {
			form, err := workflow.CorrectionForm(r.Context(), kind, r.PathValue("id"))
			if err != nil {
				servePageError(
					w,
					r,
					csrf,
					http.StatusNotFound,
					"Item unavailable",
					"This item is unavailable. Return to its workspace to inspect current records.",
				)

				return
			}

			if kind == "instrument" && form.ID != r.PathValue("id") {
				http.Redirect(w, r, taskURL(r.Context(), "/instruments/"+form.ID+"/correct"), http.StatusSeeOther)
				return
			}

			templ.Handler(SectionShell("Correct accepted data", csrf, "Activity", CorrectionPage(csrf, form))).
				ServeHTTP(w, r)
		})
		gate("POST "+path+"/check", func(w http.ResponseWriter, r *http.Request, _ string) {
			w.Header().Set("Content-Type", "application/json")

			decision, err := postedCorrection(r, kind)

			invalidDetails := err != nil
			if err == nil {
				err = workflow.CheckCorrection(r.Context(), decision)
			}

			valid, stale, message := err == nil, errors.Is(
				err,
				application.ErrStale,
			), "Ready to save. Saving checks the current record again."
			if err != nil {
				message = invalidDetailsMessage
				if !invalidDetails {
					message = "This value is not valid for the selected field or its financial terms."
					if form, e := workflow.CorrectionForm(r.Context(), kind, r.PathValue("id")); e == nil {
						message = fieldLabel(
							r.PostForm.Get("scope"),
						) + ": " + correctionSpec(
							form,
							r.PostForm.Get("scope"),
						).Hint
					}
				}

				if stale {
					message = "Accepted values changed. Review the updated record before saving."
				}

				if !stale && !errors.Is(err, application.ErrInvalid) {
					w.WriteHeader(http.StatusServiceUnavailable)

					message = "Validation is unavailable. Retry before saving."
				}
			}

			_ = json.NewEncoder(w).Encode(struct {
				Valid   bool   `json:"valid"`
				Stale   bool   `json:"stale"`
				Message string `json:"message"`
			}{valid, stale, message})
		})
		gate("POST "+path, func(w http.ResponseWriter, r *http.Request, csrf string) {
			decision, err := postedCorrection(r, kind)
			invalidDetails := err != nil
			// Attribution comes from the authenticated session, never posted form data.
			digest := sha256.Sum256([]byte(csrf))

			actor := "admin/" + auth.mode + "/" + hex.EncodeToString(digest[:12])
			if err == nil {
				err = workflow.SaveDecision(r.Context(), decision, actor)
			}

			if err != nil {
				status := http.StatusBadRequest

				message := "The correction is invalid for this field or link. Check its value, currency and target, then reload the correction form."
				if invalidDetails {
					message = invalidDetailsMessage
				}

				if err == application.ErrStale {
					status = http.StatusConflict
					message = "Accepted values changed while you were editing. Review the current values before saving again."
				}

				form, loadErr := workflow.CorrectionForm(r.Context(), kind, r.PathValue("id"))
				if loadErr != nil {
					servePageError(
						w,
						r,
						csrf,
						http.StatusServiceUnavailable,
						"Temporarily unavailable",
						"correction unavailable. Retry or inspect daemon status.",
					)

					return
				}

				form.Draft = application.CorrectionDraft{
					Scope:     r.PostForm.Get("scope"),
					Value:     r.PostForm.Get("value"),
					Currency:  r.PostForm.Get("currency"),
					Rationale: r.PostForm.Get("rationale"),
					Action:    r.PostForm.Get("action"),
				}
				form.Message = message
				form.Stale = status == http.StatusConflict

				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.WriteHeader(status)
				_ = SectionShell(
					"Correction not saved",
					csrf,
					"Activity",
					CorrectionPage(csrf, form),
				).Render(r.Context(), w)

				return
			}

			destination := "/activity/" + url.PathEscape(r.PathValue("id")) + "/correct"
			if kind == "instrument" {
				destination = "/instruments/" + url.PathEscape(r.PathValue("id")) + "/correct"
			}

			//nolint:gosec // Fixed local route plus one escaped entity ID; cannot redirect to another origin.
			target, _ := url.Parse(taskURL(r.Context(), destination))
			query := target.Query()
			query.Set("mode", "decisions")
			target.RawQuery = query.Encode()
			http.Redirect(w, r, target.String(), http.StatusSeeOther)
		})
	}

	gate("GET /events", func(w http.ResponseWriter, r *http.Request, _ string) {
		if http.NewResponseController(w).SetWriteDeadline(time.Time{}) != nil {
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Accel-Buffering", "no")

		ch, unsubscribe := workflow.Subscribe()
		defer unsubscribe()

		ticker := time.NewTicker(20 * time.Second)
		defer ticker.Stop()

		if _, err := fmt.Fprint(w, ": connected\n\n"); err != nil {
			return
		}

		if http.NewResponseController(w).Flush() != nil {
			return
		}

		for {
			select {
			case <-stopping:
				return
			case <-r.Context().Done():
				return
			case <-ch:
				if _, err := fmt.Fprint(w, "event: refresh\ndata: {}\n\n"); err != nil {
					return
				}
			case <-ticker.C:
				if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
					return
				}
			}

			if http.NewResponseController(w).Flush() != nil {
				return
			}
		}
	})
}

func settingValue(settings provider.Settings, key string) string {
	value, _ := settings.Get(key)
	return value
}

func requestDescriptor(r *http.Request, workflow *application.Workflow) provider.Descriptor {
	id := r.PostForm.Get("provider")
	if connection := r.PathValue("id"); connection != "" {
		descriptor, _ := workflow.ConnectionDescriptor(r.Context(), connection)
		return descriptor
	}

	registration, ok := workflow.Provider(id)
	if !ok {
		return provider.Descriptor{}
	}

	return registration.Descriptor()
}

func formSettings(r *http.Request, descriptor provider.Descriptor) (provider.Settings, error) {
	if descriptor.ID() == "" {
		return provider.Settings{}, provider.ErrUnavailable
	}

	values := map[string]string{}
	for _, field := range descriptor.Fields() {
		if field.Kind() == provider.TextField {
			if value := r.PostForm.Get("setting." + field.Key()); value != "" {
				values[field.Key()] = value
			}
		}
	}

	return provider.NewSettings(values)
}

func formSecrets(r *http.Request, descriptor provider.Descriptor) map[string]*credential.Secret {
	secrets := map[string]*credential.Secret{}
	for _, field := range descriptor.Fields() {
		if field.Kind() == provider.SecretField {
			key := "secret." + field.Key()
			if value := r.PostForm.Get(key); value != "" {
				secrets[field.Key()] = credential.NewSecret([]byte(value))
			}

			r.PostForm.Del(key)
		}
	}
	// Unknown secret fields must never survive in a rendered draft either.
	for key := range r.PostForm {
		if strings.HasPrefix(key, "secret.") {
			r.PostForm.Del(key)
		}
	}

	return secrets
}

func clearSecrets(secrets map[string]*credential.Secret) {
	for _, secret := range secrets {
		secret.Clear()
	}
}

type submissionKind uint8

const (
	submissionNone submissionKind = iota
	submissionCreate
	submissionSettings
	submissionConfirm
	submissionSecret
	submissionReference
)

func formSubmission(pattern string) submissionKind {
	switch pattern {
	case "POST /connections":
		return submissionCreate
	case "POST /connections/{id}/settings":
		return submissionSettings
	case "POST /connections/{id}/confirm":
		return submissionConfirm
	case "POST /connections/{id}/secrets":
		return submissionSecret
	case "POST /reference/key":
		return submissionReference
	default:
		return submissionNone
	}
}

// providerFieldID separates the form scope, owner and field into unambiguous
// parts. Opaque IDs remain stable across refreshes for focus and label targets.
func providerFieldID(scope, owner string, field provider.Field) string {
	sum := sha256.Sum256([]byte(scope + "\x00" + owner + "\x00" + string(field.Kind()) + "\x00" + field.Key()))
	return "provider-field-" + hex.EncodeToString(sum[:])
}
