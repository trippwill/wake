// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package source

import (
	"slices"

	"go.trippwill.dev/wake/financial"
)

type PositionEffect string

const (
	UnknownEffect PositionEffect = ""
	Opening       PositionEffect = "opening"
	Closing       PositionEffect = "closing"
	Mixed         PositionEffect = "mixed"
)

// Charge retains the reported monetary role and total/component qualification.
type Charge struct {
	field, scope string
	money        financial.Money
}

func NewCharge(field, scope string, money financial.Money) (Charge, error) {
	if !validText(field, 128) || (scope != "total" && scope != "component") || money.Amount() == "" {
		return Charge{}, ErrInvalidTrade
	}

	return Charge{field, scope, money}, nil
}
func (c Charge) Field() string          { return c.field }
func (c Charge) Scope() string          { return c.scope }
func (c Charge) Money() financial.Money { return c.money }

type Side string

const (
	Buy  Side = "buy"
	Sell Side = "sell"
)

// TradeInput is one reported equity execution. Native event identity and time
// can be absent; location, account, exact terms and civil trade date cannot.
type TradeInput struct {
	Location       string
	Account        Reference
	Instrument     Instrument
	Event          Reference
	EventAliases   []Reference
	Order          Reference
	PositionEffect PositionEffect
	Charges        []Charge
	Proceeds       financial.Money
	Side           Side
	Quantity       financial.Quantity
	Price          financial.Price
	TradeDate      financial.Date
	ExecutedAt     financial.Timestamp
	SettlementDate financial.Date
	Gross          financial.Money
	Net            financial.Money
}
type Trade struct{ input TradeInput }

func NewTrade(input TradeInput) (Trade, error) {
	if !validText(input.Location, 1024) || input.Account.IsZero() ||
		(input.Instrument.Kind() != Equity && input.Instrument.Kind() != Fund) ||
		(input.Side != Buy && input.Side != Sell) ||
		!input.Quantity.Positive() ||
		input.Price.Amount() == "" ||
		input.TradeDate.IsZero() {
		return Trade{}, ErrInvalidTrade
	}

	if len(input.EventAliases) > 16 || len(input.Charges) > 32 {
		return Trade{}, ErrInvalidTrade
	}

	for _, ref := range input.EventAliases {
		if ref.IsZero() {
			return Trade{}, ErrInvalidTrade
		}
	}

	for _, c := range input.Charges {
		if c.field == "" {
			return Trade{}, ErrInvalidTrade
		}
	}

	switch input.PositionEffect {
	case UnknownEffect, Opening, Closing, Mixed:
	default:
		return Trade{}, ErrInvalidTrade
	}

	input.EventAliases = slices.Clone(input.EventAliases)
	input.Charges = slices.Clone(input.Charges)

	return Trade{input}, nil
}

func (t Trade) Fields() TradeInput {
	f := t.input
	f.EventAliases = slices.Clone(f.EventAliases)
	f.Charges = slices.Clone(f.Charges)

	return f
}
