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

// FinancialReader is the socket adapter's finite accepted-state read dependency.
// Every result is materialized before transport I/O begins.
type FinancialReader interface {
	Snapshot(context.Context, application.SnapshotCondition) (application.SnapshotRead, error)
	Resource(context.Context, string, string) (application.CanonicalResource, error)
	Explanation(context.Context, string, string, int64) (application.Explanation, error)
}

type handler struct {
	reads  FinancialReader
	logger *slog.Logger
}

func NewHandler(reads FinancialReader, logger *slog.Logger) http.Handler {
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
	explanation := false
	switch endpoint {
	case "snapshot":
		if len(parts) != 2 {
			h.failure(w, NotFound, http.StatusNotFound)
			return
		}
	case "accounts", "instruments", "listings", "activity":
		if len(parts) != 3 && len(parts) != 4 || len(parts) > 2 && (parts[2] == "" || len(parts[2]) > 256) ||
			len(parts) == 4 && parts[3] != "explanation" {
			h.failure(w, NotFound, http.StatusNotFound)
			return
		}

		explanation = len(parts) == 4
	default:
		h.failure(w, NotFound, http.StatusNotFound)
		return
	}

	for key, values := range query {
		if !explanation || key != "revision" || len(values) != 1 || values[0] == "" || len(values[0]) > 32 {
			h.failure(w, InvalidRequest, http.StatusBadRequest)
			return
		}
	}

	var response any
	switch {
	case endpoint == "snapshot":
		conditions, e := snapshotConditions(r.Header.Values("If-None-Match"))
		if e != nil {
			h.failure(w, InvalidRequest, http.StatusBadRequest)
			return
		}

		var read application.SnapshotRead

		read, err = h.reads.Snapshot(r.Context(), conditions)
		if err == nil {
			w.Header().Set("ETag", `"`+read.Snapshot.Version+`"`)

			if read.Unchanged {
				w.WriteHeader(http.StatusNotModified)
				return
			}

			response = read.Snapshot
		}
	case explanation:
		revision, e := strconv.ParseInt(query.Get("revision"), 10, 64)
		if e != nil || revision < 1 {
			h.failure(w, InvalidRequest, http.StatusBadRequest)
			return
		}

		response, err = h.reads.Explanation(r.Context(), endpoint, parts[2], revision)
	default:
		response, err = h.reads.Resource(r.Context(), endpoint, parts[2])
	}

	if err != nil {
		switch {
		case errors.Is(err, application.ErrInvalid):
			h.failure(w, InvalidRequest, http.StatusBadRequest)
		case errors.Is(err, application.ErrStale):
			h.failure(w, StaleRevision, http.StatusConflict)
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

func (h *handler) failure(w http.ResponseWriter, code ErrorCode, status int) {
	messages := map[ErrorCode]string{
		InvalidRequest:   "Invalid or unsupported request parameters.",
		StaleRevision:    "Resource changed; refresh the snapshot before requesting its explanation.",
		NotFound:         "Financial resource not found.",
		MethodNotAllowed: "Only GET is supported.",
		InternalError:    "Financial read failed.",
	}

	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(Error{Code: code, Message: messages[code]})
}

func snapshotConditions(headers []string) (application.SnapshotCondition, error) {
	value := strings.Trim(strings.Join(headers, ","), " \t")
	if len(value) > 8192 || strings.ContainsAny(value, "\r\n") {
		return application.SnapshotCondition{}, application.ErrInvalid
	}

	if value == "" {
		return application.SnapshotCondition{}, nil
	}

	if value == "*" {
		return application.AnySnapshotVersion(), nil
	}

	var versions []string
	for {
		value = strings.TrimPrefix(value, "W/")
		if len(value) < 2 || value[0] != '"' {
			return application.SnapshotCondition{}, application.ErrInvalid
		}

		end := strings.IndexByte(value[1:], '"')
		if end < 0 {
			return application.SnapshotCondition{}, application.ErrInvalid
		}

		end++
		for _, c := range []byte(value[1:end]) {
			if c < 0x21 || c == 0x7f {
				return application.SnapshotCondition{}, application.ErrInvalid
			}
		}

		versions = append(versions, value[1:end])
		value = strings.Trim(value[end+1:], " \t")
		if value == "" {
			return application.MatchingSnapshotVersions(versions...), nil
		}

		if value[0] != ',' {
			return application.SnapshotCondition{}, application.ErrInvalid
		}

		value = strings.Trim(value[1:], " \t")
		if value == "" {
			return application.SnapshotCondition{}, application.ErrInvalid
		}
	}
}
