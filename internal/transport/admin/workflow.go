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

				r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
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
	failure := func(w http.ResponseWriter, r *http.Request, csrf string, err error) {
		message := "The operation could not be saved. Retry after checking daemon status."
		switch err {
		case host.ErrBusy:
			message = "Connection work is finishing. Wait for its progress to update, then retry."
		case application.ErrInvalid:
			message = "Enter an account display name and a valid currency, or leave currency unknown."
			if r.Pattern == "POST /recovery/{id}/retry" {
				message = "This rebuild is no longer eligible for retry. Review the latest rebuild status."
			}
		case application.ErrAccountLimit:
			message = "One account is already activated. Other discovered accounts remain candidates."
		case application.ErrStale, application.ErrAccessReview:
			message = err.Error()
		case application.ErrRecoveryGroup:
			message = err.Error()
		case application.ErrCandidate:
			message = "This account was not discovered through the selected connection."
		case provider.ErrInvalid:
			message = "Enter a connection name and a numeric Confirmation Query ID."
		case provider.ErrUnauthorized:
			message = "Credential protection is unavailable or the credential is invalid. Configure a master key with wake setup and retry."
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusBadRequest)
		_ = SectionShell(
			"Unable to save",
			csrf,
			"Connections",
			SubmissionFailure(csrf, r.URL.Path, message, r.PostForm),
		).Render(r.Context(), w)
	}
	gate("GET /connections", func(w http.ResponseWriter, r *http.Request, csrf string) {
		view, err := workflow.Dashboard(r.Context())
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
		templ.Handler(SectionShell("Add IBKR connection", csrf, "Connections", ConnectionSetup(csrf))).
			ServeHTTP(w, r)
	})

	for _, route := range []string{"/connections/reference", "/connections/recovery", "/system/maintenance", "/connections/{id}"} {
		gate("GET "+route, func(w http.ResponseWriter, r *http.Request, csrf string) {
			view, err := workflow.Dashboard(r.Context())
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
			case "/system/maintenance":
				title = "Maintenance"
				section = "System"
				content = MaintenancePage(csrf, view)
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
		token := credential.NewSecret([]byte(r.PostForm.Get("token")))
		defer token.Clear()

		r.PostForm.Del("token")

		connectionID, err := workflow.CreateConnection(
			r.Context(),
			r.PostForm.Get("name"),
			r.PostForm.Get("query_id"),
			token,
		)
		if err != nil {
			failure(w, r, csrf, err)
			return
		}

		//nolint:gosec // connectionURL fixes the local prefix and escapes the ID as one path segment.
		http.Redirect(w, r, connectionURL(connectionID, "status"), http.StatusSeeOther)
	})
	gate("POST /connections/{id}/settings", func(w http.ResponseWriter, r *http.Request, csrf string) {
		if err := workflow.UpdateConnection(
			r.Context(),
			r.PathValue("id"),
			r.PostForm.Get("name"),
			r.PostForm.Get("query_id"),
		); err != nil {
			failure(w, r, csrf, err)
			return
		}

		destination := connectionURL(r.PathValue("id"), "access")
		if view, e := workflow.Dashboard(r.Context()); e == nil && view.Storage.Paused {
			destination = "/connections/recovery"
		}
		//nolint:gosec // Destination is either a fixed recovery route or connectionURL's local prefix and escaped ID.
		http.Redirect(w, r, destination, http.StatusSeeOther)
	})

	gate("POST /connections/{id}/token", func(w http.ResponseWriter, r *http.Request, csrf string) {
		token := credential.NewSecret([]byte(r.PostForm.Get("token")))
		defer token.Clear()

		r.PostForm.Del("token")

		if err := workflow.ReplaceToken(r.Context(), r.PathValue("id"), token); err != nil {
			failure(w, r, csrf, err)
			return
		}

		destination := connectionURL(r.PathValue("id"), "access")
		if view, e := workflow.Dashboard(r.Context()); e == nil && view.Storage.Paused {
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
		view, err := workflow.Dashboard(r.Context())
		if err != nil {
			servePageError(
				w,
				r,
				csrf,
				http.StatusServiceUnavailable,
				"Temporarily unavailable",
				"accounts unavailable. Retry or inspect daemon status.",
			)

			return
		}

		for _, connection := range view.Connections {
			if connection.ID != r.PathValue("id") {
				continue
			}

			for _, candidate := range connection.Candidates {
				if candidate.Artifact == r.URL.Query().Get("artifact") &&
					candidate.Candidate.Reference().Value() == r.URL.Query().Get("account") {
					templ.Handler(SectionShell("Confirm account", csrf, "Connections", ConfirmAccount(csrf, connection.ID, candidate))).
						ServeHTTP(w, r)

					return
				}
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
		if view, e := workflow.Dashboard(r.Context()); e == nil && view.Storage.Paused {
			destination = "/connections/recovery"
		}

		http.Redirect(w, r, destination, http.StatusSeeOther)
	})
	gate("GET /activity", func(w http.ResponseWriter, r *http.Request, csrf string) {
		view, err := workflow.Dashboard(r.Context())
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

		templ.Handler(SectionShell("Confirmation report", csrf, "Activity", RetainedEvidence(evidence, metadata))).
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
	gate("POST /recovery/rebuild", func(w http.ResponseWriter, r *http.Request, csrf string) {
		digest := sha256.Sum256([]byte(csrf))
		actor := "admin/" + auth.mode + "/" + hex.EncodeToString(digest[:12])
		if _, err := workflow.StartRebuild(r.Context(), r.PostForm.Get("submission"), actor); err != nil {
			failure(w, r, csrf, err)
			return
		}

		http.Redirect(w, r, "/system/maintenance", http.StatusSeeOther)
	})
	gate("POST /recovery/{id}/retry", func(w http.ResponseWriter, r *http.Request, csrf string) {
		if err := workflow.RetryRecovery(r.Context(), r.PathValue("id")); err != nil {
			failure(w, r, csrf, err)
			return
		}

		http.Redirect(w, r, "/system/maintenance", http.StatusSeeOther)
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

			templ.Handler(SectionShell("Correct accepted data", csrf, "Activity", CorrectionPage(csrf, form))).
				ServeHTTP(w, r)
		})
		gate("POST "+path+"/check", func(w http.ResponseWriter, r *http.Request, _ string) {
			w.Header().Set("Content-Type", "application/json")

			decision, err := postedCorrection(r, kind)
			if err == nil {
				err = workflow.CheckCorrection(r.Context(), decision)
			}

			valid, stale, message := err == nil, errors.Is(
				err,
				application.ErrStale,
			), "Ready to save. Saving checks the current record again."
			if err != nil {
				message = "This value is not valid for the selected field or its financial terms."
				if form, e := workflow.CorrectionForm(r.Context(), kind, r.PathValue("id")); e == nil {
					message = fieldLabel(
						r.PostForm.Get("scope"),
					) + ": " + correctionSpec(
						form,
						r.PostForm.Get("scope"),
					).Hint
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
			// Attribution comes from the authenticated session, never posted form data.
			digest := sha256.Sum256([]byte(csrf))

			actor := "admin/" + auth.mode + "/" + hex.EncodeToString(digest[:12])
			if err == nil {
				err = workflow.SaveDecision(r.Context(), decision, actor)
			}

			if err != nil {
				status := http.StatusBadRequest
				message := "The correction is invalid for this field or link. Check its value, currency and target, then reload the correction form."
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
