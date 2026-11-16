// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"fmt"
	"strings"
	"testing"

	"go.trippwill.dev/wake/internal/application/calculation"
	"go.trippwill.dev/wake/internal/persistence/sqlite/sqlc"
	"go.trippwill.dev/wake/providers/ibkrflex"
)

func TestCorrectionComponentsMatchFullGraphWithoutReadingDisjointHistory(t *testing.T) {
	s := newStoreWithSyntheticAccount(t)
	ctx := t.Context()

	processConfirmationReport(t, s, "unresolved", correctionRow("cancel", "Ca", "Tmissing"))
	processConfirmationReport(t, s, "ends", equityRow("U1", "a", "100")+equityRow("U1", "other", "100"))
	processConfirmationReport(t, s, "first", correctionRow("b", "Co", "Ta"))
	processConfirmationReport(t, s, "second", correctionRow("c", "Co", "Tb"))

	view, err := s.Dashboard(ctx)
	if err != nil {
		t.Fatal(err)
	}

	ids := map[string]string{}
	for _, activity := range view.Executions {
		ids[activity.Terms().Fields().Event.Value()] = activity.ID()
	}

	// Sharing an alias does not connect activities unless an instruction uses it.
	for _, name := range []string{"a", "other"} {
		if err := s.queries.PutEventLink(ctx, sqlc.PutEventLinkParams{
			AccountID: "account", Reference: "unused-alias", ExecutionID: ids[name],
		}); err != nil {
			t.Fatal(err)
		}
	}

	processConfirmationReport(t, s, "other-correction", correctionRow("cancel-other", "Ca", "Tother"))

	recorded := &rootPlanDB{DB: s.db}
	params := sqlc.AffectedSourceCorrectionsParams{
		ActivityIds: fmt.Sprintf("[%q]", ids["a"]), ArtifactID: "unresolved", Version: ibkrflex.Version,
	}
	load := func() []sqlc.AffectedSourceCorrectionsRow {
		t.Helper()

		rows, err := sqlc.New(recorded).AffectedSourceCorrections(ctx, params)
		if err != nil {
			t.Fatal(err)
		}

		return rows
	}

	before := load()

	var unrelated strings.Builder
	for i := range 64 {
		unrelated.WriteString(correctionRow(fmt.Sprintf("disjoint-%d", i), "Ca", fmt.Sprintf("Tdisjoint-%d", i)))
	}

	processConfirmationReport(t, s, "disjoint", unrelated.String())
	assertCorrectionInputsMatchGraph(t, s)

	after := load()
	if len(before) != 6 || len(after) != len(before) {
		t.Fatalf("wanted endpoint roles for two connected instructions and the unresolved seed, got %d and %d",
			len(before), len(after))
	}

	edges, occurrences := correctionEdges(after)

	states, statuses := calculation.CorrectionStates(edges)
	if len(states) != 3 || states[ids["c"]].Lifecycle() != calculation.Accepted {
		t.Fatal("did not reach the replacement leaf", states)
	}

	fullRows, fullEdges, err := correctionInputs(ctx, s.queries)
	if err != nil {
		t.Fatal(err)
	}

	fullStates, fullStatuses := calculation.CorrectionStates(fullEdges)
	for id, state := range states {
		if state != fullStates[id] {
			t.Fatal("scoped and full activity states differ", id)
		}
	}

	for i, row := range fullRows {
		if row.Status != string(fullStatuses[i]) {
			t.Fatal("retained processing committed an incorrect status", row, fullStatuses[i])
		}

		for j, occurrence := range occurrences {
			if row.ArtifactID == occurrence.ArtifactID && row.Version == occurrence.Version &&
				row.Location == occurrence.Location &&
				statuses[j] != fullStatuses[i] {
				t.Fatal("scoped and full instruction statuses differ", occurrence)
			}
		}
	}

	plan, err := s.db.QueryContext(ctx, "EXPLAIN QUERY PLAN "+recorded.query, recorded.args...)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = plan.Close() }()

	var steps strings.Builder
	for plan.Next() {
		var (
			id, parent, unused int
			detail             string
		)
		if err := plan.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}

		fmt.Fprintln(&steps, detail)
	}

	if err := plan.Err(); err != nil {
		t.Fatal(err)
	}

	for _, index := range []string{"event_links_execution", "corrections_target", "corrections_replacement", "sqlite_autoindex_event_links_1", "sqlite_autoindex_source_corrections_1"} {
		if !strings.Contains(steps.String(), index) {
			t.Fatalf("component traversal does not use %s:\n%s", index, steps.String())
		}
	}
}
