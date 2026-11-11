// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"go.trippwill.dev/wake/financial"
	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/source"
)

func TestReportAppearancesSurviveDiscardedPartialAndRepeatedConflictSupport(t *testing.T) {
	s := coverageStore(t)
	full := partialTrade(t).Observation()
	processPartial(t, s, "first", full)

	rows, err := s.queries.Executions(t.Context())
	if err != nil || len(rows) != 1 {
		t.Fatal(rows, err)
	}

	id := rows[0].ID
	partial := partialObservation(t, full.Fields(), full.Fields().Fields[:1]...)
	retainPartial(t, s, "unprocessed", partial)

	if got := observationRowsForTest(t, s, "artifact", "unprocessed"); len(got) != 0 {
		t.Fatal("normalization alone exposed processed observations", got)
	}

	if err = s.Process(t.Context(), "unprocessed", partialVersion); err != nil {
		t.Fatal(err)
	}

	if got := observationRowsForTest(t, s, "artifact", "unprocessed"); len(got) != 1 ||
		got[0].ActivityID != id || got[0].Outcome != application.ObservationNoContribution {
		t.Fatal("discarded partial lost report context", got)
	}

	processPartial(t, s, "partial-repeat", partial)

	if got := observationRowsForTest(t, s, "artifact", "partial-repeat"); len(got) != 1 ||
		got[0].ActivityID != id || got[0].Outcome != application.ObservationNoContribution {
		t.Fatal("known discarded claim lost report context", got)
	}

	conflictFields := partialTrade(t).Fields()
	conflictFields.Price, _ = financial.ParsePrice("101", conflictFields.Price.Currency())

	conflict, err := source.NewTrade(conflictFields)
	if err != nil {
		t.Fatal(err)
	}

	processPartial(t, s, "conflict", conflict.Observation())
	processPartial(t, s, "conflict-repeat", conflict.Observation())

	if got := observationRowsForTest(t, s, "artifact", "conflict-repeat"); len(got) != 1 ||
		got[0].ActivityID != id || got[0].Outcome != application.ObservationNoContribution {
		t.Fatal("repeated conflict added support or lost report context", got)
	}

	if got := observationRowsForTest(t, s, "activity", id); len(got) != 2 {
		t.Fatal("report appearances became supporting links", got)
	}

	before, err := snapshotState(t.Context(), s)
	if err != nil {
		t.Fatal(err)
	}

	supports := tableCount(t, s, "execution_observations")
	for range 2 {
		_ = observationRowsForTest(t, s, "artifact", "partial-repeat")
		_ = observationRowsForTest(t, s, "artifact", "conflict-repeat")
	}

	after, err := snapshotState(t.Context(), s)
	if err != nil || !reflect.DeepEqual(before, after) ||
		tableCount(t, s, "execution_observations") != supports {
		t.Fatal("report inspection wrote financial state", err)
	}
}

func TestDiscardedReportContextUsesDistinctEventAndAliasAmbiguityRules(t *testing.T) {
	s := coverageStore(t)
	first := partialTrade(t).Fields()
	alias, _ := source.NewReference("fixture", "trade", "U1", "shared")
	first.EventAliases = []source.Reference{alias}

	trade, err := source.NewTrade(first)
	if err != nil {
		t.Fatal(err)
	}

	processPartial(t, s, "first", trade.Observation())
	partial := partialObservation(t, trade.Observation().Fields(), trade.Observation().Fields().Fields[:1]...)
	processPartial(t, s, "discarded", partial)
	rows, _ := s.queries.Executions(t.Context())
	id := rows[0].ID
	other := first
	other.Event, _ = source.NewReference("fixture", "execution", "U1", "second")
	other.Location = "second"

	second, err := source.NewTrade(other)
	if err != nil {
		t.Fatal(err)
	}

	processPartial(t, s, "second", second.Observation())

	got := observationRowsForTest(t, s, "artifact", "discarded")
	if len(got) != 1 || got[0].ActivityID != id {
		t.Fatal("shared alias obscured distinct fill", got)
	}

	for _, distinct := range []bool{false, true} {
		f := partial.Fields()
		if distinct {
			f.Event = other.Event
		} else {
			f.Event, f.EventAliases = alias, nil
		}

		f.EventDistinct = distinct

		value, e := source.NewActivityObservation(f)
		if e != nil {
			t.Fatal(e)
		}

		report := fmt.Sprint("matcher-", distinct)
		processPartial(t, s, report, value)

		claims, e := s.Records(t.Context(), report, partialVersion)
		if e != nil {
			t.Fatal(e)
		}

		var target string

		e = s.db.QueryRow(`SELECT coalesce(execution_id,'') FROM report_activity_targets WHERE claim_id=? AND ordinal=?`, claims[0].ClaimID, claims[0].Ordinal).
			Scan(&target)
		want, _, matchErr := matchReferences(
			t.Context(),
			s.queries,
			"account",
			f.Event,
			f.EventAliases,
			f.EventDistinct,
		)
		if e != nil || matchErr != nil || target != want {
			t.Fatal("report target differs from reconciliation", distinct, target, want, e, matchErr)
		}

		got = observationRowsForTest(t, s, "artifact", report)
		if len(got) != 1 || got[0].ActivityID != want {
			t.Fatal("report selected an ambiguous activity", got, want)
		}
	}
}

