// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package source

import (
	"slices"

	"go.trippwill.dev/wake/financial"
)

type OptionRight string

const (
	Put  OptionRight = "put"
	Call OptionRight = "call"
)

// Deliverable components are explicitly reported; a premium multiplier never
// establishes the securities or cash delivered by a contract.
type Deliverable struct {
	security Reference
	quantity financial.Quantity
	cash     financial.Money
}

func NewSecurityDeliverable(security Reference, quantity financial.Quantity) (Deliverable, error) {
	if security.IsZero() || !quantity.Positive() {
		return Deliverable{}, ErrInvalidInstrument
	}

	return Deliverable{security: security, quantity: quantity}, nil
}

func NewCashDeliverable(cash financial.Money) (Deliverable, error) {
	if cash.Amount() == "" {
		return Deliverable{}, ErrInvalidInstrument
	}

	return Deliverable{cash: cash}, nil
}
func (d Deliverable) Security() Reference          { return d.security }
func (d Deliverable) Quantity() financial.Quantity { return d.quantity }
func (d Deliverable) Cash() financial.Money        { return d.cash }

type OptionInput struct {
	Underlier     Instrument
	Right         OptionRight
	Expiry        financial.Date
	Strike        financial.Price
	Multiplier    financial.Quantity
	EffectiveFrom financial.Date
	Deliverables  []Deliverable
}
type OptionTerms struct{ input *OptionInput }

func NewOptionTerms(input OptionInput) (OptionTerms, error) {
	if input.Underlier.Kind() != "" && input.Underlier.Kind() != Equity && input.Underlier.Kind() != Fund &&
		input.Underlier.Kind() != UnknownInstrument {
		return OptionTerms{}, ErrInvalidInstrument
	}

	if input.Right != "" && input.Right != Put && input.Right != Call {
		return OptionTerms{}, ErrInvalidInstrument
	}

	if input.Multiplier.Value() != "" && !input.Multiplier.Positive() {
		return OptionTerms{}, ErrInvalidInstrument
	}

	if input.Strike.Amount() != "" && input.Strike.Amount()[0] == '-' {
		return OptionTerms{}, ErrInvalidInstrument
	}

	if len(input.Deliverables) > 32 {
		return OptionTerms{}, ErrInvalidInstrument
	}

	for _, d := range input.Deliverables {
		if d.security.IsZero() && d.cash.Amount() == "" {
			return OptionTerms{}, ErrInvalidInstrument
		}
	}

	input.Deliverables = slices.Clone(input.Deliverables)

	return OptionTerms{&input}, nil
}
func (o OptionTerms) IsZero() bool { return o.input == nil }
func (o OptionTerms) Fields() OptionInput {
	if o.input == nil {
		return OptionInput{}
	}

	f := *o.input
	f.Deliverables = slices.Clone(f.Deliverables)

	return f
}
