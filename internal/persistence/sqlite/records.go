// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"encoding/json"

	"go.trippwill.dev/wake/financial"
	"go.trippwill.dev/wake/source"
)

type refData [4]string

func refWire(ref source.Reference) refData {
	return refData{ref.Authority(), ref.Kind(), ref.Scope(), ref.Value()}
}

func (r refData) reference() (source.Reference, error) {
	if r == (refData{}) {
		return source.Reference{}, nil
	}

	return source.NewReference(r[0], r[1], r[2], r[3])
}

type (
	identifierData struct {
		Kind         source.IdentifierKind
		Value, Field string
	}
	instrumentData struct {
		Reference                 refData
		Kind                      source.InstrumentKind
		Ticker, Name, Venue, AsOf string
		Identifiers               []identifierData
	}
)

func instrumentWire(i source.Instrument) instrumentData {
	data := instrumentData{
		Reference: refWire(i.Reference()),
		Kind:      i.Kind(),
		Ticker:    i.Ticker(),
		Name:      i.Name(),
		Venue:     i.Venue(),
	}
	for _, id := range i.Identifiers() {
		data.Identifiers = append(data.Identifiers, identifierData{id.Kind(), id.Value(), id.Field()})
	}

	return data
}

func (i instrumentData) instrument() (source.Instrument, error) {
	ref, err := i.Reference.reference()
	if err != nil {
		return source.Instrument{}, err
	}

	ids := []source.Identifier{}
	for _, id := range i.Identifiers {
		parsed, e := source.NewIdentifier(id.Kind, id.Value, id.Field)
		if e != nil {
			return source.Instrument{}, e
		}

		ids = append(ids, parsed)
	}

	return source.NewInstrument(
		source.InstrumentInput{
			Reference:   ref,
			Kind:        i.Kind,
			Ticker:      i.Ticker,
			Name:        i.Name,
			Venue:       i.Venue,
			Identifiers: ids,
		},
	)
}
func sourceDate(text string) financial.Date { date, _ := financial.ParseDate(text); return date }

type moneyData struct{ Amount, Currency string }

func moneyWire(m financial.Money) moneyData {
	if m.Amount() == "" {
		return moneyData{}
	}

	return moneyData{m.Amount(), m.Currency().String()}
}

func (m moneyData) money() (financial.Money, error) {
	if m == (moneyData{}) {
		return financial.Money{}, nil
	}

	c, err := financial.ParseCurrency(m.Currency)
	if err != nil {
		return financial.Money{}, err
	}

	return financial.NewMoney(m.Amount, c)
}

type tradeData struct {
	Location                                                         string
	Account, Event                                                   refData
	Instrument                                                       instrumentData
	Side                                                             source.Side
	Quantity, Price, Currency, TradeDate, ExecutedAt, SettlementDate string
	Gross, Net                                                       moneyData
	Proceeds                                                         moneyData
	Order                                                            refData
	EventAliases                                                     []refData
	PositionEffect                                                   source.PositionEffect
	Charges                                                          []chargeData
}
type chargeData struct {
	Field, Scope string
	Money        moneyData
}

func encodeTrade(t source.Trade) ([]byte, error) {
	f := t.Fields()
	data := tradeData{
		Location:       f.Location,
		Account:        refWire(f.Account),
		Event:          refWire(f.Event),
		Instrument:     instrumentWire(f.Instrument),
		Side:           f.Side,
		Quantity:       f.Quantity.Value(),
		Price:          f.Price.Amount(),
		Currency:       f.Price.Currency().String(),
		TradeDate:      f.TradeDate.String(),
		ExecutedAt:     f.ExecutedAt.String(),
		Gross:          moneyWire(f.Gross),
		Net:            moneyWire(f.Net),
		Proceeds:       moneyWire(f.Proceeds),
		Order:          refWire(f.Order),
		PositionEffect: f.PositionEffect,
	}
	for _, r := range f.EventAliases {
		data.EventAliases = append(data.EventAliases, refWire(r))
	}

	for _, c := range f.Charges {
		data.Charges = append(data.Charges, chargeData{c.Field(), c.Scope(), moneyWire(c.Money())})
	}

	if !f.SettlementDate.IsZero() {
		data.SettlementDate = f.SettlementDate.String()
	}

	return json.Marshal(data)
}

func decodeTrade(data []byte) (source.Trade, error) {
	var wire tradeData
	if err := json.Unmarshal(data, &wire); err != nil {
		return source.Trade{}, err
	}

	account, err := wire.Account.reference()
	if err != nil {
		return source.Trade{}, err
	}

	event, err := wire.Event.reference()
	if err != nil {
		return source.Trade{}, err
	}

	instrument, err := wire.Instrument.instrument()
	if err != nil {
		return source.Trade{}, err
	}

	quantity, err := financial.NewQuantity(wire.Quantity)
	if err != nil {
		return source.Trade{}, err
	}

	currency, err := financial.ParseCurrency(wire.Currency)
	if err != nil {
		return source.Trade{}, err
	}

	price, err := financial.NewPrice(wire.Price, currency)
	if err != nil {
		return source.Trade{}, err
	}

	date, err := financial.ParseDate(wire.TradeDate)
	if err != nil {
		return source.Trade{}, err
	}

	at := financial.Timestamp{}
	if wire.ExecutedAt != "" {
		at, err = financial.ParseTimestamp(wire.ExecutedAt)
		if err != nil {
			return source.Trade{}, err
		}
	}

	settlement := financial.Date{}
	if wire.SettlementDate != "" {
		settlement, err = financial.ParseDate(wire.SettlementDate)
		if err != nil {
			return source.Trade{}, err
		}
	}

	gross, err := wire.Gross.money()
	if err != nil {
		return source.Trade{}, err
	}

	net, err := wire.Net.money()
	if err != nil {
		return source.Trade{}, err
	}

	proceeds, err := wire.Proceeds.money()
	if err != nil {
		return source.Trade{}, err
	}

	order, err := wire.Order.reference()
	if err != nil {
		return source.Trade{}, err
	}

	aliases := []source.Reference{}
	for _, r := range wire.EventAliases {
		ref, e := r.reference()
		if e != nil {
			return source.Trade{}, e
		}

		aliases = append(aliases, ref)
	}

	charges := []source.Charge{}
	for _, c := range wire.Charges {
		money, e := c.Money.money()
		if e != nil {
			return source.Trade{}, e
		}

		charge, e := source.NewCharge(c.Field, c.Scope, money)
		if e != nil {
			return source.Trade{}, e
		}

		charges = append(charges, charge)
	}

	return source.NewTrade(
		source.TradeInput{
			Location:       wire.Location,
			Account:        account,
			Event:          event,
			Instrument:     instrument,
			Side:           wire.Side,
			Quantity:       quantity,
			Price:          price,
			TradeDate:      date,
			ExecutedAt:     at,
			SettlementDate: settlement,
			Gross:          gross,
			Net:            net,
			Proceeds:       proceeds,
			Order:          order,
			EventAliases:   aliases,
			PositionEffect: wire.PositionEffect,
			Charges:        charges,
		},
	)
}
