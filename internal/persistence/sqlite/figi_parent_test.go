// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.trippwill.dev/wake/internal/reference"
)

func TestFIGIParentLookupUsesDurableBudgetEvidenceAndCache(t *testing.T) {
	s, _ := testStore(t)
	insertReferenceInstrument(t, s, "root", "synthetic-isin")

	now := time.Now().UTC()
	primary, parent := 0, 0

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var jobs []reference.Job
		if err := json.NewDecoder(r.Body).Decode(&jobs); err != nil || len(jobs) != 1 {
			t.Error(jobs, err)
			w.WriteHeader(http.StatusBadRequest)

			return
		}

		switch jobs[0] {
		case (reference.Job{IDType: "ID_ISIN", IDValue: "synthetic-isin"}):
			primary++
			_, _ = w.Write(
				[]byte(`[{"data":[{"figi":"orphan-venue","compositeFIGI":"composite","marketSector":"Equity"}]}]`),
			)
		case (reference.Job{IDType: "COMPOSITE_ID_BB_GLOBAL", IDValue: "composite"}):
			parent++
			if parent == 1 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}

			_, _ = w.Write(
				[]byte(
					`[{"data":[{"figi":"sibling-venue","compositeFIGI":"composite","shareClassFIGI":"share","marketSector":"Equity","securityType":"Common Stock"},{"figi":"unrelated","compositeFIGI":"unrelated","shareClassFIGI":"wrong","marketSector":"Equity"}]}]`,
				),
			)
		default:
			t.Error("unexpected/filtered job", jobs)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer server.Close()

	service, err := reference.New(s, nil, server.Client(), server.URL, func() time.Time { return now }, nil)
	if err != nil {
		t.Fatal(err)
	}

	if _, err = service.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}

	row, err := s.queries.Instrument(t.Context(), "root")
	if err != nil || row.ReferenceReason != string(reference.MissingParent) {
		t.Fatal(row, err)
	}

	attempts, err := s.queries.InstrumentReferenceAttempts(t.Context(), "root")
	if err != nil || len(attempts) != 2 {
		t.Fatal(attempts, err)
	}

	failure := false
	for _, attempt := range attempts {
		failure = failure || attempt.Status == "temporary-failure"
	}

	if !failure {
		t.Fatal("parent failure lost", attempts)
	}

	now = now.Add(2 * time.Minute)

	if _, err = service.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}

	row, err = s.queries.Instrument(t.Context(), "root")
	if err != nil || row.ReferenceFigi != "share" || row.ReferenceStatus != "resolved" ||
		row.Kind != "equity" ||
		primary != 1 ||
		parent != 2 {
		t.Fatal(row, primary, parent, err)
	}

	budget, err := s.queries.ReferenceBudget(t.Context())
	if err != nil || budget.Used != 1 {
		t.Fatal(budget, err)
	}

	form, err := s.CorrectionForm(t.Context(), "instrument", "root")
	if err != nil || len(form.Candidates) != 1 ||
		form.Candidates[0].Fields["reference_identity"].Value != "share" {
		t.Fatal("supporting candidate not attributable", form, err)
	}

	retainDecision(t, s, "instrument", "root", "reference", form.Candidates[0].Value, "", "set")
	insertReferenceInstrument(t, s, "duplicate", "synthetic-isin")

	if _, err = service.RunOnce(t.Context()); err != nil || primary != 1 || parent != 2 {
		t.Fatal("parent cache not reused", primary, parent, err)
	}

	canonical, err := canonicalID(t.Context(), s.queries, "instruments", "duplicate")
	if err != nil || canonical != "root" {
		t.Fatal(canonical, err)
	}
}

func TestMissingIdentifierWaitsForNewEvidence(t *testing.T) {
	s := coverageStore(t)
	coverageReport(t, s, "initial", syntheticOption)

	now := time.Now().UTC()

	service, err := reference.New(s, nil, nil, "", func() time.Time { return now }, nil)
	if err != nil {
		t.Fatal(err)
	}

	if changed, err := service.RunOnce(t.Context()); err != nil || !changed {
		t.Fatal(changed, err)
	}

	now = now.Add(48 * time.Hour)

	if changed, err := service.RunOnce(t.Context()); err != nil || changed {
		t.Fatal("unchanged missing identifier repeated", changed, err)
	}

	enriched := strings.Replace(
		syntheticOption,
		`underlyingSymbol="SYN"`,
		`underlyingSymbol="SYN" underlyingSecurityID="US5949181045"`,
		1,
	)
	coverageReport(t, s, "enriched", enriched)

	pending, err := s.PendingReference(t.Context(), now)
	if err != nil || len(pending) != 2 {
		t.Fatal("new identifier did not wake underlier", pending, err)
	}
}

