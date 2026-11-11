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

	"github.com/getkin/kin-openapi/openapi3"
	"go.trippwill.dev/wake/internal/application"
)

type positionReads struct {
	reads
	view application.PositionsView
}

func TestGeneratedPositionsModelPreservesReportedTimestampText(t *testing.T) {
	for _, at := range []string{"2026-11-04T12:00:00.1200Z", "2026-11-04T12:00:00.000000000+01:00"} {
		body := `{"warehouse_id":"warehouse","account_id":"account","version":"v","status":"available","as_of":{"date":"2026-11-04","at":"` + at + `"},"reason":null,"positions":[]}`

		var model PositionsView
		if err := json.Unmarshal([]byte(body), &model); err != nil {
			t.Fatal(err)
		}

		roundtrip, err := json.Marshal(model)
		if err != nil || !strings.Contains(string(roundtrip), `"at":"`+at+`"`) {
			t.Fatalf("lost reported timestamp precision: %s: %v", roundtrip, err)
		}
	}
}

func TestGeneratedUnavailablePositionsAlwaysReplaceRowsWithNull(t *testing.T) {
	doc, err := openapi3.NewLoader().LoadFromFile("../../../api/openapi/v1.yaml")
	if err != nil {
		t.Fatal(err)
	}

	for _, initial := range []string{"", `{"warehouse_id":"w","account_id":"a","version":"v","status":"available","as_of":{"date":"2026-11-04","at":null,"basis":"broker_effective"},"reason":null,"positions":[{"instrument_id":"old"}]}`} {
		model := PositionsView{WarehouseId: "w", AccountId: "a", Version: "v"}
		if initial != "" {
			if err := json.Unmarshal([]byte(initial), &model); err != nil {
				t.Fatal(err)
			}
		}

		model.Status = "unavailable"
		model.Positions = nil
		model.Reason = new(PositionsViewReason("incomplete"))

		b, err := json.Marshal(model)
		if err != nil {
			t.Fatal(err)
		}

		var body map[string]any
		if err = json.Unmarshal(b, &body); err != nil {
			t.Fatal(err)
		}

		if rows, present := body["positions"]; !present || rows != nil {
			t.Fatalf("unavailable view retained or omitted rows: %s: %v", b, err)
		}

		if err = doc.Components.Schemas["PositionsView"].Value.VisitJSON(body); err != nil {
			t.Fatal(err)
		}

		body["status"], body["reason"] = "available", nil
		if err = doc.Components.Schemas["PositionsView"].Value.VisitJSON(body); err == nil {
			t.Fatal("available view accepted null positions")
		}
	}
}

func (s positionReads) Positions(
	_ context.Context,
	_ string,
	c application.SnapshotCondition,
) (application.PositionsRead, error) {
	return application.PositionsRead{View: s.view, Unchanged: c.Matches(s.view.Version)}, s.err
}

func TestPositionRouteConditionalReadsSchemaAndUnavailableSets(t *testing.T) {
	doc, err := openapi3.NewLoader().LoadFromFile("../../../api/openapi/v1.yaml")
	if err != nil {
		t.Fatal(err)
	}

	for _, status := range []string{"available", "unavailable"} {
		v := application.PositionsView{
			WarehouseID: "warehouse",
			AccountID:   "account",
			Version:     "positions-version",
			Status:      status,
		}
		if status == "available" {
			v.AsOf = &application.PositionTime{Date: "2026-11-04", Basis: "broker_effective"}
			v.Positions = []application.PublishedPosition{}
		} else {
			v.Reason = new("not_reported")
		}

		handler := NewHandler(positionReads{view: v}, nil, nil)
		for _, etag := range []string{"", `W/"positions-version"`, `"other", "positions-version"`, `*`} {
			req := httptest.NewRequest(http.MethodGet, "/v1/accounts/account/positions", nil)
			req.Header.Set("If-None-Match", etag)

			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)

			if w.Header().Get("ETag") != `"positions-version"` {
				t.Fatal(w.Header())
			}

			if etag != "" {
				if w.Code != 304 || w.Body.Len() != 0 {
					t.Fatal(w.Code, w.Body.String())
				}

				continue
			}

			if w.Code != 200 {
				t.Fatal(w.Code, w.Body.String())
			}

			var body any
			if err = json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}

			if err = doc.Components.Schemas["PositionsView"].Value.VisitJSON(body); err != nil {
				t.Fatal(err)
			}
		}
	}

	for _, path := range []string{"/v1/accounts/account/positions?revision=1", "/v1/accounts/account/positions?extra=1", "/v1/accounts/account/positions?x=1&x=2"} {
		w := httptest.NewRecorder()
		NewHandler(reads{}, nil, nil).ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))

		if w.Code != 400 {
			t.Fatal(path, w.Code)
		}
	}

	w := httptest.NewRecorder()
	NewHandler(
		reads{err: application.ErrNotFound},
		nil,
		nil,
	).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/accounts/missing/positions", nil))

	if w.Code != 404 {
		t.Fatal(w.Code)
	}
}
