// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0
package source

// Incomplete instructions are retained review cases, never inferred cancellations.
type CorrectionKind string

const (
	Cancellation      CorrectionKind = "cancellation"
	Replacement       CorrectionKind = "replacement"
	UnclearCorrection CorrectionKind = "unclear"
)

type (
	CorrectionInput struct {
		Location                     string
		Account, Target, Replacement Reference
		Kind                         CorrectionKind
	}
	Correction struct{ input CorrectionInput }
)

func NewCorrection(f CorrectionInput) (Correction, error) {
	if !validText(f.Location, 1024) || f.Account.IsZero() {
		return Correction{}, ErrInvalidTrade
	}

	switch f.Kind {
	case Cancellation, Replacement, UnclearCorrection:
	default:
		return Correction{}, ErrInvalidTrade
	}

	if f.Kind == Cancellation && !f.Replacement.IsZero() {
		return Correction{}, ErrInvalidTrade
	}

	return Correction{f}, nil
}
func (c Correction) Fields() CorrectionInput { return c.input }
