// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package socket

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.trippwill.dev/wake/internal/application"
)

type reads struct {
	application.Publishing
	err  error
	item application.CanonicalResource
}

func (s reads) PublishCheckpoint(context.Context) (string, error) { return "opaque", s.err }
func (s reads) Resources(context.Context, string, string, int) (application.ResourcePage, error) {
	return application.ResourcePage{Items: []application.CanonicalResource{s.item}}, s.err
}

func (s reads) Resource(context.Context, string, string) (application.CanonicalResource, error) {
	return s.item, s.err
}

func (s reads) Explanation(context.Context, string, string) (application.Explanation, error) {
	return application.Explanation{
		ID:          s.item.ID,
		Revision:    s.item.Revision,
		Decisions:   []application.PublishedDecision{},
		Corrections: []application.PublishedCorrection{},
	}, s.err
}

func (s reads) Changes(context.Context, string, string, int) (application.ChangePage, error) {
	return application.ChangePage{
		Items:   []application.Change{{Cursor: "next", Resource: s.item}},
		Cursor:  "next",
		Through: "next",
	}, s.err
}

func TestFinancialContractRoutesPrecisionUnknownsAndErrors(t *testing.T) {
	item := application.CanonicalResource{
		ID:       "financial-id",
		Kind:     "activity",
		Revision: 42,
		Activity: &application.PublishedActivity{
			Family:        "conversion",
			AccountID:     "account",
			State:         "accepted",
			Date:          "2026-10-20",
			Sent:          &application.PublishedAmount{Currency: "EUR"},
			Received:      &application.PublishedAmount{Currency: "USD", Amount: new("0.0000")},
			Rate:          &application.PublishedMoney{Amount: "1.23000000000000000100", Currency: "USD"},
			Charges:       []application.PublishedCharge{},
			Relationships: []application.PublishedRelationship{},
		},
	}
	for _, path := range []string{"/v1/checkpoint", "/v1/activity?limit=1", "/v1/activity/financial-id", "/v1/activity/financial-id/explanation", "/v1/changes?cursor=opaque"} {
		w := httptest.NewRecorder()
		NewHandler(reads{item: item}, nil).ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))

		if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/json" {
			t.Fatal(path, w.Code, w.Body.String())
		}

		if strings.Contains(path, "explanation") {
			if strings.Contains(w.Body.String(), "1.2300") {
				t.Fatal("explanation includes resource body")
			}

			continue
		}

		if strings.Contains(path, "activity/") {
			var result Resource
			if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}

			if result.Activity.Sent.Amount != nil || *result.Activity.Received.Amount != "0.0000" ||
				result.Activity.Rate.Amount != "1.23000000000000000100" ||
				result.Activity.At != nil {
				t.Fatal("unknown/zero/precision lost", w.Body.String())
			}
		}
	}

	for _, test := range []struct {
		path, method string
		status       int
	}{
		{"/v1/accounts", "POST", 405}, {"/v1/accounts", "HEAD", 405}, {"/v1/accounts?limit=0", "GET", 400}, {"/v1/accounts?limit=501", "GET", 400}, {"/v1/accounts?limit=1&limit=2", "GET", 400}, {"/v1/accounts?secret=private", "GET", 400}, {"/v1/accounts?cursor=", "GET", 400}, {"/v1/accounts?limit=%XX", "GET", 400}, {"/v1/accounts/", "GET", 404}, {"/v1/evidence/private", "GET", 404}, {"/v1/activity/id/explanation/extra", "GET", 404},
	} {
		w := httptest.NewRecorder()
		NewHandler(reads{item: item}, nil).ServeHTTP(w, httptest.NewRequest(test.method, test.path, nil))

		if w.Code != test.status {
			t.Fatal(test, w.Code)
		}
	}

	for _, test := range []struct {
		err    error
		code   ErrorCode
		status int
	}{
		{application.ErrCursor, MalformedCursor, 400}, {application.ErrGeneration, CursorInvalidated, 409}, {application.ErrRebuilding, RebuildInProgress, 503}, {application.ErrNotFound, NotFound, 404}, {application.ErrInvalid, InvalidRequest, 400},
	} {
		w := httptest.NewRecorder()
		NewHandler(
			reads{err: test.err},
			nil,
		).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/checkpoint", nil))

		var response Error
		if json.Unmarshal(w.Body.Bytes(), &response) != nil || response.Code != test.code ||
			w.Code != test.status {
			t.Fatal(test, w.Code, w.Body.String())
		}
	}
}
