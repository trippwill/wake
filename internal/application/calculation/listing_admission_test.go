// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package calculation

import (
	"testing"

	"go.trippwill.dev/wake/source"
)

func TestPlanListingAdmission(t *testing.T) {
	t.Parallel()

	identifier := func(kind source.IdentifierKind, value string) source.Identifier {
		t.Helper()

		id, err := source.NewIdentifier(kind, value, "figi")
		if err != nil {
			t.Fatal(err)
		}

		return id
	}

	venueA := identifier(source.VenueFIGI, "venue-a")
	unclassifiedB := identifier(source.UnclassifiedFIGI, "venue-b")
	compositeA := identifier(source.CompositeFIGI, "venue-a")
	shareA := identifier(source.ShareClassFIGI, "venue-a")
	differentCase := identifier(source.VenueFIGI, "VENUE-A")
	a := ListingIdentity{ID: "a", FIGI: "venue-a"}
	b := ListingIdentity{ID: "b", FIGI: "venue-b"}
	duplicateA := ListingIdentity{ID: "duplicate", FIGI: a.FIGI}
	unverified := ListingIdentity{ID: "unverified"}

	for _, tc := range []struct {
		name        string
		identifiers []source.Identifier
		candidates  []ListingIdentity
		want        ListingAdmission
	}{
		{"create without candidates", []source.Identifier{venueA}, nil, ListingAdmission{Create: true}},
		{"exact match among candidates", []source.Identifier{unclassifiedB}, []ListingIdentity{a, b}, ListingAdmission{ExistingID: b.ID}},
		{"identifier order", []source.Identifier{unclassifiedB, venueA}, []ListingIdentity{a, b}, ListingAdmission{ExistingID: b.ID}},
		{"candidate order", []source.Identifier{venueA}, []ListingIdentity{duplicateA, a}, ListingAdmission{ExistingID: duplicateA.ID}},
		{"exact case required", []source.Identifier{differentCase}, []ListingIdentity{a}, ListingAdmission{}},
		{"conflicting singleton", []source.Identifier{venueA}, []ListingIdentity{b}, ListingAdmission{}},
		{"unverified singleton", []source.Identifier{venueA}, []ListingIdentity{unverified}, ListingAdmission{ExistingID: unverified.ID}},
		{"singleton by terms", []source.Identifier{compositeA, shareA}, []ListingIdentity{b}, ListingAdmission{ExistingID: b.ID}},
		{"ambiguous terms", []source.Identifier{compositeA, shareA}, []ListingIdentity{a, b}, ListingAdmission{}},
		{"missing matching FIGI", []source.Identifier{venueA}, []ListingIdentity{unverified, b}, ListingAdmission{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := PlanListingAdmission(tc.identifiers, tc.candidates); got != tc.want {
				t.Fatalf("admission = %+v, want %+v", got, tc.want)
			}
		})
	}
}
