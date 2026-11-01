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
	err  error
	item application.CanonicalResource
}

func (s reads) Resource(context.Context, string, string) (application.CanonicalResource, error) {
	return s.item, s.err
}

func (s reads) Explanation(context.Context, string, string, int64) (application.Explanation, error) {
	return application.Explanation{
		ID:          s.item.ID,
		Revision:    s.item.Revision,
		Decisions:   []application.PublishedDecision{},
		Corrections: []application.PublishedCorrection{},
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
	for _, path := range []string{"/v1/snapshot", "/v1/activity/financial-id", "/v1/activity/financial-id/explanation?revision=42"} {
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

			activity, err := result.AsActivityResource()
			if err != nil {
				t.Fatal(err)
			}

			if activity.Activity.Sent.Amount != nil || *activity.Activity.Received.Amount != "0.0000" ||
				activity.Activity.Rate.Amount != "1.23000000000000000100" ||
				activity.Activity.At != nil {
				t.Fatal("unknown/zero/precision lost", w.Body.String())
			}
		}
	}

	for _, test := range []struct {
		path, method string
		status       int
	}{
		{"/v1/snapshot", "POST", 405},
		{"/v1/snapshot", "HEAD", 405},
		{"/v1/snapshot?extra=1", "GET", 400},
		{"/v1/snapshot?x=%XX", "GET", 400},
		{"/v1/activity/id/explanation", "GET", 400},
		{"/v1/activity/id/explanation?revision=0", "GET", 400},
		{"/v1/activity/id/explanation?revision=1&revision=2", "GET", 400},
		{"/v1/activity/id?revision=1", "GET", 400},
		{"/v1/accounts/", "GET", 404},
		{"/v1/evidence/private", "GET", 404},
		{"/v1/activity/id/explanation/extra", "GET", 404},
		{"/v1/checkpoint", "GET", 404},
		{"/v1/changes?cursor=old", "GET", 404},
		{"/v1/accounts", "GET", 404},
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
		{application.ErrStale, StaleRevision, 409}, {application.ErrNotFound, NotFound, 404}, {application.ErrInvalid, InvalidRequest, 400},
	} {
		w := httptest.NewRecorder()
		NewHandler(
			reads{err: test.err},
			nil,
		).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/snapshot", nil))

		var response Error
		if json.Unmarshal(w.Body.Bytes(), &response) != nil || response.Code != test.code ||
			w.Code != test.status {
			t.Fatal(test, w.Code, w.Body.String())
		}
	}
}

func (s reads) Snapshot(
	_ context.Context,
	condition application.SnapshotCondition,
) (application.SnapshotRead, error) {
	unchanged := condition.Matches("opaque-version")

	return application.SnapshotRead{
		Snapshot: application.FinancialSnapshot{
			WarehouseID: "warehouse",
			Version:     "opaque-version",
			Resources:   []application.CanonicalResource{s.item},
		},
		Unchanged: unchanged,
	}, s.err
}

func TestSnapshotConditionalHTTP(t *testing.T) {
	h := NewHandler(
		reads{
			item: application.CanonicalResource{
				ID:       "one",
				Kind:     "accounts",
				Revision: 1,
				Account:  &application.PublishedAccount{Name: "Synthetic"},
			},
		},
		nil,
	)
	for _, condition := range []string{"", `"opaque-version"`, `W/"opaque-version"`, `"other", "opaque-version"`, "*"} {
		r := httptest.NewRequest(http.MethodGet, "/v1/snapshot", nil)
		r.Header.Set("If-None-Match", condition)

		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)

		want := http.StatusNotModified
		if condition == "" {
			want = http.StatusOK
		}

		if w.Code != want || w.Header().Get("ETag") != `"opaque-version"` {
			t.Fatal(condition, w.Code, w.Header(), w.Body.String())
		}

		if want == http.StatusNotModified && w.Body.Len() != 0 {
			t.Fatal("conditional response had a body")
		}
	}
}

func TestSnapshotConditionalSyntax(t *testing.T) {
	for _, test := range []struct {
		condition string
		status    int
	}{
		{`"other,tag"`, 200},
		{`"*"`, 200},
		{`W/"*"`, 200},
		{`"*", W/"opaque-version"`, 304},
		{`W/"other,tag", W/"opaque-version"`, 304},
		{`""`, 200},
		{`"opaque-version",`, 400},
		{`*, "other"`, 400},
		{`unquoted`, 400},
		{`"space tag"`, 400},
	} {
		h := NewHandler(reads{}, nil)
		request := httptest.NewRequest(http.MethodGet, "/v1/snapshot", nil)
		request.Header.Set("If-None-Match", test.condition)

		response := httptest.NewRecorder()
		h.ServeHTTP(response, request)

		if response.Code != test.status {
			t.Fatal(test, response.Code, response.Body.String())
		}
	}
}

func TestRetiredResourceRedirectStaysWithinResourcePath(t *testing.T) {
	for _, endpoint := range []string{"instruments", "listings"} {
		w := httptest.NewRecorder()
		NewHandler(reads{item: application.CanonicalResource{ID: "current /?id", Kind: endpoint}}, nil).
			ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/"+endpoint+"/retired", nil))

		if w.Code != http.StatusPermanentRedirect ||
			w.Header().Get("Location") != "/v1/"+endpoint+"/current%20%2F%3Fid" ||
			w.Body.Len() != 0 {
			t.Fatal(
				"retired ID did not redirect to its escaped canonical resource",
				endpoint,
				w.Code,
				w.Header(),
				w.Body.String(),
			)
		}
	}
}
