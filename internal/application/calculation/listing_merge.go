// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package calculation

type ListingIdentity struct {
	ID, FIGI string
}

type ListingMerge struct {
	// An empty target preserves the listing ID when it moves to the surviving instrument.
	TargetID, FIGIToCopy string
}

// PlanListingMerge uses candidates with the same venue, ticker and currency in
// their supplied order. A missing FIGI supports a merge only if unambiguous.
func PlanListingMerge(figi string, candidates []ListingIdentity) ListingMerge {
	for _, candidate := range candidates {
		if candidate.FIGI == figi {
			return ListingMerge{TargetID: candidate.ID}
		}
	}

	var match ListingMerge
	for _, candidate := range candidates {
		if candidate.FIGI != "" && figi != "" {
			continue
		}

		if match.TargetID != "" {
			return ListingMerge{}
		}

		match.TargetID, match.FIGIToCopy = candidate.ID, figi
	}

	return match
}
