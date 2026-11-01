// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/reference"
	"go.trippwill.dev/wake/source"
)

func TestConvergedBindingRetainsLaterSourceIdentifierChanges(t *testing.T) {
	for _, disputed := range []bool{false, true} {
		t.Run(map[bool]string{false: "enrichment", true: "disagreement"}[disputed], func(t *testing.T) {
			s := coverageStore(t)
			coverageReport(t, s, "first", equityRow("U1", "a", "100"))
			second := strings.NewReplacer(`conid="100"`, `conid="200"`, "US0000000001", "US0000000002").
				Replace(equityRow("U1", "b", "200"))
			coverageReport(t, s, "second", second)

			view, err := s.Dashboard(t.Context())
			if err != nil {
				t.Fatal(err)
			}

			a, b := view.Instruments[0].ID(), view.Instruments[1].ID()
			applyEquityIdentity(t, s, a)
			applyEquityIdentity(t, s, b)

			changed := strings.Replace(
				second,
				`isin="US0000000002"`,
				`isin="US0000000002" cusip="synthetic-cusip"`,
				1,
			)
			if disputed {
				changed = strings.Replace(second, "US0000000002", "US0000000001", 1)
			}

			coverageReport(t, s, "new-claim", changed)

			detail, err := s.InstrumentDetail(t.Context(), a)
			if err != nil || detail.Instrument.Status().NeedsReview() != disputed {
				t.Fatal("converged binding hid a source disagreement", detail, err)
			}

			if !disputed &&
				!slices.ContainsFunc(
					detail.Instrument.Reported().Identifiers(),
					func(id source.Identifier) bool { return id.Kind() == source.CUSIP && id.Value() == "synthetic-cusip" },
				) {
				t.Fatal("converged binding lost new identifier", detail)
			}
		})
	}
}

func TestConvergencePreservesConcreteFamily(t *testing.T) {
	for _, family := range []source.InstrumentKind{source.Equity, source.Fund} {
		t.Run(string(family), func(t *testing.T) {
			s, _ := testStore(t)
			insertReferenceInstrument(t, s, "concrete", "one")
			insertReferenceInstrument(t, s, "unknown", "two")

			for id, kind := range map[string]source.InstrumentKind{"concrete": family, "unknown": source.UnknownInstrument} {
				row, err := s.queries.Instrument(t.Context(), id)
				if err != nil {
					t.Fatal(err)
				}

				value, err := instrumentValue(row)
				if err != nil {
					t.Fatal(err)
				}

				f := value.Reported().Fields()
				f.Kind = kind

				instrument, err := source.NewInstrument(f)
				if err != nil {
					t.Fatal(err)
				}

				data, err := json.Marshal(instrumentWire(instrument))
				if err != nil {
					t.Fatal(err)
				}

				if _, err = s.db.Exec(
					`UPDATE instruments SET kind=?,reported=? WHERE id=?`,
					kind,
					data,
					id,
				); err != nil {
					t.Fatal(err)
				}
			}

			for _, id := range []string{"concrete", "unknown"} {
				if err := s.ApplyReference(
					t.Context(),
					id,
					reference.Result{
						Status:  "resolved",
						Records: []reference.Record{{FIGI: "venue", ShareClass: "share", Name: "Source label"}},
						Next:    time.Now().Add(time.Hour),
					},
				); err != nil {
					t.Fatal(err)
				}
			}

			detail, err := s.InstrumentDetail(t.Context(), "concrete")
			if err != nil || detail.Instrument.Kind() != string(family) {
				t.Fatal("unknown binding downgraded concrete family", detail, err)
			}
		})
	}
}

func TestConvergedDetailAndSnapshotIncludeAllBindings(t *testing.T) {
	for _, check := range []string{"detail", "snapshot"} {
		t.Run(check, func(t *testing.T) {
			s := coverageStore(t)
			coverageReport(t, s, "first", equityRow("U1", "a", "100"))
			coverageReport(
				t,
				s,
				"second",
				strings.NewReplacer(`conid="100"`, `conid="200"`, "US0000000001", "US0000000002").
					Replace(equityRow("U1", "b", "200")),
			)

			view, err := s.Dashboard(t.Context())
			if err != nil {
				t.Fatal(err)
			}

			a, b := view.Instruments[0].ID(), view.Instruments[1].ID()
			applyEquityIdentity(t, s, a)

			before, err := s.Resource(t.Context(), "instruments", a)
			if err != nil {
				t.Fatal(err)
			}

			applyEquityIdentity(t, s, b)

			if check == "detail" {
				canonical, e := s.InstrumentDetail(t.Context(), a)
				alias, e2 := s.InstrumentDetail(t.Context(), b)
				if e != nil || e2 != nil || len(canonical.Listings) == 0 ||
					!reflect.DeepEqual(canonical.Listings, alias.Listings) ||
					!reflect.DeepEqual(canonical.References, alias.References) {
					t.Fatal("alias detail omitted canonical data", canonical, alias, e, e2)
				}
			} else {
				after, e := s.Resource(t.Context(), "instruments", a)
				if e != nil || after.Revision <= before.Revision ||
					!slices.Contains(
						after.Instrument.Identifiers,
						application.PublishedIdentifier{Kind: "isin", Value: "US0000000002"},
					) {
					t.Fatal("survivor snapshot omitted converged binding", after, before, e)
				}
			}
		})
	}
}

