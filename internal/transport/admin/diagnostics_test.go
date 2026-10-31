// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package admin

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.trippwill.dev/wake/internal/application"
)

func TestAdminFailuresAreClassifiedAndPrivate(t *testing.T) {
	dir := t.TempDir()
	if err := SetUnlock(dir, []byte("synthetic-workflow-unlock")); err != nil {
		t.Fatal(err)
	}

	auth, _, err := Open(dir, "local", "http://127.0.0.1:8787")
	if err != nil {
		t.Fatal(err)
	}
	defer auth.Close()

	login := httptest.NewRecorder()
	auth.issue(login)

	cookie := login.Result().Cookies()[0]
	for _, level := range []slog.Level{slog.LevelInfo, slog.LevelDebug} {
		t.Run(level.String(), func(t *testing.T) {
			var output bytes.Buffer

			logger := slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: level}))

			handler, err := NewHandler(auth, Dependencies{Logger: logger, Workflow: new(application.Workflow)})
			if err != nil {
				t.Fatal(err)
			}

			for _, test := range []struct {
				path, origin, code string
				authenticated      bool
			}{
				{"/connections/private-path/secrets?private-query=value", "http://127.0.0.1:8787", "session_required", false},
				{"/connections/private-path/secrets", "http://private-origin.invalid", "invalid_origin", true},
				{"/connections/private-path/secrets", "http://127.0.0.1:8787", "invalid_csrf", true},
				{"/login", "http://127.0.0.1:8787", "login_rejected", false},
			} {
				output.Reset()

				r := httptest.NewRequest(
					http.MethodPost,
					"http://127.0.0.1:8787"+test.path,
					strings.NewReader("csrf=private-csrf&unlock=private-unlock&token=private-token"),
				)
				r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				r.Header.Set("Origin", test.origin)

				if test.authenticated {
					r.AddCookie(cookie)
				}

				handler.ServeHTTP(httptest.NewRecorder(), r)

				if strings.Contains(output.String(), "private-") || strings.Contains(output.String(), cookie.Value) {
					t.Fatal("request data leaked", output.String())
				}

				var record map[string]any
				if err := json.Unmarshal(output.Bytes(), &record); err != nil {
					t.Fatal(err, output.String())
				}

				if record["code"] != test.code || record["operation_id"] == "" || record["route"] == "unmatched" {
					t.Fatal(record)
				}
			}
		})
	}
}

func TestDiagnosticsPreservesStreamingAndQuietReads(t *testing.T) {
	var output bytes.Buffer

	logger := slog.New(slog.NewJSONHandler(&output, nil))
	handler := diagnostics(logger, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := http.NewResponseController(w).
			SetWriteDeadline(time.Time{}); !errors.Is(
			err,
			http.ErrNotSupported,
		) {
			t.Fatal("response controller was not unwrapped", err)
		}

		_, _ = w.Write([]byte("data: refresh\n\n"))
		w.(http.Flusher).Flush()
	}))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://wake/workflow/events", nil))

	if !response.Flushed || response.Body.String() != "data: refresh\n\n" {
		t.Fatal("stream did not flush")
	}

	if output.Len() != 0 {
		t.Fatal("routine reads logged at info")
	}
}

type failedFlushResponse struct {
	*httptest.ResponseRecorder
	failure error
}

func (w failedFlushResponse) FlushError() error { return w.failure }

func TestResponseControllerPreservesFlushFailure(t *testing.T) {
	failure := errors.New("synthetic flush failure")
	recorder := httptest.NewRecorder()

	wrapped := &responseStatus{ResponseWriter: failedFlushResponse{recorder, failure}}
	if err := http.NewResponseController(wrapped).Flush(); !errors.Is(err, failure) {
		t.Fatal("flush failure hidden", err)
	}

	if wrapped.status != http.StatusOK || recorder.Code != http.StatusOK {
		t.Fatal("stream status not recorded", wrapped.status, recorder.Code)
	}
}
