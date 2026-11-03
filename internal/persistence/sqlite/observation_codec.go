// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"encoding/json"

	"go.trippwill.dev/wake/financial"
	"go.trippwill.dev/wake/source"
)

type activityFieldData struct {
	Name                                                                source.ActivityFieldName
	Unknown                                                             bool
	ReportedOn                                                          string
	Text, Quantity, Date, Timestamp, Currency, QuantityUnit, PriceBasis string
	Money, Price                                                        moneyData
	Instrument                                                          *instrumentData
	Reference                                                           refData
	CurrencyAmount                                                      *currencyAmountData
	Charge                                                              *chargeData
	Relationship                                                        *relationshipData
}
type observationData struct {
	Location       string
	Account, Event refData
	EventAliases   []refData
	EventDistinct  bool
	Fields         []activityFieldData
}

func optionalSourceDate(value financial.Date) string {
	if value.IsZero() {
		return ""
	}

	return value.String()
}

func activityFieldWire(value source.ActivityField) activityFieldData {
	f := value.Fields()
	d := activityFieldData{
		Name:         f.Name,
		Unknown:      f.Unknown,
		ReportedOn:   optionalSourceDate(f.ReportedOn),
		Text:         f.Text,
		QuantityUnit: f.QuantityUnit,
		PriceBasis:   f.PriceBasis,
		Money:        moneyWire(f.Money),
		Quantity:     f.Quantity.Value(),
		Date:         optionalSourceDate(f.Date),
		Timestamp:    f.Timestamp.String(),
		Reference:    refWire(f.Reference),
	}
	if f.Price.Amount() != "" {
		d.Price = moneyData{f.Price.Amount(), f.Price.Currency().String()}
	}

	if f.Instrument.Kind() != "" {
		i := instrumentWire(f.Instrument)
		d.Instrument = &i
	}

	if f.Currency != (financial.Currency{}) {
		d.Currency = f.Currency.String()
	}

	if f.CurrencyAmount.Currency() != (financial.Currency{}) {
		d.CurrencyAmount = &currencyAmountData{
			Currency: f.CurrencyAmount.Currency().String(),
			Amount:   f.CurrencyAmount.Amount(),
		}
	}

	if f.Charge.Field() != "" {
		d.Charge = &chargeData{f.Charge.Field(), f.Charge.Scope(), moneyWire(f.Charge.Money())}
	}

	if !f.Relationship.Target().IsZero() {
		d.Relationship = &relationshipData{Kind: f.Relationship.Kind(), Target: refWire(f.Relationship.Target())}
	}

	return d
}

func (d activityFieldData) field() (source.ActivityField, error) {
	f := source.ActivityFieldInput{
		Name:         d.Name,
		Unknown:      d.Unknown,
		Text:         d.Text,
		QuantityUnit: d.QuantityUnit,
		PriceBasis:   d.PriceBasis,
	}

	var err error
	if d.ReportedOn != "" {
		f.ReportedOn, err = financial.ParseDate(d.ReportedOn)
		if err != nil {
			return source.ActivityField{}, err
		}
	}

	f.Money, err = d.Money.money()
	if err != nil {
		return source.ActivityField{}, err
	}

	if d.Price != (moneyData{}) {
		c, e := financial.ParseCurrency(d.Price.Currency)
		if e != nil {
			return source.ActivityField{}, e
		}

		f.Price, err = financial.NewPrice(d.Price.Amount, c)
		if err != nil {
			return source.ActivityField{}, err
		}
	}

	if d.Quantity != "" {
		f.Quantity, err = financial.NewQuantity(d.Quantity)
		if err != nil {
			return source.ActivityField{}, err
		}
	}

	if d.Date != "" {
		f.Date, err = financial.ParseDate(d.Date)
		if err != nil {
			return source.ActivityField{}, err
		}
	}

	if d.Timestamp != "" {
		f.Timestamp, err = financial.ParseTimestamp(d.Timestamp)
		if err != nil {
			return source.ActivityField{}, err
		}
	}

	if d.Instrument != nil {
		f.Instrument, err = d.Instrument.instrument()
		if err != nil {
			return source.ActivityField{}, err
		}
	}

	f.Reference, err = d.Reference.reference()
	if err != nil {
		return source.ActivityField{}, err
	}

	if d.Currency != "" {
		f.Currency, err = financial.ParseCurrency(d.Currency)
		if err != nil {
			return source.ActivityField{}, err
		}
	}

	if d.CurrencyAmount != nil {
		c, e := financial.ParseCurrency(d.CurrencyAmount.Currency)
		if e != nil {
			return source.ActivityField{}, e
		}

		f.CurrencyAmount, err = source.NewCurrencyAmount(c, d.CurrencyAmount.Amount)
		if err != nil {
			return source.ActivityField{}, err
		}
	}

	if d.Charge != nil {
		m, e := d.Charge.Money.money()
		if e != nil {
			return source.ActivityField{}, e
		}

		f.Charge, err = source.NewCharge(d.Charge.Field, d.Charge.Scope, m)
		if err != nil {
			return source.ActivityField{}, err
		}
	}

	if d.Relationship != nil {
		r, e := d.Relationship.Target.reference()
		if e != nil {
			return source.ActivityField{}, e
		}

		f.Relationship, err = source.NewRelationship(d.Relationship.Kind, r)
		if err != nil {
			return source.ActivityField{}, err
		}
	}

	return source.NewActivityField(f)
}

func encodeObservation(o source.ActivityObservation) ([]byte, error) {
	f := o.Fields()
	d := observationData{
		Location:      f.Location,
		Account:       refWire(f.Account),
		Event:         refWire(f.Event),
		EventDistinct: f.EventDistinct,
	}
	for _, r := range f.EventAliases {
		d.EventAliases = append(d.EventAliases, refWire(r))
	}

	for _, field := range f.Fields {
		d.Fields = append(d.Fields, activityFieldWire(field))
	}

	return json.Marshal(d)
}

func decodeObservation(data []byte) (source.ActivityObservation, error) {
	var d observationData
	if json.Unmarshal(data, &d) != nil {
		return source.ActivityObservation{}, ErrCorrupt
	}

	f := source.ActivityObservationInput{Location: d.Location, EventDistinct: d.EventDistinct}

	var err error

	f.Account, err = d.Account.reference()
	if err != nil {
		return source.ActivityObservation{}, err
	}

	f.Event, err = d.Event.reference()
	if err != nil {
		return source.ActivityObservation{}, err
	}

	for _, r := range d.EventAliases {
		ref, e := r.reference()
		if e != nil {
			return source.ActivityObservation{}, e
		}

		f.EventAliases = append(f.EventAliases, ref)
	}

	for _, value := range d.Fields {
		field, e := value.field()
		if e != nil {
			return source.ActivityObservation{}, e
		}

		f.Fields = append(f.Fields, field)
	}

	return source.NewActivityObservation(f)
}
