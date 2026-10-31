// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package admin

import (
	"strings"

	"go.trippwill.dev/wake/internal/application"
)

func activityStatus(state application.ActivityState) string {
	parts := []string{string(state.Lifecycle())}
	if state.NeedsReview() {
		parts = append(parts, "needs review")
	}

	if state.Authority() == application.OperatorSelection {
		parts = append(parts, "operator decision")
	}

	return strings.Join(parts, "; ")
}

func correctionStatus(status application.CorrectionStatus) string {
	switch status {
	case application.CorrectionNeedsReview:
		return "needs review"
	case application.CorrectionUnresolved:
		return "endpoint unresolved; needs review"
	default:
		return string(status)
	}
}

func observationStatus(outcome application.ObservationOutcome) string {
	switch outcome {
	case application.ObservationUnselected:
		return "unselected account or source"
	case application.ObservationIdentityReview:
		return "identity needs review"
	case application.ObservationNativeReview:
		return "native identity needs review"
	case application.ObservationKindConflict:
		return "conflicting activity kind; needs review"
	case application.ObservationTermsConflict:
		return "conflicting terms; needs review"
	case application.ObservationCorroborated:
		return "corroborating observation"
	case application.ObservationAccepted:
		return "accepted"
	default:
		return string(outcome)
	}
}
