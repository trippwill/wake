// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package source

import "slices"

type InstrumentKind string

const (
	UnknownInstrument InstrumentKind = "unknown"
	Equity            InstrumentKind = "equity"
	Fund              InstrumentKind = "fund"
	Option            InstrumentKind = "option"
	Debt              InstrumentKind = "debt"
)

// InstrumentInput carries reported labels and identifiers, never accepted Wake
// financial identities. Reported labels and identifiers do not establish accepted financial identity.
type InstrumentInput struct {
	Reference   Reference
	Kind        InstrumentKind
	Ticker      string
	Name        string
	Venue       string
	Identifiers []Identifier
	Option      OptionTerms
	Debt        DebtTerms
}

type Instrument struct{ input InstrumentInput }

func NewInstrument(input InstrumentInput) (Instrument, error) {
	switch input.Kind {
	case UnknownInstrument, Equity, Fund, Option, Debt:
	default:
		return Instrument{}, ErrInvalidInstrument
	}

	if (input.Ticker != "" && !validText(input.Ticker, 256)) ||
		(input.Venue != "" && !validText(input.Venue, 256)) ||
		(input.Name != "" && !validText(input.Name, 1024)) || len(input.Identifiers) > 64 {
		return Instrument{}, ErrInvalidInstrument
	}

	if input.Reference.IsZero() && len(input.Identifiers) == 0 && input.Ticker == "" && input.Name == "" {
		return Instrument{}, ErrInvalidInstrument
	}

	if slices.Contains(input.Identifiers, (Identifier{})) {
		return Instrument{}, ErrInvalidIdentifier
	}

	if (input.Kind != Option && !input.Option.IsZero()) || (input.Kind != Debt && !input.Debt.IsZero()) {
		return Instrument{}, ErrInvalidInstrument
	}

	input.Identifiers = slices.Clone(input.Identifiers)

	return Instrument{input: input}, nil
}

func (i Instrument) Reference() Reference      { return i.input.Reference }
func (i Instrument) Kind() InstrumentKind      { return i.input.Kind }
func (i Instrument) Ticker() string            { return i.input.Ticker }
func (i Instrument) Name() string              { return i.input.Name }
func (i Instrument) Venue() string             { return i.input.Venue }
func (i Instrument) Identifiers() []Identifier { return slices.Clone(i.input.Identifiers) }

func (i Instrument) Option() OptionTerms { return i.input.Option }

func (i Instrument) Fields() InstrumentInput {
	f := i.input
	f.Identifiers = slices.Clone(f.Identifiers)

	return f
}

func (i Instrument) Debt() DebtTerms { return i.input.Debt }
