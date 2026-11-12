// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"strings"
	"testing"

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/providers/snaptrade"
)

func TestV1PublishedRuleAndInterpretationBaseline(t *testing.T) {
	ctx := t.Context()
	s := newStoreWithSyntheticAccount(t)
	profile := testProfile(s.owners[0].Name())
	processConfirmationReport(t, s, "baseline", syntheticOption)

	snap, err := snaptrade.Registration(snaptrade.Options{})
	if err != nil || snap.Version() != "snaptrade/1" {
		t.Fatal("SnapTrade interpretation baseline", snap.Version(), err)
	}

	view, err := s.Dashboard(ctx)
	if err != nil || len(view.Executions) != 1 {
		t.Fatal(view, err)
	}

	activity, instrument := view.Executions[0].ID(), view.Executions[0].Instrument()

	if err = s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = Open(ctx, profile, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	if s.HistoryUpdating() {
		t.Fatal("opening the baseline scheduled a rule update")
	}

	state, err := s.queries.HistoryUpdate(ctx)
	if err != nil || state.Applied != 1 || state.Target != 0 {
		t.Fatal("release rule checkpoint", state, err)
	}

	processConfirmationReport(t, s, "replay", syntheticOption)

	replayed, err := s.Dashboard(ctx)
	if err != nil || len(replayed.Executions) != 1 || replayed.Executions[0].ID() != activity ||
		replayed.Executions[0].Instrument() != instrument {
		t.Fatal("replay changed financial identity", replayed, err)
	}

	query, err := application.NewListQuery(application.ListOptions{PageSize: 10}, application.ObservationScope)
	if err != nil {
		t.Fatal(err)
	}

	observations, err := s.ObservationList(ctx, "artifact", "replay", query)
	if err != nil || len(observations.Items) != 2 {
		t.Fatal("replayed observations", observations, err)
	}

	for _, observation := range observations.Items {
		if observation.Version != "ibkr-flex/1" || observation.Rule != "qualified-activity-observations/1" {
			t.Fatal("retained interpretation and observation rule", observation)
		}
	}

	for _, expected := range []struct {
		kind, id, selection, calculation string
	}{
		{kind: "accounts", id: "account", selection: "account-authority/1"},
		{kind: "activity", id: activity, selection: "qualified-activity-observations/1", calculation: "retained-claims-and-decisions/1"},
		{kind: "instruments", id: instrument, selection: "qualified-instrument-reference/1;", calculation: "retained-claims-and-decisions/1"},
	} {
		item, err := s.Resource(ctx, expected.kind, expected.id)
		if err != nil {
			t.Fatal(err)
		}

		why, err := s.Explanation(ctx, expected.kind, expected.id, item.Revision)
		if err != nil || !strings.HasPrefix(why.Selection, expected.selection) ||
			why.CalculationRule != expected.calculation {
			t.Fatal(expected.kind, why, err)
		}

		if expected.kind == "instruments" &&
			(why.Identity == nil || why.Identity.Rule != "openfigi-authority/1") {
			t.Fatal("OpenFIGI rule baseline", why.Identity)
		}
	}
}