func TestReportPendingObservationsRemainVisible(t *testing.T) {
	s := coverageStore(t)
	full := partialTrade(t).Observation()
	partial := partialObservation(t, full.Fields(), full.Fields().Fields[:1]...)
	processPartial(t, s, "pending", partial)
	processPartial(t, s, "pending-repeat", partial)

	for _, id := range []string{"pending", "pending-repeat"} {
		got := observationRowsForTest(t, s, "artifact", id)
		if len(got) != 1 || got[0].ActivityID != "" || got[0].Outcome != application.ObservationIncomplete {
			t.Fatal("pending source meaning lost", id, got)
		}
	}

	processPartial(t, s, "completion", full)

	got := observationRowsForTest(t, s, "artifact", "pending-repeat")
	if len(got) != 1 || got[0].ActivityID == "" || got[0].Outcome != application.ObservationNoContribution {
		t.Fatal("completion did not update derived report context", got)
	}
}

func TestReportRepeatedLinkedIdentityReviewRemainsVisible(t *testing.T) {
	s := coverageStore(t)
	f := partialTrade(t).Fields()
	alias, _ := source.NewReference("fixture", "trade", "U1", "shared-review")
	f.EventAliases = []source.Reference{alias}

	first, err := source.NewTrade(f)
	if err != nil {
		t.Fatal(err)
	}

	processPartial(t, s, "first", first.Observation())
	meaning := first.Observation().Fields()
	meaning.Event, meaning.EventAliases, meaning.EventDistinct = alias, nil, false
	amount, _ := financial.ParseMoney("1", f.Price.Currency())
	partial := partialObservation(
		t,
		meaning,
		partialField(t, source.ActivityFieldInput{Name: source.FieldRealizedPnL, Money: amount}),
	)
	processPartial(t, s, "alias-meaning", partial)

	f.Event, _ = source.NewReference("fixture", "execution", "U1", "second")

	second, err := source.NewTrade(f)
	if err != nil {
		t.Fatal(err)
	}

	processPartial(t, s, "second", second.Observation())
	processPartial(t, s, "alias-repeat", partial)

	for _, id := range []string{"alias-meaning", "alias-repeat"} {
		got := observationRowsForTest(t, s, "artifact", id)
		if len(got) != 1 || got[0].ActivityID == "" || got[0].Outcome != application.ObservationNativeReview {
			t.Fatal("repeat hid unresolved identity on an established association", got)
		}
	}
}

func TestReportContextQueriesUseSourceAndActivityKeys(t *testing.T) {
	s := coverageStore(t)

	plan, err := s.db.QueryContext(
		t.Context(),
		`EXPLAIN QUERY PLAN SELECT * FROM report_observations WHERE artifact_id=?`,
		"one-report",
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = plan.Close() }()

	var steps []string
	for plan.Next() {
		var (
			id, parent, unused int
			detail             string
		)
		if err = plan.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}

		steps = append(steps, detail)
	}

	if err = plan.Err(); err != nil {
		t.Fatal(err)
	}

	text := strings.Join(steps, "\n")
	for _, scan := range []string{"SCAN c", "MATERIALIZE report_activity_targets"} {
		if strings.Contains(text, scan) {
			t.Fatal("report context reads unrelated history", text)
		}
	}

	for _, key := range []string{
		"SEARCH r USING INDEX sqlite_autoindex_records_1 (artifact_id=?)",
		"SEARCH other USING INDEX event_links_execution (execution_id=?)",
		"SEARCH r USING INDEX sqlite_autoindex_activity_references_1 (claim_id=? AND ordinal=?)",
	} {
		if !strings.Contains(text, key) {
			t.Fatal("report context scans unrelated history", key, text)
		}
	}
}
