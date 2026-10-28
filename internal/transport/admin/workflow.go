// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package admin

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
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
				http.Redirect(w, r, "/login", http.StatusSeeOther)

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

		w.WriteHeader(http.StatusBadRequest)
		_ = SectionShell("Unable to save", csrf, "Connections", WorkflowError(message)).Render(r.Context(), w)
	}
	gate("GET /connections", func(w http.ResponseWriter, r *http.Request, csrf string) {
		view, err := workflow.Dashboard(r.Context())
		if err != nil {
			http.Error(w, "workflow unavailable", http.StatusServiceUnavailable)
			return
		}

		templ.Handler(SectionShell("Connections", csrf, "Connections", ConnectionsPage(csrf, view))).
			ServeHTTP(w, r)
	})
	gate("GET /workflow/progress", func(w http.ResponseWriter, r *http.Request, csrf string) {
		view, err := workflow.Dashboard(r.Context())
		if err != nil {
			http.Error(w, "progress unavailable", http.StatusServiceUnavailable)
			return
		}

		templ.Handler(WorkflowProgress(view)).ServeHTTP(w, r)
	})
	gate("POST /connections", func(w http.ResponseWriter, r *http.Request, csrf string) {
		token := credential.NewSecret([]byte(r.PostForm.Get("token")))
		defer token.Clear()

		r.PostForm.Del("token")

		_, err := workflow.CreateConnection(
			r.Context(),
			r.PostForm.Get("name"),
			r.PostForm.Get("query_id"),
			token,
		)
		if err != nil {
			failure(w, r, csrf, err)
			return
		}

		http.Redirect(w, r, "/connections", http.StatusSeeOther)
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

		http.Redirect(w, r, "/connections", http.StatusSeeOther)
	})

	gate("POST /connections/{id}/token", func(w http.ResponseWriter, r *http.Request, csrf string) {
		token := credential.NewSecret([]byte(r.PostForm.Get("token")))
		defer token.Clear()

		r.PostForm.Del("token")

		if err := workflow.ReplaceToken(r.Context(), r.PathValue("id"), token); err != nil {
			failure(w, r, csrf, err)
			return
		}

		http.Redirect(w, r, "/connections", http.StatusSeeOther)
	})
	gate("POST /connections/{id}/retry", func(w http.ResponseWriter, r *http.Request, csrf string) {
		if err := workflow.Retry(r.Context(), r.PathValue("id")); err != nil {
			failure(w, r, csrf, err)
			return
		}

		http.Redirect(w, r, "/connections", http.StatusSeeOther)
	})
	gate("GET /connections/{id}/confirm", func(w http.ResponseWriter, r *http.Request, csrf string) {
		view, err := workflow.Dashboard(r.Context())
		if err != nil {
			http.Error(w, "accounts unavailable", http.StatusServiceUnavailable)
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

		http.NotFound(w, r)
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

		http.Redirect(w, r, "/connections", http.StatusSeeOther)
	})
	gate("GET /activity", func(w http.ResponseWriter, r *http.Request, csrf string) {
		view, err := workflow.Dashboard(r.Context())
		if err != nil {
			http.Error(w, "activity unavailable", http.StatusServiceUnavailable)
			return
		}

		templ.Handler(SectionShell("Activity", csrf, "Activity", ActivityPage(view))).ServeHTTP(w, r)
	})
	gate("GET /activity/rows", func(w http.ResponseWriter, r *http.Request, _ string) {
		view, err := workflow.Dashboard(r.Context())
		if err != nil {
			http.Error(w, "activity unavailable", http.StatusServiceUnavailable)
			return
		}

		templ.Handler(ActivityRows(view)).ServeHTTP(w, r)
	})
	gate("GET /executions/{id}", func(w http.ResponseWriter, r *http.Request, csrf string) {
		detail, err := workflow.ExecutionDetail(r.Context(), r.PathValue("id"))
		if err != nil {
			http.NotFound(w, r)
			return
		}

		templ.Handler(SectionShell("Execution", csrf, "Activity", ExecutionPage(detail))).ServeHTTP(w, r)
	})

	gate("GET /activity/{id}", func(w http.ResponseWriter, r *http.Request, csrf string) {
		if d, err := workflow.ExecutionDetail(r.Context(), r.PathValue("id")); err == nil {
			templ.Handler(SectionShell("Activity", csrf, "Activity", ExecutionPage(d))).ServeHTTP(w, r)
			return
		}

		if d, err := workflow.ConversionDetail(r.Context(), r.PathValue("id")); err == nil {
			templ.Handler(SectionShell("Currency conversion", csrf, "Activity", ConversionPage(d))).ServeHTTP(w, r)
			return
		}

		d, err := workflow.LifecycleDetail(r.Context(), r.PathValue("id"))
		if err != nil {
			http.NotFound(w, r)
			return
		}

		templ.Handler(SectionShell("Option outcome", csrf, "Activity", LifecyclePage(d))).ServeHTTP(w, r)
	})
	gate("GET /instruments/{id}", func(w http.ResponseWriter, r *http.Request, csrf string) {
		detail, err := workflow.InstrumentDetail(r.Context(), r.PathValue("id"))
		if err != nil {
			http.NotFound(w, r)
			return
		}

		templ.Handler(SectionShell("Instrument", csrf, "Activity", InstrumentPage(detail))).ServeHTTP(w, r)
	})
	gate("GET /evidence/{id}", func(w http.ResponseWriter, r *http.Request, csrf string) {
		evidence, err := workflow.Evidence(r.Context(), r.PathValue("id"))
		if err != nil {
			http.NotFound(w, r)
			return
		}

		templ.Handler(SectionShell("Original evidence", csrf, "Activity", EvidencePage(evidence.ID, evidence.Artifact.Hash(), string(evidence.Artifact.Content())))).
			ServeHTTP(w, r)
	})
	gate("GET /reference-evidence/{id}", func(w http.ResponseWriter, r *http.Request, csrf string) {
		evidence, err := workflow.ReferenceEvidence(r.Context(), r.PathValue("id"))
		if err != nil {
			http.NotFound(w, r)
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

		http.Redirect(w, r, "/connections", http.StatusSeeOther)
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

		http.Redirect(w, r, "/connections", http.StatusSeeOther)
	})
	gate("POST /recovery/{id}/retry", func(w http.ResponseWriter, r *http.Request, csrf string) {
		if err := workflow.RetryRecovery(r.Context(), r.PathValue("id")); err != nil {
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
				http.NotFound(w, r)
				return
			}

			templ.Handler(SectionShell("Correct accepted data", csrf, "Activity", CorrectionPage(csrf, form))).
				ServeHTTP(w, r)
		})
		gate("POST "+path, func(w http.ResponseWriter, r *http.Request, csrf string) {
			revision, err := strconv.ParseInt(r.PostForm.Get("revision"), 10, 64)
			if err != nil {
				http.Error(w, "invalid revision", http.StatusBadRequest)
				return
			}

			decision, err := application.NewDecision(
				application.DecisionInput{
					TargetKind:       kind,
					TargetID:         r.PathValue("id"),
					Scope:            r.PostForm.Get("scope"),
					Text:             r.PostForm.Get("value"),
					Currency:         r.PostForm.Get("currency"),
					Action:           r.PostForm.Get("action"),
					Submission:       r.PostForm.Get("submission"),
					Rationale:        r.PostForm.Get("rationale"),
					ExpectedRevision: revision,
				},
			)
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
					message = err.Error()
				}

				w.WriteHeader(status)
				_ = SectionShell(
					"Correction not saved",
					csrf,
					"Activity",
					CorrectionError(message, r.URL.Path),
				).Render(r.Context(), w)

				return
			}

			destination := "/activity/" + url.PathEscape(r.PathValue("id")) + "/correct"
			if kind == "instrument" {
				destination = "/instruments/" + url.PathEscape(r.PathValue("id")) + "/correct"
			}

			//nolint:gosec // Fixed local route plus one escaped entity ID; cannot redirect to another origin.
			http.Redirect(w, r, destination, http.StatusSeeOther)
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