func TestSingletonListingRejectsConflictingReportedFIGI(t *testing.T) {
	s := coverageStore(t)
	coverageReport(t, s, "first", equityRow("U1", "a", "100"))

	view, err := s.Dashboard(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	e := view.Executions[0]
	if _, err = s.db.Exec(`UPDATE listings SET figi='verified-a' WHERE id=?`, e.Listing()); err != nil {
		t.Fatal(err)
	}

	f := e.Terms().Fields().Instrument.Fields()
	id, _ := source.NewIdentifier(source.VenueFIGI, "reported-b", "figi")
	f.Identifiers = append(f.Identifiers, id)

	reported, err := source.NewInstrument(f)
	if err != nil {
		t.Fatal(err)
	}

	listing, err := acceptListing(t.Context(), s.queries, e.Instrument(), reported, "USD")
	if err != nil || listing != "" {
		t.Fatal("singleton contradicted a reported venue FIGI", listing, err)
	}
}

func TestSourceDiagnosisDoesNotSpreadToCompatibleRoot(t *testing.T) {
	s := coverageStore(t)
	coverageReport(t, s, "first", equityRow("U1", "a", "100"))

	view, err := s.Dashboard(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	a := view.Instruments[0].ID()
	now := time.Now()
	jobs := []reference.Job{{IDType: "ID_ISIN", IDValue: "US0000000001"}}

	request, err := json.Marshal(jobs)
	if err != nil {
		t.Fatal(err)
	}

	records := []reference.Record{
		{
			FIGI:         "venue",
			ShareClass:   "shared",
			Name:         "Verified issuer",
			Market:       "Equity",
			SecurityType: "Common Stock",
		},
	}

	response, err := json.Marshal([]struct {
		Data []reference.Record `json:"data"`
	}{{records}})
	if err != nil {
		t.Fatal(err)
	}

	results, err := s.SaveReference(
		t.Context(),
		reference.Evidence{At: now, Request: request, Response: response, Status: 200},
		jobs,
		[]reference.Result{{Status: "resolved", Records: records, Next: now.Add(time.Hour)}},
	)
	if err != nil {
		t.Fatal(err)
	}

	if err = s.ApplyReference(t.Context(), a, results[0]); err != nil {
		t.Fatal(err)
	}

	coverageReport(
		t,
		s,
		"contradiction",
		strings.Replace(equityRow("U1", "a", "100"), "US0000000001", "US0000000002", 1),
	)
	coverageReport(
		t,
		s,
		"second",
		strings.Replace(equityRow("U1", "b", "200"), `conid="100"`, `conid="200"`, 1),
	)

	view, err = s.Dashboard(t.Context())
	if err != nil || len(view.Instruments) != 2 {
		t.Fatal(view, err)
	}

	b := view.Instruments[1].ID()
	if err = s.ApplyReference(t.Context(), b, results[0]); err != nil {
		t.Fatal(err)
	}

	coverageReport(
		t,
		s,
		"compatible-enrichment",
		strings.NewReplacer(`conid="100"`, `conid="200"`, `isin="US0000000001"`, `isin="US0000000001" cusip="new-cusip"`).
			Replace(equityRow("U1", "b", "200")),
	)

	blocked, err := s.InstrumentDetail(t.Context(), b)
	if err != nil || blocked.Instrument.ReferenceMetadata().Reason != string(reference.ConvergenceBlocked) ||
		!blocked.Instrument.Status().NeedsReview() {
		t.Fatal("compatible enrichment cleared blocked convergence review", blocked, err)
	}

	form, err := s.CorrectionForm(t.Context(), "instrument", a)
	if err != nil || len(form.Candidates) == 0 {
		t.Fatal(form, err)
	}

	retainDecision(t, s, "instrument", a, "reference", form.Candidates[0].Value, "", "set")

	view, err = s.Dashboard(t.Context())
	if err != nil || len(view.Instruments) != 1 {
		t.Fatal("copied source diagnosis required an unrelated override", view, err)
	}
}
