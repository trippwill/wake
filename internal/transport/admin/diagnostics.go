// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package admin

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"go.trippwill.dev/wake/internal/logging"
)

type rejectionKey struct{}

func rejection(r *http.Request, code string) {
	if value, ok := r.Context().Value(rejectionKey{}).(*string); ok {
		*value = code
	}
}

type responseStatus struct {
	http.ResponseWriter
	status int
}

func (w *responseStatus) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *responseStatus) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}

	w.ResponseWriter.WriteHeader(status)
}

func (w *responseStatus) Write(value []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}

	return w.ResponseWriter.Write(value)
}

func (w *responseStatus) Flush() {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}

	_ = http.NewResponseController(w.ResponseWriter).Flush()
}

func diagnostics(logger *slog.Logger, next http.Handler) http.Handler {
	logger = logging.Component(logger, "admin")

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		code := ""

		ctx := context.WithValue(logging.Correlate(r.Context()), rejectionKey{}, &code)
		r = r.WithContext(ctx)
		response := &responseStatus{ResponseWriter: w}
		defer func() {
			status := response.status
			if status == 0 {
				status = http.StatusOK
			}

			level := slog.LevelDebug
			if code != "" || status >= 400 {
				level = slog.LevelWarn
			}

			if status >= 500 {
				level = slog.LevelError
			}

			if ctx.Err() != nil {
				level, code = slog.LevelDebug, logging.Code(ctx.Err())
			}

			if code == "" && status >= 400 {
				code = "request_rejected"
			}

			pattern := r.Pattern
			if pattern == "" {
				pattern = "unmatched"
			}

			logger.Log(
				ctx,
				level,
				"admin request finished",
				"event",
				"admin_request",
				"operation_id",
				logging.ID(ctx),
				"route",
				pattern,
				"status",
				status,
				"code",
				code,
				"duration_ms",
				time.Since(started).Milliseconds(),
			)
		}()

		next.ServeHTTP(response, r)
	})
}
