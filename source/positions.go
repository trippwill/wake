// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package source

import (
	"slices"

	"go.trippwill.dev/wake/financial"
)

const ErrInvalidPositions Error = "invalid reported position set"

type PositionTimeBasis string

// The zero basis denotes broker-effective time.
const (
	PositionSourceFetched   PositionTimeBasis = "source_fetched"
	PositionBrokerEffective PositionTimeBasis = "broker_effective"
)

// Position is a reported holding, independently of the events or lots explaining it.
type Position struct {
	instrument    Instrument
	quantity      financial.Quantity
	unitCostBasis financial.Price
	reportedUnits bool
}

func NewPosition(
	instrument Instrument,
	quantity financial.Quantity,
	unitCostBasis financial.Price,
) (Position, error) {
	if !slices.Contains([]InstrumentKind{Equity, Fund, Option, Debt, UnknownInstrument}, instrument.Kind()) ||
		!quantity.Abs().Positive() {
		return Position{}, ErrInvalidPositions
	}

	return Position{instrument: instrument, quantity: quantity, unitCostBasis: unitCostBasis}, nil
}

// WithReportedUnits preserves a debt quantity whose face amount is unspecified.
func (p Position) WithReportedUnits() (Position, error) {
	if p.instrument.Kind() != Debt {
		return Position{}, ErrInvalidPositions
	}

	p.reportedUnits = true

	return p, nil
}

// WithTerms enriches a holding while preserving its reported quantity and unit.
func (p Position) WithTerms(instrument Instrument, unitCostBasis financial.Price) (Position, error) {
	if instrument.Kind() != p.instrument.Kind() {
		return Position{}, ErrInvalidPositions
	}

	next, err := NewPosition(instrument, p.quantity, unitCostBasis)
	if err != nil {
		return Position{}, err
	}

	next.reportedUnits = p.reportedUnits

	return next, nil
}

func (p Position) UnitCostBasis() financial.Price { return p.unitCostBasis }

// UnitCostBasisQuotationBasis identifies the quote convention where established.
func (p Position) UnitCostBasisQuotationBasis() QuotationBasis {
	switch p.instrument.Kind() {
	case Equity, Fund, Option:
		return quotationBasis(p.instrument.Kind())
	default:
		return ""
	}
}

func (p Position) Instrument() Instrument       { return p.instrument }
func (p Position) Quantity() financial.Quantity { return p.quantity }
func (p Position) Unit() string {
	if p.reportedUnits || p.instrument.Kind() == UnknownInstrument {
		return "units"
	}

	return quantityUnit(p.instrument.Kind())
}

type PositionSetInput struct {
	Location string
	Account  Reference
	// Reference and Supersedes express established source revision authority,
	// never precedence inferred from generation or retrieval time.
	Reference  Reference
	Supersedes []Reference
	Date       financial.Date
	At         financial.Timestamp
	TimeBasis  PositionTimeBasis
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
		(f.Date.IsZero() && !f.At.IsZero()) ||
		(f.TimeBasis != "" && f.TimeBasis != PositionBrokerEffective && (f.TimeBasis != PositionSourceFetched || f.At.IsZero())) {
		return PositionSetObservation{}, ErrInvalidPositions
	}

	for _, p := range f.Positions {
		if _, err := NewPosition(p.Instrument(), p.Quantity(), p.UnitCostBasis()); err != nil {
			return PositionSetObservation{}, err
		}
	}

	for _, r := range f.Supersedes {
		if r.IsZero() || r == f.Reference || f.Reference.IsZero() {
			return PositionSetObservation{}, ErrInvalidPositions
		}
	}

	if f.TimeBasis == PositionBrokerEffective {
		f.TimeBasis = ""
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
