// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0
package source

import (
	"slices"

	"go.trippwill.dev/wake/financial"
)

// CurrencyAmount distinguishes a known currency with an unknown amount from a
// missing currency. Amounts are positive magnitudes in their explicit role.
type CurrencyAmount struct {
	currency financial.Currency
	amount   financial.Quantity
}

func NewCurrencyAmount(c financial.Currency, amount string) (CurrencyAmount, error) {
	if c == (financial.Currency{}) {
		return CurrencyAmount{}, ErrInvalidTrade
	}

	q := financial.Quantity{}

	var err error
	if amount != "" {
		q, err = financial.NewQuantity(amount)
		if err != nil || q.Value()[0] == '-' {
			return CurrencyAmount{}, ErrInvalidTrade
		}
	}

	return CurrencyAmount{c, q}, nil
}
func (a CurrencyAmount) Currency() financial.Currency { return a.currency }
func (a CurrencyAmount) Amount() string               { return a.amount.Value() }

type ConversionInput struct {
	Location                              string
	Account, Event, Order                 Reference
	EventAliases                          []Reference
	Date                                  financial.Date
	At                                    financial.Timestamp
	Sent, Received                        CurrencyAmount
	RateBase                              financial.Currency
	Rate                                  financial.Price
	Gross, Proceeds, Net, NetWithBillable financial.Money
	Charges                               []Charge
}
type Conversion struct{ input ConversionInput }

func NewConversion(f ConversionInput) (Conversion, error) {
	if !validText(f.Location, 1024) || f.Account.IsZero() || f.Date.IsZero() ||
		f.Sent.currency == (financial.Currency{}) ||
		f.Received.currency == (financial.Currency{}) ||
		f.Sent.currency == f.Received.currency ||
		len(f.EventAliases) > 16 ||
		len(f.Charges) > 32 {
		return Conversion{}, ErrInvalidTrade
	}

	if f.Rate.Amount() != "" {
		q, err := financial.NewQuantity(f.Rate.Amount())
		direct := f.RateBase == f.Sent.currency && f.Rate.Currency() == f.Received.currency
		inverse := f.RateBase == f.Received.currency && f.Rate.Currency() == f.Sent.currency
		if err != nil || !q.Positive() || f.RateBase == (financial.Currency{}) ||
			f.RateBase == f.Rate.Currency() ||
			(!direct && !inverse) {
			return Conversion{}, ErrInvalidTrade
		}
	} else if f.RateBase != (financial.Currency{}) {
		return Conversion{}, ErrInvalidTrade
	}

	for _, r := range f.EventAliases {
		if r.IsZero() {
			return Conversion{}, ErrInvalidTrade
		}
	}

	for _, c := range f.Charges {
		if c.field == "" {
			return Conversion{}, ErrInvalidTrade
		}
	}

	f.EventAliases = slices.Clone(f.EventAliases)
	f.Charges = slices.Clone(f.Charges)

	return Conversion{f}, nil
}

func (c Conversion) Fields() ConversionInput {
	f := c.input
	f.EventAliases = slices.Clone(f.EventAliases)
	f.Charges = slices.Clone(f.Charges)

	return f
}
