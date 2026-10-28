// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

// Package socket adapts the accepted financial read model to the versioned wire contract.
package socket

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/logging"
)

type handler struct {
	reads  application.Publishing
	logger *slog.Logger
}

func NewHandler(reads application.Publishing, logger *slog.Logger) http.Handler {
	return &handler{reads: reads, logger: logging.Component(logger, "socket")}
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")

	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		h.failure(w, MethodNotAllowed, http.StatusMethodNotAllowed)

		return
	}

	if r.ContentLength != 0 || len(r.TransferEncoding) > 0 || len(r.URL.RawQuery) > 8192 {
		h.failure(w, InvalidRequest, http.StatusBadRequest)
		return
	}

	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		h.failure(w, InvalidRequest, http.StatusBadRequest)
		return
	}

	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
	if len(parts) < 2 || parts[0] != "v1" {
		h.failure(w, NotFound, http.StatusNotFound)
		return
	}

	endpoint := parts[1]
	allowed := map[string]bool{}
	switch endpoint {
	case "checkpoint":
		if len(parts) != 2 {
			h.failure(w, NotFound, http.StatusNotFound)
			return
		}
	case "changes":
		if len(parts) != 2 {
			h.failure(w, NotFound, http.StatusNotFound)
			return
		}

		allowed = map[string]bool{"cursor": true, "through": true, "limit": true}
	case "accounts", "instruments", "listings", "activity":
		if len(parts) > 4 || (len(parts) == 4 && parts[3] != "explanation") ||
			(len(parts) > 2 && (parts[2] == "" || len(parts[2]) > 256)) {
			h.failure(w, NotFound, http.StatusNotFound)
			return
		}

		if len(parts) == 2 {
			allowed = map[string]bool{"cursor": true, "limit": true}
		}
	default:
		h.failure(w, NotFound, http.StatusNotFound)
		return
	}

	for key, values := range query {
		if !allowed[key] || len(values) != 1 || values[0] == "" || len(values[0]) > 2048 {
			h.failure(w, InvalidRequest, http.StatusBadRequest)
			return
		}
	}

	limit := 100
	if value := query.Get("limit"); value != "" {
		limit, err = strconv.Atoi(value)
		if err != nil || limit < 1 || limit > 500 {
			h.failure(w, InvalidRequest, http.StatusBadRequest)
			return
		}
	}

	var response any
	switch endpoint {
	case "checkpoint":
		var cursor string

		cursor, err = h.reads.PublishCheckpoint(r.Context())
		response = Checkpoint{Cursor: cursor}
	case "changes":
		if query.Get("cursor") == "" {
			h.failure(w, MalformedCursor, http.StatusBadRequest)
			return
		}

		var page application.ChangePage

		page, err = h.reads.Changes(r.Context(), query.Get("cursor"), query.Get("through"), limit)
		if err == nil {
			response, err = wire[ChangePage](page)
		}
	default:
		switch len(parts) {
		case 2:
			var page application.ResourcePage

			page, err = h.reads.Resources(r.Context(), endpoint, query.Get("cursor"), limit)
			if err == nil {
				response, err = wire[ResourcePage](page)
			}
		case 3:
			var resource application.CanonicalResource

			resource, err = h.reads.Resource(r.Context(), endpoint, parts[2])
			if err == nil {
				response, err = wire[Resource](resource)
			}
		default:
			var explanation application.Explanation

			explanation, err = h.reads.Explanation(r.Context(), endpoint, parts[2])
			if err == nil {
				response, err = wire[Explanation](explanation)
			}
		}
	}

	if err != nil {
		switch {
		case errors.Is(err, application.ErrCursor), errors.Is(err, application.ErrInvalid):
			code := MalformedCursor
			if errors.Is(err, application.ErrInvalid) {
				code = InvalidRequest
			}

			h.failure(w, code, http.StatusBadRequest)
		case errors.Is(err, application.ErrGeneration):
			h.failure(w, CursorInvalidated, http.StatusConflict)
		case errors.Is(err, application.ErrRebuilding):
			h.failure(w, RebuildInProgress, http.StatusServiceUnavailable)
		case errors.Is(err, application.ErrNotFound):
			h.failure(w, NotFound, http.StatusNotFound)
		default:
			if !errors.Is(err, context.Canceled) {
				h.logger.ErrorContext(
					r.Context(),
					"financial read failed",
					"event",
					"socket_read_failed",
					"code",
					"read_failed",
					"route",
					endpoint,
				)
			}

			h.failure(w, InternalError, http.StatusInternalServerError)
		}

		return
	}

	if err = json.NewEncoder(w).Encode(response); err != nil && r.Context().Err() == nil {
		h.logger.DebugContext(
			r.Context(),
			"financial response interrupted",
			"event",
			"socket_write_failed",
			"code",
			"transport_failed",
		)
	}
}

// Keep generated wire models in the transport; canonical storage uses application read models.
func wire[T any](value any) (T, error) {
	var out T

	data, err := json.Marshal(value)
	if err != nil {
		return out, err
	}

	err = json.Unmarshal(data, &out)

	return out, err
}

func (h *handler) failure(w http.ResponseWriter, code ErrorCode, status int) {
	messages := map[ErrorCode]string{
		InvalidRequest:    "Invalid or unsupported request parameters.",
		MalformedCursor:   "Malformed or wrong-purpose cursor.",
		CursorInvalidated: "Cursor invalidated; start a fresh bootstrap.",
		RebuildInProgress: "Publishing paused for rebuild; retry after successful completion.",
		NotFound:          "Financial resource not found.",
		MethodNotAllowed:  "Only GET is supported.",
		InternalError:     "Financial read failed.",
	}

	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(Error{Code: code, Message: messages[code]})
}
