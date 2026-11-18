// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package financialapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/application/calculation"
)

func (s reads) SubscribePublications() (<-chan struct{}, func()) {
	return make(chan struct{}), func() {}
}

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
			PublishedActivityHeader: application.PublishedActivityHeader{
				Family:        "conversion",
				AccountID:     "account",
				State:         "accepted",
				Date:          "2026-10-20",
				Relationships: []application.PublishedRelationship{},
			},
			Conversion: &application.PublishedConversion{
				Sent:     &application.PublishedAmount{Currency: "EUR"},
				Received: &application.PublishedAmount{Currency: "USD", Amount: new("0.0000")},
				Rate:     &application.PublishedMoney{Amount: "1.23000000000000000100", Currency: "USD"},
				Charges:  []application.PublishedCharge{},
			},
		},
	}
	for _, path := range []string{"/v1/snapshot", "/v1/activity/financial-id", "/v1/activity/financial-id/explanation?revision=42"} {
		w := httptest.NewRecorder()
		NewHandler(reads{item: item}, nil, nil).ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))

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

			conversion, err := activity.Activity.AsConversionActivity()
			if err != nil {
				t.Fatal(err)
			}

			if conversion.Sent.Amount != nil || *conversion.Received.Amount != "0.0000" ||
				conversion.Rate.Amount != "1.23000000000000000100" ||
				conversion.At != nil {
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
		{"/v1/snapshot?baseline_id=w", "GET", 400},
		{"/v1/snapshot?from=v", "GET", 400},
		{"/v1/snapshot?baseline_id=w&from=", "GET", 400},
		{"/v1/snapshot?baseline_id=w&from=v&from=v", "GET", 400},
		{"/v1/snapshot?baseline_id=w&from=%00", "GET", 400},
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
		NewHandler(reads{item: item}, nil, nil).ServeHTTP(w, httptest.NewRequest(test.method, test.path, nil))

		if w.Code != test.status {
			t.Fatal(test, w.Code)
		}
	}

	for _, test := range []struct {
		err    error
		code   ErrorCode
		status int
	}{
		{application.ErrStale, StaleRevision, 409}, {application.ErrNotFound, NotFound, 404}, {calculation.ErrInvalid, InvalidRequest, 400},
	} {
		w := httptest.NewRecorder()
		NewHandler(
			reads{err: test.err},
			nil,
			nil,
		).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/snapshot", nil))

		var response Error
		if json.Unmarshal(w.Body.Bytes(), &response) != nil || response.Code != test.code ||
			w.Code != test.status {
			t.Fatal(test, w.Code, w.Body.String())
		}
	}
}

func (s reads) HistoryPublication(context.Context) (application.HistoryPublication, error) {
	return application.HistoryPublication{BaselineID: "warehouse", Version: "opaque-version"}, s.err
}

func (s reads) Snapshot(
	_ context.Context,
	base application.HistoryPublication,
) (application.SnapshotRead, error) {
	if base.BaselineID == "warehouse" && base.Version == "opaque-version" {
		return application.SnapshotRead{Delta: &application.SnapshotDelta{
			Mode: "delta", BaselineID: base.BaselineID, From: base.Version, Version: base.Version,
			Upserts: []application.CanonicalResource{}, Removals: []application.ResourceKey{},
		}}, s.err
	}

	return application.SnapshotRead{Snapshot: application.FinancialSnapshot{
		Mode: "full", BaselineID: "warehouse", Version: "opaque-version",
		Resources: []application.CanonicalResource{s.item},
	}}, s.err
}

func TestSnapshotBaselineHTTP(t *testing.T) {
	h := NewHandler(reads{}, nil, nil)
	for _, test := range []struct {
		query, mode string
	}{
		{"", "full"},
		{"?baseline_id=warehouse&from=opaque-version", "delta"},
		{"?baseline_id=warehouse&from=unknown", "full"},
		{"?baseline_id=other&from=opaque-version", "full"},
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/snapshot"+test.query, nil))

		var response struct {
			Mode string `json:"mode"`
		}

		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &response) != nil ||
			response.Mode != test.mode {
			t.Fatal(test, w.Code, w.Body.String())
		}
	}
}

func TestPositionsConditionalSyntax(t *testing.T) {
	for _, test := range []struct {
		condition string
		status    int
	}{
		{`"other,tag"`, 200},
		{`"*"`, 200},
		{`W/"*"`, 200},
		{`"*", W/"positions-version"`, 304},
		{`W/"other,tag", W/"positions-version"`, 304},
		{`""`, 200},
		{`"positions-version",`, 400},
		{`*, "other"`, 400},
		{`unquoted`, 400},
		{`"space tag"`, 400},
	} {
		h := NewHandler(reads{}, nil, nil)
		request := httptest.NewRequest(http.MethodGet, "/v1/accounts/account/positions", nil)
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
		NewHandler(reads{item: application.CanonicalResource{ID: "current /?id", Kind: endpoint}}, nil, nil).
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

func (s reads) Positions(
	_ context.Context,
	id string,
	c application.SnapshotCondition,
) (application.PositionsRead, error) {
	v := application.PositionsView{
		BaselineID: "warehouse",
		AccountID:  id,
		Version:    "positions-version",
		Status:     "unavailable",
		Reason:     new("not_reported"),
	}

	return application.PositionsRead{View: v, Unchanged: c.Matches(v.Version)}, s.err
}

func (s reads) PositionsPublications(context.Context) ([]application.PositionsPublication, error) {
	return nil, s.err
}

func (reads) HistoryUpdating() bool { return false }
