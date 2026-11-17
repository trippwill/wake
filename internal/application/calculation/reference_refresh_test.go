// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package calculation

import (
	"testing"

	"go.trippwill.dev/wake/internal/reference/figi"
	"go.trippwill.dev/wake/source"
)

func TestPlanReferenceRefreshPreservesEstablishedFacts(t *testing.T) {
	t.Parallel()

	baseline := InstrumentSelection{
		Name:      "Retained name",
		Ticker:    "SYN",
		FIGI:      "share",
		Evidence:  "old",
		Underlier: "parent",
		Status:    "resolved",
	}
	metadata := ReferenceIdentityMetadata{Family: source.Equity, Allocation: figi.ShareClassAllocation}
	for _, tc := range []struct {
		name, evidence string
		status, want   ReferenceStatus
		records        []figi.Record
		reason, result figi.Reason
	}{
		{name: "negative refresh", status: "no-match", want: "resolved", evidence: "old"},
		{name: "lookup failure", status: "temporary-failure", want: "resolved", evidence: "old"},
		{name: "missing parent", status: "resolved", records: []figi.Record{{FIGI: "venue", Market: "Equity"}}, want: "resolved", evidence: "old"},
		{name: "matching refresh with incomplete labels", status: "resolved", records: []figi.Record{{FIGI: "venue", ShareClass: "share"}}, want: "resolved", evidence: "new", result: figi.IncompleteMetadata},
		{name: "different identity", status: "resolved", records: []figi.Record{{FIGI: "venue", ShareClass: "other", Name: "Other"}}, want: "conflict", evidence: "old", result: figi.CompetingIdentity},
		{name: "incompatible result", status: "resolved", records: []figi.Record{{FIGI: "debt", Market: "Govt"}}, want: "ambiguous", evidence: "old", result: figi.IncompatibleSubject},
		{name: "source conflict precedes reference conflict", status: "resolved", records: []figi.Record{{FIGI: "venue", ShareClass: "other"}}, want: "conflict", evidence: "old", reason: figi.SourceConflict, result: figi.SourceConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := ReferenceRefreshInput{
				Baseline: baseline,
				Metadata: metadata,
				Status:   tc.status,
				Evidence: "new",
				Records:  tc.records,
			}
			input.Metadata.Reason = tc.reason
			want := ReferenceRefreshPlan{Selection: baseline, Metadata: metadata}
			want.Selection.Status, want.Selection.Evidence, want.Metadata.Reason = tc.want, tc.evidence, tc.result

			if got := PlanReferenceRefresh(input); got != want {
				t.Fatalf("refresh = %+v, want %+v", got, want)
			}
		})
	}
}

func TestPlanReferenceRefreshInitialSelection(t *testing.T) {
	t.Parallel()

	in := ReferenceRefreshInput{
		Baseline: InstrumentSelection{Name: "Reported", Ticker: "SYN", Status: "provisional"},
		Metadata: ReferenceIdentityMetadata{Family: source.UnknownInstrument},
		Status:   "provisional",
	}
	waiting := PlanReferenceRefresh(in)
	if waiting.Selection != in.Baseline || waiting.Metadata.Reason != figi.MissingIdentifier {
		t.Fatal("empty selection did not await a lookup identifier", waiting)
	}

	in.Status, in.Evidence = "resolved", "new"
	in.Records = []figi.Record{
		{
			FIGI:         "venue",
			ShareClass:   "share",
			Market:       "Equity",
			SecurityType: "Common Stock",
			Name:         "Verified",
			Ticker:       "VER",
		},
	}
	want := ReferenceRefreshPlan{
		Selection: InstrumentSelection{
			Name:     "Verified",
			Ticker:   "VER",
			FIGI:     "share",
			Evidence: "new",
			Status:   "resolved",
		},
		Metadata: ReferenceIdentityMetadata{Family: source.Equity, Allocation: figi.ShareClassAllocation},
	}
	if got := PlanReferenceRefresh(in); got != want {
		t.Fatalf("refresh = %+v, want %+v", got, want)
	}

	in.Metadata.Reason = figi.SourceConflict
	want.Selection.Status, want.Metadata.Reason = "conflict", figi.SourceConflict

	if got := PlanReferenceRefresh(in); got != want {
		t.Fatalf("source conflict refresh = %+v, want %+v", got, want)
	}
}
