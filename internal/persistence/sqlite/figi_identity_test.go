// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"errors"
	"strings"
	"testing"
	"time"

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/reference"
)

func applyEquityIdentity(t *testing.T, s *Store, id string) {
	t.Helper()

	if err := s.ApplyReference(
		t.Context(),
		id,
		reference.Result{
			Status: "resolved",
			Records: []reference.Record{
				{
					FIGI:         "venue",
					ShareClass:   "shared",
					Composite:    "composite",
					Market:       "Equity",
					SecurityType: "Common Stock",
					Name:         "Verified issuer",
				},
			},
			Next: time.Now().Add(time.Hour),
		},
	); err != nil {
		t.Fatal(err)
	}
}

func TestFIGICombinesBindingsAndPreservesOldLinks(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(map[bool]string{false: "forward", true: "reverse"}[reverse], func(t *testing.T) {
			s := coverageStore(t)

			a, b := equityRow(
				"U1",
				"a",
				"100",
			), strings.Replace(
				equityRow("U1", "b", "200"),
				`conid="100"`,
				`conid="200"`,
				1,
			)
			if reverse {
				a, b = b, a
			}

			coverageReport(t, s, "first", a)
			coverageReport(t, s, "second", b)

			view, err := s.Dashboard(t.Context())
			if err != nil || len(view.Instruments) != 2 {
				t.Fatal(view, err)
			}

			first, second := view.Instruments[0].ID(), view.Instruments[1].ID()
			retainDecision(t, s, "instrument", first, "name", "Operator label", "", "set")
			applyEquityIdentity(t, s, second)
			applyEquityIdentity(t, s, first)

			view, err = s.Dashboard(t.Context())
			if err != nil || len(view.Instruments) != 1 || view.Instruments[0].ID() != first ||
				view.Instruments[0].Name() != "Operator label" {
				t.Fatal(view, err)
			}

			for _, activity := range view.Executions {
				if activity.Instrument() != first {
					t.Fatal(activity)
				}
			}

			resource, err := s.Resource(t.Context(), "instruments", second)
			if err != nil || resource.ID != first {
				t.Fatal(resource, err)
			}

			if _, err = s.Explanation(
				t.Context(),
				"instruments",
				second,
				resource.Revision,
			); !errors.Is(
				err,
				application.ErrStale,
			) {
				t.Fatal("old explanation revision was accepted", err)
			}

			form, err := s.CorrectionForm(t.Context(), "instrument", first)
			if err != nil || len(form.Decisions) != 1 {
				t.Fatal(form, err)
			}

			coverageReport(t, s, "third", strings.Replace(a, `execID="a"`, `execID="third"`, 1))

			view, err = s.Dashboard(t.Context())
			if err != nil || len(view.Instruments) != 1 {
				t.Fatal("replay split identity", view, err)
			}
		})
	}
}

