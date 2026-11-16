// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package application

import (
	"testing"

	"go.trippwill.dev/wake/internal/reference"
	"go.trippwill.dev/wake/source"
)

func TestRootEnrichmentPreservesAuthorityAndLinkEffects(t *testing.T) {
	reported, err := source.NewInstrument(source.InstrumentInput{Kind: source.Equity, Name: "Reported"})
	if err != nil {
		t.Fatal(err)
	}

	verified := IdentityRoot{FIGI: "verified", Family: source.Fund, Reason: reference.SourceConflict}
	for _, tc := range []struct {
		name       string
		root       IdentityRoot
		result     RootCalculation
		link       UnderlierLink
		conflict   SourceConflictChange
		family     source.InstrumentKind
		changeLink bool
	}{
		{"preserve link under conflict", verified, RootCalculation{Reported: reported, Conflict: true}, UnderlierLink{}, RecordSourceConflict, "", false},
		{"clear invalid link under conflict", verified, RootCalculation{Reported: reported, Conflict: true}, UnderlierLink{Invalid: true}, RecordSourceConflict, "", true},
		{"restore automatic status", verified, RootCalculation{Reported: reported}, UnderlierLink{}, ClearSourceConflict, source.Fund, true},
		{"preserve independent conflict", IdentityRoot{Reason: reference.OperatorConflict}, RootCalculation{Reported: reported, Underlier: reported}, UnderlierLink{}, KeepSourceConflict, source.Equity, false},
		{"set automatic link", IdentityRoot{}, RootCalculation{Reported: reported, Underlier: reported}, UnderlierLink{ID: "underlier"}, KeepSourceConflict, source.Equity, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := PlanRootEnrichment(tc.root, tc.result, tc.link)
			if plan.Conflict != tc.conflict || plan.Family != tc.family ||
				(plan.Reported == nil) != tc.result.Conflict ||
				(plan.Underlier != nil) != tc.changeLink ||
				tc.changeLink && *plan.Underlier != tc.link.ID {
				t.Fatalf("unexpected effects: %+v", plan)
			}
		})
	}

	if ClearSourceConflict.Status("") != "provisional" ||
		ClearSourceConflict.Status("automatic") != "resolved" {
		t.Fatal("conflict recovery did not use the automatic FIGI")
	}
}

func TestCalculateRootClaimsPreservesBaselineAuthority(t *testing.T) {
	instrument := func(name string) source.Instrument {
		t.Helper()

		value, err := source.NewInstrument(source.InstrumentInput{Kind: source.Equity, Name: name})
		if err != nil {
			t.Fatal(err)
		}

		return value
	}
	stored := instrument("Stored")
	claims := []RootClaim{{Reported: instrument("First claim")}, {Reported: instrument("Later claim")}}
	for _, tc := range []struct {
		name     string
		verified bool
		claims   []RootClaim
		want     string
	}{
		{"verified root", true, claims, "Stored"},
		{"provisional root", false, claims, "First claim"},
		{"no claims", false, nil, "Stored"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := CalculateRootClaims(stored, tc.verified, tc.claims, nil)
			if err != nil || got.Reported.Name() != tc.want {
				t.Fatalf("reported name = %q, want %q: %v", got.Reported.Name(), tc.want, err)
			}
		})
	}
}
