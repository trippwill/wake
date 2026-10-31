// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"reflect"
	"strings"
	"testing"

	"go.trippwill.dev/wake/internal/application"
)

func TestInstrumentRelinkReplayPreservesSourceAndTarget(t *testing.T) {
	ctx := t.Context()
	s := coverageStore(t)
	original := equityRow("U1", "original", "100")
	other := strings.NewReplacer(`conid="100"`, `conid="101"`,
		`isin="US0000000001"`, `isin="US0000000002"`).Replace(equityRow("U1", "other", "100"))
	coverageReport(t, s, "initial", original+original+other)

	view, err := s.Dashboard(ctx)
	if err != nil {
		t.Fatal(err)
	}

	var activity, target application.Execution
	for _, execution := range view.Executions {
		if execution.Terms().Fields().Event.Value() == "original" {
			activity = execution
		} else {
			target = execution
		}
	}

	retainDecision(t, s, "activity", activity.ID(), "instrument", target.Instrument(), "", "set")

	if got := observationRowsForTest(t, s, "instrument", activity.Instrument()); len(got) != 2 {
		t.Fatal("operator relink moved original source appearances", got)
	}

	if got := observationRowsForTest(t, s, "instrument", target.Instrument()); len(got) != 1 {
		t.Fatal("operator target inherited unrelated source appearances", got)
	}

	before, err := s.Resource(ctx, "instruments", target.Instrument())
	if err != nil {
		t.Fatal(err)
	}

	for _, phase := range []string{"replay", "restart", "recalculate"} {
		if phase == "restart" {
			profile := testProfile(s.owners[0].Name())
			if err = s.Close(); err != nil {
				t.Fatal(err)
			}

			s, err = Open(ctx, profile, nil)
			if err != nil {
				t.Fatal(err)
			}

			t.Cleanup(func() { _ = s.Close() })
		}

		if phase == "recalculate" {
			recalculateFixture(t, s)
		}

		coverageReport(t, s, phase, original)

		after, e := s.Resource(ctx, "instruments", target.Instrument())
		if e != nil || !reflect.DeepEqual(before, after) {
			t.Fatal(phase, "source replay changed corrected target", before, after, e)
		}

		detail, e := s.ExecutionDetail(ctx, activity.ID())
		if e != nil || detail.Execution.Instrument() != target.Instrument() {
			t.Fatal(phase, "lost instrument decision", detail, e)
		}
	}

	// A real source contradiction still belongs to the original root.
	coverageReport(t, s, "contradiction", strings.ReplaceAll(original, "US0000000001", "US0000000003"))

	sourceRoot, err := s.InstrumentDetail(ctx, activity.Instrument())
	if err != nil || sourceRoot.Instrument.Status() != "conflict" {
		t.Fatal("source contradiction was lost", sourceRoot, err)
	}

	after, err := s.Resource(ctx, "instruments", target.Instrument())
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("source contradiction reached corrected target", before, after, err)
	}

	retainDecision(t, s, "activity", activity.ID(), "instrument", "", "", "release")

	detail, err := s.ExecutionDetail(ctx, activity.ID())
	if err != nil || detail.Execution.Instrument() != activity.Instrument() {
		t.Fatal("release lost original source link", detail, err)
	}
}
