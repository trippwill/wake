// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package source

// activityInputs materializes only reported known fields. The strict term
// constructors below remain the completeness and coupled-invariant boundary.
func (o ActivityObservation) activityInputs() (string, TradeInput, LifecycleInput, ConversionInput, error) {
	subject := o.Fields()
	fields := subject.Fields

	t := TradeInput{
		Location:      subject.Location,
		Account:       subject.Account,
		Event:         subject.Event,
		EventAliases:  subject.EventAliases,
		EventDistinct: subject.EventDistinct,
	}

	l := LifecycleInput{
		Location:      subject.Location,
		Account:       subject.Account,
		Event:         subject.Event,
		EventAliases:  subject.EventAliases,
		EventDistinct: subject.EventDistinct,
	}
	c := ConversionInput{
		Location:      subject.Location,
		Account:       subject.Account,
		Event:         subject.Event,
		EventAliases:  subject.EventAliases,
		EventDistinct: subject.EventDistinct,
	}
	family := ""
	for _, field := range fields {
		if !field.Known() {
			continue
		}

		f := field.Fields()
		switch f.Name {
		case FieldFamily:
			family = f.Text
		case FieldTradeKind:
			t.Kind = TradeKind(f.Text)
		case FieldKind:
			l.Kind = LifecycleKind(f.Text)
		case FieldInstrument:
			t.Instrument, l.Instrument = f.Instrument, f.Instrument
		case FieldSide:
			t.Side = Side(f.Text)
		case FieldQuantity:
			t.Quantity, l.Quantity = f.Quantity, f.Quantity
		case FieldPrice:
			t.Price = f.Price
		case FieldTradeDate:
			t.TradeDate, l.Date, c.Date = f.Date, f.Date, f.Date
		case FieldExecutedAt:
			t.ExecutedAt, l.At, c.At = f.Timestamp, f.Timestamp, f.Timestamp
		case FieldSettlementDate:
			t.SettlementDate = f.Date
		case FieldOrder:
			t.Order, l.Order, c.Order = f.Reference, f.Reference, f.Reference
		case FieldPositionEffect:
			t.PositionEffect = PositionEffect(f.Text)
		case FieldGross:
			t.Gross, c.Gross = f.Money, f.Money
		case FieldProceeds:
			t.Proceeds, c.Proceeds = f.Money, f.Money
		case FieldNet:
			t.Net, c.Net = f.Money, f.Money
		case FieldNetBillable:
			t.NetWithBillable, c.NetWithBillable = f.Money, f.Money
		case FieldAccruedInterest:
			t.AccruedInterest = f.Money
		case FieldRelationship:
			t.Relationships = append(t.Relationships, f.Relationship)
			l.Relationships = append(l.Relationships, f.Relationship)
		case FieldSent:
			c.Sent = f.CurrencyAmount
		case FieldReceived:
			c.Received = f.CurrencyAmount
		case FieldRate:
			c.Rate = f.Price
		case FieldRateBase:
			c.RateBase = f.Currency
		}
	}

	charges, err := ChargesFromFields(fields)
	t.Charges, c.Charges = charges, charges

	return family, t, l, c, err
}

func (o ActivityObservation) Trade() (Trade, error) {
	family, t, _, _, err := o.activityInputs()
	if err != nil {
		return Trade{}, err
	}

	kind, present := o.Field(FieldTradeKind)
	if !present || !kind.Known() {
		return Trade{}, ErrInvalidActivity
	}

	if family != "trade" {
		return Trade{}, ErrInvalidActivity
	}

	v, err := NewTrade(t)
	if err != nil {
		return Trade{}, err
	}

	if f, ok := o.Field(FieldQuantity); ok && f.Known() && f.Fields().QuantityUnit != v.QuantityUnit() {
		return Trade{}, ErrInvalidActivity
	}

	if f, ok := o.Field(
		FieldPrice,
	); ok && f.Known() && f.Fields().PriceBasis != "" &&
		f.Fields().PriceBasis != v.PriceBasis() {
		return Trade{}, ErrInvalidActivity
	}

	return v, nil
}

func (o ActivityObservation) Lifecycle() (Lifecycle, error) {
	family, _, l, _, err := o.activityInputs()
	if err != nil {
		return Lifecycle{}, err
	}

	if family != "lifecycle" {
		return Lifecycle{}, ErrInvalidActivity
	}

	v, err := NewLifecycle(l)
	if err != nil {
		return Lifecycle{}, err
	}

	if f, ok := o.Field(FieldQuantity); ok && f.Known() && f.Fields().QuantityUnit != "contracts" {
		return Lifecycle{}, ErrInvalidActivity
	}

	return v, nil
}

func (o ActivityObservation) Conversion() (Conversion, error) {
	family, _, _, c, err := o.activityInputs()
	if err != nil {
		return Conversion{}, err
	}

	if family != "conversion" {
		return Conversion{}, ErrInvalidActivity
	}

	return NewConversion(c)
}
