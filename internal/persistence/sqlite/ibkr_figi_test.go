// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.trippwill.dev/wake/internal/reference"
	"go.trippwill.dev/wake/source"
)

func TestIBKRCompositeFIGIReachesTypedLookupAndRetainsListingScope(t *testing.T) {
	s := coverageStore(t)
	coverageReport(
		t,
		s,
		"source",
		strings.Replace(
			equityRow("U1", "fill", "100"),
			`assetCategory="STK"`,
			`assetCategory="STK" figi="synthetic-composite"`,
			1,
		),
	)

	view, err := s.Dashboard(t.Context())
	if err != nil || len(view.Instruments) != 1 {
		t.Fatal(view, err)
	}

	id := view.Instruments[0].ID()

	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var jobs []map[string]any
		if e := json.NewDecoder(r.Body).Decode(&jobs); e != nil {
			t.Error(e)
		}

		if len(jobs) != 1 || len(jobs[0]) != 2 || jobs[0]["idType"] != "COMPOSITE_ID_BB_GLOBAL" ||
			jobs[0]["idValue"] != "synthetic-composite" {
			t.Errorf("wrong identifier-only typed lookup: %+v", jobs)
		}

		_, _ = fmt.Fprint(
			w,
			`[{"data":[{"figi":"synthetic-composite","compositeFIGI":"synthetic-composite","shareClassFIGI":"synthetic-share","marketSector":"Equity","securityType":"Common Stock","name":"Synthetic issuer"},{"figi":"synthetic-venue","compositeFIGI":"synthetic-composite","shareClassFIGI":"synthetic-share","marketSector":"Equity","securityType":"Common Stock","name":"Synthetic issuer"}]}]`,
		)
	}))
	defer upstream.Close()

	service, err := reference.New(s, nil, upstream.Client(), upstream.URL, time.Now, nil)
	if err != nil {
		t.Fatal(err)
	}

	if _, err = service.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}

	detail, err := s.InstrumentDetail(t.Context(), id)
	if err != nil || detail.Instrument.FIGI() != "synthetic-share" || len(detail.Listings) != 1 ||
		detail.Listings[0].FIGI() != "" {
		t.Fatal("identity and listing levels were conflated", detail, err)
	}

	for _, identifier := range detail.Instrument.Reported().Identifiers() {
		if identifier.Field() == "figi" && identifier.Kind() != source.CompositeFIGI {
			t.Fatal("provider classification lost", identifier)
		}
	}
}
