// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package admin

import (
	"strings"

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/application/calculation"
)

func activityStatus(state calculation.ActivityState) string {
	parts := []string{string(state.Lifecycle())}
	if state.NeedsReview() {
		parts = append(parts, "needs review")
	}

	if state.Authority() == calculation.OperatorSelection {
		parts = append(parts, "operator decision")
	}

	return strings.Join(parts, "; ")
}

func correctionStatus(status calculation.CorrectionStatus) string {
	switch status {
	case calculation.CorrectionNeedsReview:
		return "needs review"
	case calculation.CorrectionUnresolved:
		return "endpoint unresolved; needs review"
	default:
		return string(status)
	}
}

func observationStatus(outcome calculation.ObservationOutcome) string {
	switch outcome {
	case calculation.ObservationUnselected:
		return "unselected account or source"
	case calculation.ObservationIncompleteConflict:
		return "incomplete activity terms conflict"
	case calculation.ObservationIncomplete:
		return "waiting for activity terms"
	case calculation.ObservationIdentityReview:
		return "identity needs review"
	case calculation.ObservationNativeReview:
		return "native identity needs review"
	case calculation.ObservationKindConflict:
		return "conflicting activity kind; needs review"
	case calculation.ObservationTermsConflict:
		return "conflicting terms; needs review"
	case calculation.ObservationNoContribution:
		return "no additional contribution"
	case calculation.ObservationEnriched:
		return "adds source information"
	case calculation.ObservationAccepted:
		return "accepted"
	default:
		return string(outcome)
	}
}

func instrumentStatus(instrument application.Instrument) string {
	label := string(instrument.Status())
	if instrument.Authority() == calculation.OperatorSelection {
		label += "; operator decision"
	}

	return label
}

func referenceTone(status calculation.ReferenceStatus) string {
	if status.NeedsReview() {
		return "error"
	}

	if status == "resolved" {
		return "success"
	}

	return "warning"
}
