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

type TradeKind string

const (
	ExchangeExecution TradeKind = "execution"
	StockBooking      TradeKind = "stock-booking"
)

type Side string

const (
	Buy  Side = "buy"
	Sell Side = "sell"
)

// TradeInput is one reported security execution. Native event identity and time
// can be absent; location, account, exact terms and civil trade date cannot.
type TradeInput struct {
	QuotationBasis                   QuotationBasis
	AccruedInterest, NetWithBillable financial.Money
	Location                         string
	Kind                             TradeKind
	Relationships                    []Relationship
	Account                          Reference
	Instrument                       Instrument
	Event                            Reference
	EventAliases                     []Reference
	// EventDistinct states that Event identifies one event within its qualified scope.
	// Its native kind and value remain opaque to the core.
	EventDistinct  bool
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
	switch input.QuotationBasis {
	case UnknownQuotation, Per100Nominal, PerNominalUnit:
	default:
		return Trade{}, ErrInvalidTrade
	}

	if input.Instrument.Kind() != Debt && input.QuotationBasis != "" {
		return Trade{}, ErrInvalidTrade
	}

	if input.Kind == "" {
		input.Kind = ExchangeExecution
	}

	if (input.Kind != ExchangeExecution && input.Kind != StockBooking) ||
		(input.Kind == StockBooking && input.Instrument.Kind() != Equity && input.Instrument.Kind() != Fund) ||
		!validRelationships(input.Relationships) {
		return Trade{}, ErrInvalidTrade
	}

	if !validText(input.Location, 1024) || input.Account.IsZero() ||
		(input.EventDistinct && input.Event.IsZero()) ||
		(input.Instrument.Kind() != Equity && input.Instrument.Kind() != Fund && input.Instrument.Kind() != Option && input.Instrument.Kind() != Debt) ||
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
	input.Relationships = slices.Clone(input.Relationships)

	return Trade{input}, nil
}

func (t Trade) Fields() TradeInput {
	f := t.input
	f.EventAliases = slices.Clone(f.EventAliases)
	f.Charges = slices.Clone(f.Charges)
	f.Relationships = slices.Clone(f.Relationships)

	return f
}

func (t Trade) QuantityUnit() string {
	if t.input.Instrument.Kind() == Option {
		return "contracts"
	}

	if t.input.Instrument.Kind() == Debt {
		return "nominal"
	}

	return "shares"
}

func (t Trade) PriceBasis() string {
	if t.input.Instrument.Kind() == Option {
		return "premium per underlying unit"
	}

	if t.input.Instrument.Kind() == Debt {
		if t.input.QuotationBasis == "" {
			return "quotation basis unknown"
		}

		return string(t.input.QuotationBasis)
	}

	return "per share"
}
