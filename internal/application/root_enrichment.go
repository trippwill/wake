// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package application

import (
	"go.trippwill.dev/wake/internal/reference"
	"go.trippwill.dev/wake/source"
)

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

func PlanRootEnrichment(root IdentityRoot, result RootCalculation, link UnderlierLink) RootEnrichment {
	var plan RootEnrichment

	if result.Conflict {
		plan.Conflict = RecordSourceConflict
	} else {
		if root.Reason == reference.SourceConflict {
			plan.Conflict = ClearSourceConflict
		}

		plan.Reported = &result.Reported
		plan.Family = root.Family
		if root.FIGI == "" {
			plan.Family = result.Reported.Kind()
		}
	}

	if link.ID != "" || link.Invalid || !result.Conflict && result.Underlier.Kind() == "" {
		plan.Underlier = &link.ID
	}

	return plan
}

func (c SourceConflictChange) Reason() reference.Reason {
	if c == RecordSourceConflict {
		return reference.SourceConflict
	}

	return ""
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