func TestMatchingOperatorReferenceChoicesCombineAndReleaseTogether(t *testing.T) {
	s, _ := testStore(t)
	for _, id := range []string{"first", "second"} {
		insertReferenceInstrument(t, s, id, "synthetic-isin")
	}

	jobs := []reference.Job{{IDType: "ID_ISIN", IDValue: "synthetic-isin"}}

	request, err := json.Marshal(jobs)
	if err != nil {
		t.Fatal(err)
	}

	records := []reference.Record{
		{FIGI: "venue-x", ShareClass: "X", Market: "Equity"},
		{FIGI: "venue-y", ShareClass: "Y", Market: "Equity"},
	}

	response, err := json.Marshal([]struct {
		Data []reference.Record `json:"data"`
	}{{Data: records}})
	if err != nil {
		t.Fatal(err)
	}

	results, err := s.SaveReference(
		t.Context(),
		reference.Evidence{At: time.Now(), Request: request, Response: response, Status: 200},
		jobs,
		[]reference.Result{{Status: "resolved", Records: records, Next: time.Now().Add(time.Hour)}},
	)
	if err != nil {
		t.Fatal(err)
	}

	for _, id := range []string{"first", "second"} {
		if err = s.ApplyReference(t.Context(), id, results[0]); err != nil {
			t.Fatal(err)
		}

		form, e := s.CorrectionForm(t.Context(), "instrument", id)
		if e != nil || len(form.Candidates) != 2 {
			t.Fatal(form, e)
		}

		retainDecision(t, s, "instrument", id, "reference", form.Candidates[0].Value, "", "set")
	}

	canonical, err := canonicalID(t.Context(), s.queries, "instruments", "second")
	if err != nil || canonical != "first" {
		t.Fatal("matching reviewed identity did not combine", canonical, err)
	}

	retainDecision(t, s, "instrument", "first", "reference", "", "", "release")

	detail, err := s.InstrumentDetail(t.Context(), "first")
	if err != nil || detail.Instrument.FIGI() != "" || !detail.Instrument.Status().NeedsReview() ||
		detail.Instrument.ReferenceMetadata().Allocation != "" {
		t.Fatal("release invented automatic authority", detail, err)
	}
}

func TestFailedFIGIParentIsAttemptedOncePerRun(t *testing.T) {
	s, _ := testStore(t)
	insertReferenceInstrument(t, s, "first", "shared-isin")
	insertReferenceInstrument(t, s, "second", "shared-isin")

	now := time.Now().UTC()
	primary, parent := 0, 0

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var jobs []reference.Job
		if err := json.NewDecoder(r.Body).Decode(&jobs); err != nil || len(jobs) != 1 {
			t.Error(jobs, err)
			w.WriteHeader(http.StatusBadRequest)

			return
		}

		if jobs[0].IDType == "ID_ISIN" {
			primary++
			_, _ = w.Write([]byte(`[{"data":[{"figi":"venue","compositeFIGI":"parent","marketSector":"Equity"}]}]`))
		} else {
			parent++

			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer server.Close()

	service, err := reference.New(s, nil, server.Client(), server.URL, func() time.Time { return now }, nil)
	if err != nil {
		t.Fatal(err)
	}

	for run := 1; run <= 2; run++ {
		if _, err = service.RunOnce(t.Context()); err != nil || primary != 1 || parent != run {
			t.Fatalf(
				"shared parent failure repeated: run=%d primary=%d parent=%d err=%v",
				run,
				primary,
				parent,
				err,
			)
		}

		now = now.Add(2 * time.Minute)
	}
}

func TestParentLookupPreservesContradictoryKnownFIGI(t *testing.T) {
	s, _ := testStore(t)
	insertReferenceInstrument(t, s, "root", "synthetic-isin")

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var jobs []reference.Job
		if err := json.NewDecoder(r.Body).Decode(&jobs); err != nil || len(jobs) != 1 {
			t.Error(jobs, err)
			w.WriteHeader(http.StatusBadRequest)

			return
		}

		if jobs[0].IDType == "ID_ISIN" {
			_, _ = w.Write([]byte(`[{"data":[{"figi":"venue","compositeFIGI":"parent"}]}]`))
		} else {
			_, _ = w.Write(
				[]byte(
					`[{"data":[{"figi":"venue","compositeFIGI":"contradictory-parent"},{"figi":"sibling","compositeFIGI":"parent","shareClassFIGI":"share"},{"figi":"unrelated","compositeFIGI":"unrelated","shareClassFIGI":"unrelated-share"}]}]`,
				),
			)
		}
	}))
	defer server.Close()

	service, err := reference.New(s, nil, server.Client(), server.URL, time.Now, nil)
	if err != nil {
		t.Fatal(err)
	}

	if _, err = service.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}

	row, err := s.queries.Instrument(t.Context(), "root")
	if err != nil || row.ReferenceFigi != "" || row.ReferenceReason != string(reference.ConflictingParents) {
		t.Fatal("contradictory parent was filtered out", row, err)
	}
}
