// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package application

type LifecycleState string

const (
	Accepted   LifecycleState = "accepted"
	Cancelled  LifecycleState = "cancelled"
	Superseded LifecycleState = "superseded"
)

type ReviewReasons uint8

const (
	ConflictingClaims ReviewReasons = 1 << iota
	AmbiguousCorrection
)

type SelectionAuthority string

const (
	AutomaticSelection SelectionAuthority = "automatic"
	OperatorSelection  SelectionAuthority = "operator"
)

// ActivityState keeps independent financial facts separate from their wording.
type ActivityState struct {
	lifecycle LifecycleState
	review    ReviewReasons
	authority SelectionAuthority
}

func NewActivityState(
	lifecycle LifecycleState,
	review ReviewReasons,
	authority SelectionAuthority,
) (ActivityState, error) {
	if lifecycle != Accepted && lifecycle != Cancelled && lifecycle != Superseded ||
		review & ^(ConflictingClaims|AmbiguousCorrection) != 0 ||
		authority != AutomaticSelection && authority != OperatorSelection {
		return ActivityState{}, ErrInvalid
	}

	return ActivityState{lifecycle, review, authority}, nil
}

func (s ActivityState) Lifecycle() LifecycleState     { return s.lifecycle }
func (s ActivityState) ReviewReasons() ReviewReasons  { return s.review }
func (s ActivityState) NeedsReview() bool             { return s.review != 0 }
func (s ActivityState) Authority() SelectionAuthority { return s.authority }

type CorrectionStatus string

const (
	CorrectionApplied     CorrectionStatus = "applied"
	CorrectionNeedsReview CorrectionStatus = "review"
	CorrectionUnresolved  CorrectionStatus = "unresolved"
)

func (s CorrectionStatus) NeedsReview() bool {
	return s == CorrectionNeedsReview || s == CorrectionUnresolved
}

type ObservationOutcome string

const (
	ObservationUnselected     ObservationOutcome = "unselected"
	ObservationIdentityReview ObservationOutcome = "identity-review"
	ObservationNativeReview   ObservationOutcome = "native-review"
	ObservationKindConflict   ObservationOutcome = "kind-conflict"
	ObservationTermsConflict  ObservationOutcome = "terms-conflict"
	ObservationCorroborated   ObservationOutcome = "corroborated"
	ObservationAccepted       ObservationOutcome = "accepted"
)

type ReferenceStatus string

func (s ReferenceStatus) NeedsReview() bool { return s == "conflict" || s == "ambiguous" }
func (s ReferenceStatus) Gap() bool         { return s != "resolved" && !s.NeedsReview() }

func (o ObservationOutcome) NeedsReview() bool {
	return o == ObservationIdentityReview || o == ObservationNativeReview || o == ObservationKindConflict ||
		o == ObservationTermsConflict
}
