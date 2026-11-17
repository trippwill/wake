// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package calculation

import "go.trippwill.dev/wake/source"

type ListingAdmission struct {
	ExistingID string
	Create     bool
}

// PlanListingAdmission uses matching venue, ticker and currency candidates in
// their supplied order. A reported venue identifier may select an existing
// verified listing even when its terms match other listings.
func PlanListingAdmission(identifiers []source.Identifier, candidates []ListingIdentity) ListingAdmission {
	if len(candidates) == 0 {
		return ListingAdmission{Create: true}
	}

	hasVenueFIGI := false
	for _, identifier := range identifiers {
		if !canIdentifyListing(identifier.Kind()) {
			continue
		}

		hasVenueFIGI = true

		for _, candidate := range candidates {
			if candidate.FIGI == identifier.Value() {
				return ListingAdmission{ExistingID: candidate.ID}
			}
		}
	}

	if len(candidates) == 1 && (!hasVenueFIGI || candidates[0].FIGI == "") {
		return ListingAdmission{ExistingID: candidates[0].ID}
	}

	return ListingAdmission{}
}

func canIdentifyListing(kind source.IdentifierKind) bool {
	return kind == source.VenueFIGI || kind == source.UnclassifiedFIGI
}
