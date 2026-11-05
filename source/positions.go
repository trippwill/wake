// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package source

import (
	"slices"

	"go.trippwill.dev/wake/financial"
)

const ErrInvalidPositions Error = "invalid reported position set"

// Position is a reported holding, independently of the events or lots explaining it.
type Position struct {
	instrument Instrument
	quantity   financial.Quantity
	costBasis  financial.Money
}

func NewPosition(
	instrument Instrument,
	quantity financial.Quantity,
	costBasis financial.Money,
) (Position, error) {
	if !slices.Contains([]InstrumentKind{Equity, Fund, Option, Debt}, instrument.Kind()) ||
		!quantity.Abs().Positive() {
		return Position{}, ErrInvalidPositions
	}

	return Position{instrument, quantity, costBasis}, nil
}

func (p Position) Instrument() Instrument       { return p.instrument }
func (p Position) Quantity() financial.Quantity { return p.quantity }
func (p Position) Unit() string                 { return quantityUnit(p.instrument.Kind()) }
func (p Position) CostBasis() financial.Money   { return p.costBasis }

type PositionSetInput struct {
	Location string
	Account  Reference
	// Reference and Supersedes express established source revision authority,
	// never precedence inferred from generation or retrieval time.
	Reference  Reference
	Supersedes []Reference
	Date       financial.Date
	At         financial.Timestamp
	Complete   bool
	// Reason is not_reported or incomplete for an unavailable set.
	Reason    string
	Positions []Position
}

type PositionSetObservation struct{ input PositionSetInput }

func NewPositionSetObservation(f PositionSetInput) (PositionSetObservation, error) {
	if !validText(f.Location, 1024) || f.Account.IsZero() || len(f.Positions) > 100000 ||
		len(f.Supersedes) > 1024 ||
		(f.Complete && (f.Date.IsZero() || f.Reason != "")) ||
		(!f.Complete && (len(f.Positions) != 0 || (f.Reason != "not_reported" && f.Reason != "incomplete"))) ||
		(f.Date.IsZero() && !f.At.IsZero()) {
		return PositionSetObservation{}, ErrInvalidPositions
	}

	for _, p := range f.Positions {
		if _, err := NewPosition(p.Instrument(), p.Quantity(), p.CostBasis()); err != nil {
			return PositionSetObservation{}, err
		}
	}

	for _, r := range f.Supersedes {
		if r.IsZero() || r == f.Reference || f.Reference.IsZero() {
			return PositionSetObservation{}, ErrInvalidPositions
		}
	}

	f.Positions = slices.Clone(f.Positions)
	f.Supersedes = slices.Clone(f.Supersedes)

	return PositionSetObservation{f}, nil
}

func (o PositionSetObservation) IsZero() bool { return o.input.Location == "" }
func (o PositionSetObservation) Fields() PositionSetInput {
	f := o.input
	f.Positions = slices.Clone(f.Positions)
	f.Supersedes = slices.Clone(f.Supersedes)

	return f
}
