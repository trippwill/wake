// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package calculation

import "testing"

func TestPlanListingMergePreservesOrderedFIGISelection(t *testing.T) {
	blank := ListingIdentity{ID: "blank"}
	first := ListingIdentity{ID: "first", FIGI: "same"}
	second := ListingIdentity{ID: "second", FIGI: "same"}
	distinct := ListingIdentity{ID: "distinct", FIGI: "different"}

	for _, tc := range []struct {
		name, figi string
		candidates []ListingIdentity
		want       ListingMerge
	}{
		{"first exact beats blank", "same", []ListingIdentity{blank, first, second}, ListingMerge{TargetID: first.ID}},
		{"empty exact beats fallback", "", []ListingIdentity{first, blank}, ListingMerge{TargetID: blank.ID}},
		{"enrich blank", "same", []ListingIdentity{blank, distinct}, ListingMerge{TargetID: blank.ID, FIGIToCopy: "same"}},
		{"keep known FIGI", "", []ListingIdentity{first}, ListingMerge{TargetID: first.ID}},
		{"different FIGI", "same", []ListingIdentity{distinct}, ListingMerge{}},
		{"ambiguous fallback", "", []ListingIdentity{first, distinct}, ListingMerge{}},
		{"no candidate", "same", nil, ListingMerge{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := PlanListingMerge(tc.figi, tc.candidates); got != tc.want {
				t.Fatalf("merge = %+v, want %+v", got, tc.want)
			}
		})
	}
}
