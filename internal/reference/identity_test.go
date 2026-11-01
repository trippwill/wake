// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package reference

import (
	"slices"
	"testing"

	"go.trippwill.dev/wake/source"
)

func TestAllocationLookupTypes(t *testing.T) {
	for kind, want := range map[source.IdentifierKind]string{source.VenueFIGI: "ID_BB_GLOBAL", source.InstrumentFIGI: "ID_BB_GLOBAL", source.CompositeFIGI: "COMPOSITE_ID_BB_GLOBAL", source.ShareClassFIGI: "ID_BB_GLOBAL_SHARE_CLASS_LEVEL", source.OCC: "OCC_SYMBOL"} {
		id, err := source.NewIdentifier(kind, "identifier", "field")
		if err != nil {
			t.Fatal(err)
		}

		got, ok := LookupJob(id)
		if !ok || got.IDType != want {
			t.Fatal(kind, got)
		}
	}
}

func TestIdentitySelectionUsesParentRelationships(t *testing.T) {
	records := []Record{
		{
			FIGI:         "venue-1",
			Composite:    "composite",
			ShareClass:   "share",
			Name:         "Issuer",
			Ticker:       "ONE",
			Market:       "Equity",
			SecurityType: "Common Stock",
		},
		{
			FIGI:         "venue-2",
			Composite:    "composite",
			Name:         "Issuer",
			Ticker:       "TWO",
			Market:       "Equity",
			SecurityType: "Common Stock",
		},
	}
	for range 2 {
		selected := SelectIdentity(source.UnknownInstrument, records)
		if selected.FIGI != "share" || selected.Allocation != ShareClassAllocation ||
			selected.Family != source.Equity ||
			selected.Name != "Issuer" ||
			selected.Ticker != "" {
			t.Fatal(selected)
		}

		if jobs := MissingComposites(source.Equity, records); len(jobs) != 0 {
			t.Fatal(jobs)
		}

		slices.Reverse(records)
	}

	direct := []Record{records[0]}
	direct[0].ShareClass = ""
	if got := MissingComposites(source.Equity, direct); len(got) != 1 || got[0].IDValue != "composite" {
		t.Fatal(got)
	}

	records = append(
		records,
		Record{FIGI: "venue-3", Composite: "composite", ShareClass: "other", Market: "Equity"},
	)
	if got := SelectIdentity(source.Equity, records); got.FIGI != "" || got.Reason != ConflictingParents {
		t.Fatal(got)
	}
}

func TestOptionIdentityIgnoresClassificationSpellingAndRowOrder(t *testing.T) {
	records := []Record{
		{
			FIGI:          "venue-1",
			Composite:     "contract",
			SecurityType:  "Equity Option",
			SecurityType2: "Option",
			Market:        "Equity",
		},
		{
			FIGI:          "venue-2",
			Composite:     "contract",
			SecurityType:  "Equity Option",
			SecurityType2: "Equity",
			Market:        "Equity",
		},
	}
	got := SelectIdentity(source.Option, records)
	if got.FIGI != "contract" || got.Allocation != CompositeAllocation || got.Reason != IncompleteMetadata {
		t.Fatal(got)
	}

	if got = SelectIdentity(source.Equity, records); got.FIGI != "" || got.Reason != IncompatibleSubject {
		t.Fatal(got)
	}

	records[1].Composite = "different"
	if got = SelectIdentity(source.Option, records); got.FIGI != "" || got.Reason != CompetingIdentity {
		t.Fatal(got)
	}
}

func TestOptionIdentityRejectsContradictoryClassifications(t *testing.T) {
	for _, tc := range []struct{ primary, secondary string }{
		{"", "Common Stock"}, {"Equity Option", "Common Stock"}, {"Common Stock", "Option"},
	} {
		got := SelectIdentity(
			source.Option,
			[]Record{
				{
					FIGI:          "underlier-venue",
					Composite:     "underlier-composite",
					Market:        "Equity",
					SecurityType:  tc.primary,
					SecurityType2: tc.secondary,
				},
			},
		)
		if got.FIGI != "" || got.Reason != IncompatibleSubject {
			t.Fatal("non-option classification identified an option", tc, got)
		}
	}
}

func TestDebtIdentityRejectsExplicitEquityWithoutMarket(t *testing.T) {
	for _, record := range []Record{{FIGI: "equity", SecurityType: "Equity Option"}, {FIGI: "equity", SecurityType2: "Option"}, {FIGI: "equity", SecurityType: "Common Stock"}, {FIGI: "equity", SecurityType2: "Common Stock"}, {FIGI: "equity", SecurityType2: "Equity"}} {
		got := SelectIdentity(source.Debt, []Record{record})
		if got.FIGI != "" || got.Reason != IncompatibleSubject {
			t.Fatal("explicit equity classification identified a debt issue", record, got)
		}
	}
}
