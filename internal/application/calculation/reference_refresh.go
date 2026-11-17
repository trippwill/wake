// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package calculation

import (
	"go.trippwill.dev/wake/internal/reference/figi"
	"go.trippwill.dev/wake/source"
)

type ReferenceIdentityMetadata struct {
	Family     source.InstrumentKind
	Allocation figi.Allocation
	Reason     figi.Reason
}

type ReferenceRefreshInput struct {
	Baseline InstrumentSelection
	Metadata ReferenceIdentityMetadata
	Status   ReferenceStatus
	Evidence string
	Records  []figi.Record
}

type ReferenceRefreshPlan struct {
	Selection InstrumentSelection
	Metadata  ReferenceIdentityMetadata
}

// PlanReferenceRefresh updates the automatic selection. Operator decisions
// apply when projecting that selection with the other retained inputs.
func PlanReferenceRefresh(in ReferenceRefreshInput) ReferenceRefreshPlan {
	out := ReferenceRefreshPlan{Selection: in.Baseline, Metadata: in.Metadata}
	if in.Status == "resolved" {
		selected := figi.SelectIdentity(in.Metadata.Family, in.Records)
		switch {
		case selected.FIGI != "" && in.Baseline.FIGI != "" && in.Baseline.FIGI != selected.FIGI:
			out.Selection.Status = "conflict"
			out.Metadata.Reason = figi.CompetingIdentity
		case selected.FIGI != "":
			out.Selection.FIGI = selected.FIGI
			out.Selection.Evidence = in.Evidence
			out.Selection.Status = "resolved"

			if selected.Name != "" {
				out.Selection.Name = selected.Name
			}

			if selected.Ticker != "" {
				out.Selection.Ticker = selected.Ticker
			}

			out.Metadata = ReferenceIdentityMetadata{
				Family: selected.Family, Allocation: selected.Allocation, Reason: selected.Reason,
			}
		case in.Baseline.FIGI == "" || referenceIdentityConflict(selected.Reason):
			out.Selection.Status = "ambiguous"
			out.Metadata.Reason = selected.Reason
		}
	} else if in.Baseline.FIGI == "" {
		out.Selection.Status = in.Status
		if in.Status == "provisional" {
			out.Metadata.Reason = figi.MissingIdentifier
		}
	}

	if in.Metadata.Reason == figi.SourceConflict {
		out.Selection.Status = "conflict"
		out.Metadata.Reason = figi.SourceConflict
	}

	return out
}

func referenceIdentityConflict(reason figi.Reason) bool {
	return reason == figi.ConflictingParents || reason == figi.CompetingIdentity ||
		reason == figi.IncompatibleSubject
}

type ListingCorroboration struct {
	ListingID string
	FIGIs     []string
}

// PlanListingCorroboration retains each directly corroborated venue FIGI in
// reported identifier order, then record order, for a unique listing.
func PlanListingCorroboration(
	identifiers []source.Identifier,
	records []figi.Record,
	listings []ListingIdentity,
) ListingCorroboration {
	if len(listings) != 1 {
		return ListingCorroboration{}
	}

	out := ListingCorroboration{ListingID: listings[0].ID}
	for _, reported := range identifiers {
		if !canIdentifyListing(reported.Kind()) {
			continue
		}

		for _, record := range records {
			if record.FIGI == reported.Value() && record.FIGI != record.Composite {
				out.FIGIs = append(out.FIGIs, record.FIGI)
			}
		}
	}

	return out
}