func TestFIGIConflictingChoicesWaitForReview(t *testing.T) {
	s := coverageStore(t)
	coverageReport(t, s, "first", equityRow("U1", "a", "100"))
	coverageReport(
		t,
		s,
		"second",
		strings.Replace(equityRow("U1", "b", "200"), `conid="100"`, `conid="200"`, 1),
	)

	view, err := s.Dashboard(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	a, b := view.Instruments[0].ID(), view.Instruments[1].ID()
	retainDecision(t, s, "instrument", a, "name", "First choice", "", "set")
	retainDecision(t, s, "instrument", b, "name", "Second choice", "", "set")
	applyEquityIdentity(t, s, a)
	applyEquityIdentity(t, s, b)

	view, err = s.Dashboard(t.Context())
	if err != nil || len(view.Instruments) != 2 {
		t.Fatal(view, err)
	}

	for _, i := range view.Instruments {
		if i.ReferenceMetadata().Reason != string(reference.OperatorConflict) {
			t.Fatal(i)
		}
	}

	retainDecision(t, s, "instrument", b, "name", "", "", "release")

	view, err = s.Dashboard(t.Context())
	if err != nil || len(view.Instruments) != 1 || view.Instruments[0].Name() != "First choice" {
		t.Fatal(view, err)
	}
}

func TestReferenceFailureDoesNotUndoVerifiedIdentity(t *testing.T) {
	s := coverageStore(t)
	coverageReport(t, s, "first", equityRow("U1", "a", "100"))

	view, err := s.Dashboard(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	id := view.Instruments[0].ID()
	applyEquityIdentity(t, s, id)

	before, err := snapshotState(t.Context(), s)
	if err != nil {
		t.Fatal(err)
	}

	for _, status := range []string{"temporary-failure", "no-match", "throttled"} {
		if err = s.ApplyReference(
			t.Context(),
			id,
			reference.Result{Status: status, Next: time.Now().Add(time.Hour)},
		); err != nil {
			t.Fatal(err)
		}

		resource, e := s.Resource(t.Context(), "instruments", id)
		if e != nil || resource.Instrument.ReferenceStatus != "resolved" || resource.Instrument.FIGI == nil ||
			*resource.Instrument.FIGI != "shared" {
			t.Fatal(resource, e)
		}
	}

	after, err := snapshotState(t.Context(), s)
	if err != nil || after.Version != before.Version {
		t.Fatal("lookup health republished accepted state", before, after, err)
	}
}

func TestFIGICombinationRollsBackAndRetainsIndependentReleases(t *testing.T) {
	s := coverageStore(t)
	coverageReport(t, s, "first", equityRow("U1", "a", "100"))
	coverageReport(
		t,
		s,
		"second",
		strings.Replace(equityRow("U1", "b", "200"), `conid="100"`, `conid="200"`, 1),
	)

	view, err := s.Dashboard(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	a, b := view.Instruments[0].ID(), view.Instruments[1].ID()
	for _, id := range []string{a, b} {
		retainDecision(t, s, "instrument", id, "name", "Shared label", "", "set")
	}

	oldForm, err := s.CorrectionForm(t.Context(), "instrument", b)
	if err != nil {
		t.Fatal(err)
	}

	applyEquityIdentity(t, s, a)

	before, err := snapshotState(t.Context(), s)
	if err != nil {
		t.Fatal(err)
	}

	if _, err = s.db.Exec(
		`CREATE TRIGGER reject_alias BEFORE INSERT ON resource_aliases BEGIN SELECT RAISE(ABORT,'injected'); END`,
	); err != nil {
		t.Fatal(err)
	}

	result := reference.Result{
		Status:  "resolved",
		Records: []reference.Record{{FIGI: "other-venue", ShareClass: "shared", Market: "Equity"}},
		Next:    time.Now().Add(time.Hour),
	}
	if err = s.ApplyReference(t.Context(), b, result); err == nil {
		t.Fatal("failed combination committed")
	}

	after, err := snapshotState(t.Context(), s)
	if err != nil || after.Version != before.Version {
		t.Fatal("rollback changed publication", err)
	}

	row, err := s.queries.Instrument(t.Context(), b)
	if err != nil || row.ReferenceFigi != "" {
		t.Fatal("rollback retained partial identity", row, err)
	}

	if _, err = s.db.Exec(`DROP TRIGGER reject_alias`); err != nil {
		t.Fatal(err)
	}

	applyEquityIdentity(t, s, b)

	stale, err := application.NewDecision(
		application.DecisionInput{
			TargetKind:       "instrument",
			TargetID:         b,
			Scope:            "name",
			Text:             "Stale label",
			Action:           "set",
			ExpectedRevision: oldForm.Revision,
			Submission:       "stale-alias-submission",
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	if err = s.SaveDecision(
		t.Context(),
		stale,
		"synthetic/operator",
		time.Now(),
	); !errors.Is(
		err,
		application.ErrStale,
	) {
		t.Fatal("old-ID correction accepted", err)
	}

	retainDecision(t, s, "instrument", a, "name", "", "", "release")
	recalculateFixture(t, s)

	detail, err := s.InstrumentDetail(t.Context(), a)
	if err != nil || detail.Instrument.Name() == "Shared label" {
		t.Fatal("released choice resurrected from alias", detail, err)
	}
}

func TestFIGICombinationPreservesDifferentVenueIdentities(t *testing.T) {
	s := coverageStore(t)
	coverageReport(t, s, "first", equityRow("U1", "a", "100"))
	coverageReport(
		t,
		s,
		"second",
		strings.Replace(equityRow("U1", "b", "200"), `conid="100"`, `conid="200"`, 1),
	)

	view, err := s.Dashboard(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	for _, e := range view.Executions {
		if _, err = s.db.Exec(
			`UPDATE listings SET figi=? WHERE id=?`,
			"venue-"+e.Instrument(),
			e.Listing(),
		); err != nil {
			t.Fatal(err)
		}
	}

	a, b := view.Instruments[0].ID(), view.Instruments[1].ID()
	applyEquityIdentity(t, s, a)
	applyEquityIdentity(t, s, b)

	detail, err := s.InstrumentDetail(t.Context(), a)
	if err != nil || len(detail.Listings) != 2 {
		t.Fatal("distinct verified venues collapsed", detail, err)
	}

	for _, e := range view.Executions {
		resource, err := s.Resource(t.Context(), "listings", e.Listing())
		if err != nil || resource.ID != e.Listing() || resource.Listing.InstrumentID != a {
			t.Fatal(resource, err)
		}
	}

	coverageReport(
		t,
		s,
		"third",
		strings.Replace(equityRow("U1", "third", "300"), `conid="100"`, `conid="300"`, 1),
	)

	current, err := s.Dashboard(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	var thirdInstrument, thirdListing string
	for _, execution := range current.Executions {
		if execution.Terms().Fields().Event.Value() == "third" {
			thirdInstrument, thirdListing = execution.Instrument(), execution.Listing()
		}
	}

	if thirdInstrument == "" || thirdListing == "" {
		t.Fatal("missing incoming listing")
	}

	applyEquityIdentity(t, s, thirdInstrument)

	retained, err := s.Resource(t.Context(), "listings", thirdListing)
	if err != nil || retained.ID != thirdListing || retained.Listing.InstrumentID != a ||
		retained.Listing.FIGI != nil {
		t.Fatal("ambiguous missing-FIGI listing was assigned a verified venue", retained, err)
	}

	detail, err = s.InstrumentDetail(t.Context(), a)
	if err != nil || len(detail.Listings) != 3 {
		t.Fatal("ambiguous listing was not retained separately", detail, err)
	}
}

func TestFIGIContradictionCannotBeOutvotedByAnotherRoot(t *testing.T) {
	s := coverageStore(t)
	coverageReport(t, s, "first", equityRow("U1", "a", "100"))
	coverageReport(
		t,
		s,
		"second",
		strings.Replace(equityRow("U1", "b", "200"), `conid="100"`, `conid="200"`, 1),
	)

	view, err := s.Dashboard(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	a, b := view.Instruments[0].ID(), view.Instruments[1].ID()
	applyEquityIdentity(t, s, a)

	if err = s.ApplyReference(
		t.Context(),
		a,
		reference.Result{
			Status:  "resolved",
			Records: []reference.Record{{FIGI: "venue", ShareClass: "different", Market: "Equity"}},
		},
	); err != nil {
		t.Fatal(err)
	}

	applyEquityIdentity(t, s, b)

	view, err = s.Dashboard(t.Context())
	if err != nil || len(view.Instruments) != 2 {
		t.Fatal("competing identity was erased", view, err)
	}

	for _, i := range view.Instruments {
		if !i.Status().NeedsReview() || i.ReferenceMetadata().Reason != string(reference.CompetingIdentity) {
			t.Fatal(i)
		}
	}
}

func TestFIGIOptionAndUnderlierBindingsRemainCombinedAfterRecalculation(t *testing.T) {
	for _, underliersFirst := range []bool{true, false} {
		t.Run(
			map[bool]string{true: "underliers-first", false: "options-first"}[underliersFirst],
			func(t *testing.T) {
				s := coverageStore(t)
				coverageReport(t, s, "first-option", syntheticOption)
				other := strings.NewReplacer(`conid="200"`, `conid="300"`, `underlyingConid="100"`, `underlyingConid="101"`, `execID="option-fill"`, `execID="other-fill"`, `tradeID="option-trade"`, `tradeID="other-trade"`, `multiplier="10"`, `multiplier="100"`).
					Replace(syntheticOption)
				coverageReport(t, s, "second-option", other)

				view, err := s.Dashboard(t.Context())
				if err != nil || len(view.Instruments) != 4 {
					t.Fatal(view, err)
				}

				options := []string{}
				underliers := []string{}
				for _, i := range view.Instruments {
					if i.Kind() == "option" {
						options = append(options, i.ID())
					} else {
						underliers = append(underliers, i.ID())
					}
				}

				resolveUnderliers := func() {
					for _, id := range underliers {
						applyEquityIdentity(t, s, id)
					}
				}
				if underliersFirst {
					resolveUnderliers()
				}
				// Retain a link event using an old ID; projection resolves its alias while
				// keeping the original decision text intact.
				if underliersFirst {
					retainDecision(t, s, "instrument", options[0], "underlier", underliers[1], "", "set")
				}

				result := reference.Result{
					Status: "resolved",
					Records: []reference.Record{
						{FIGI: "option-venue", Composite: "contract", Market: "Equity", SecurityType: "Equity Option"},
					},
					Next: time.Now().Add(time.Hour),
				}
				for _, id := range options {
					if err = s.ApplyReference(t.Context(), id, result); err != nil {
						t.Fatal(err)
					}
				}

				if !underliersFirst {
					resolveUnderliers()
				}

				recalculateFixture(t, s)

				view, err = s.Dashboard(t.Context())
				if err != nil || len(view.Instruments) != 2 {
					t.Fatal("recalculation split verified identities", view, err)
				}

				for _, i := range view.Instruments {
					if i.Status().NeedsReview() {
						t.Fatal("converged source bindings disputed", i)
					}
				}

				detail, err := s.InstrumentDetail(t.Context(), options[0])
				if err != nil || detail.Underlier != underliers[0] {
					t.Fatal(detail, err)
				}

				multipliers := map[string]bool{}
				for _, e := range view.Executions {
					multipliers[e.Terms().Fields().Instrument.Option().Fields().Multiplier.Value()] = true
					if e.Instrument() != options[0] {
						t.Fatal(e)
					}
				}

				if !multipliers["10"] || !multipliers["100"] {
					t.Fatal("historical activity terms changed", multipliers)
				}
			},
		)
	}
}

func TestSourceIdentityDisagreementSurvivesReferenceRefresh(t *testing.T) {
	s := coverageStore(t)
	coverageReport(t, s, "original", equityRow("U1", "a", "100"))

	view, err := s.Dashboard(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	id := view.Instruments[0].ID()
	applyEquityIdentity(t, s, id)
	coverageReport(
		t,
		s,
		"disputed",
		strings.Replace(equityRow("U1", "a", "100"), "US0000000001", "US0000000002", 1),
	)
	applyEquityIdentity(t, s, id)

	detail, err := s.InstrumentDetail(t.Context(), id)
	if err != nil || !detail.Instrument.Status().NeedsReview() ||
		detail.Instrument.ReferenceMetadata().Reason != string(reference.SourceConflict) ||
		detail.Instrument.FIGI() != "shared" {
		t.Fatal("old identifier lookup erased source disagreement", detail, err)
	}
}
