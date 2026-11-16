// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package calculation

import "go.trippwill.dev/wake/source"

type RootClaim struct {
	ID                string
	Ordinal, Sequence int64
	Role              string
	Reported          source.Instrument
}

type UnderlierLink struct {
	ID string
	// Invalid targets clear a previous automatic choice even during conflict.
	Invalid bool
}

func CalculateRootClaims(
	reported source.Instrument,
	hasProjectedFIGI bool,
	claims []RootClaim,
	bindings map[source.Reference]RootBinding,
) (RootCalculation, error) {
	baseline := reported
	if !hasProjectedFIGI && len(claims) != 0 {
		baseline = claims[0].Reported
	}

	values := make([]source.Instrument, 0, len(claims))
	for _, claim := range claims {
		values = append(values, claim.Reported)
	}

	return CalculateBoundRoot(baseline, values, bindings)
}

type SourceConflictChange uint8

const (
	KeepSourceConflict SourceConflictChange = iota
	RecordSourceConflict
	ClearSourceConflict
)

type RootEnrichment struct {
	Conflict SourceConflictChange
	Reported *source.Instrument
	Family   source.InstrumentKind
	// Nil preserves the automatic underlier. An empty ID clears it.
	Underlier *string
}

func PlanRootEnrichment(
	family source.InstrumentKind,
	hasProjectedFIGI, hadSourceConflict bool,
	result RootCalculation,
	link UnderlierLink,
) RootEnrichment {
	var plan RootEnrichment

	if result.Conflict {
		plan.Conflict = RecordSourceConflict
	} else {
		if hadSourceConflict {
			plan.Conflict = ClearSourceConflict
		}

		plan.Reported = &result.Reported
		plan.Family = family
		if !hasProjectedFIGI {
			plan.Family = result.Reported.Kind()
		}
	}

	if link.ID != "" || link.Invalid || !result.Conflict && result.Underlier.Kind() == "" {
		plan.Underlier = &link.ID
	}

	return plan
}

func (c SourceConflictChange) Status(automaticFIGI string) ReferenceStatus {
	if c == RecordSourceConflict {
		return "conflict"
	}

	if automaticFIGI != "" {
		return "resolved"
	}

	return "provisional"
}
