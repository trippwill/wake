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
		Option                    *optionData
		Debt                      *debtData
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

	if !i.Option().IsZero() {
		data.Option = optionWire(i.Option())
	}

	if !i.Debt().IsZero() {
		data.Debt = debtWire(i.Debt())
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

	debt, err := i.Debt.debt()
	if err != nil {
		return source.Instrument{}, err
	}

	option, err := i.Option.option()
	if err != nil {
		return source.Instrument{}, err
	}

	return source.NewInstrument(
		source.InstrumentInput{
			Reference:   ref,
			Kind:        i.Kind,
			Ticker:      i.Ticker,
			Name:        i.Name,
			Venue:       i.Venue,
			Identifiers: ids,
			Option:      option,
			Debt:        debt,
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
	QuotationBasis                                                   source.QuotationBasis
	AccruedInterest, NetWithBillable                                 moneyData
	Kind                                                             source.TradeKind
	Relationships                                                    []relationshipData
	Location                                                         string
	Account, Event                                                   refData
	Instrument                                                       instrumentData
	Side                                                             source.Side
	Quantity, Price, Currency, TradeDate, ExecutedAt, SettlementDate string
	Gross, Net                                                       moneyData
	Proceeds                                                         moneyData
	Order                                                            refData
	EventAliases                                                     []refData
	EventDistinct                                                    bool
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
		QuotationBasis:  f.QuotationBasis,
		AccruedInterest: moneyWire(f.AccruedInterest),
		NetWithBillable: moneyWire(f.NetWithBillable),
		Kind:            f.Kind,
		Relationships:   relationshipsWire(f.Relationships),
		Location:        f.Location,
		Account:         refWire(f.Account),
		Event:           refWire(f.Event),
		EventDistinct:   f.EventDistinct,
		Instrument:      instrumentWire(f.Instrument),
		Side:            f.Side,
		Quantity:        f.Quantity.Value(),
		Price:           f.Price.Amount(),
		Currency:        f.Price.Currency().String(),
		TradeDate:       f.TradeDate.String(),
		ExecutedAt:      f.ExecutedAt.String(),
		Gross:           moneyWire(f.Gross),
		Net:             moneyWire(f.Net),
		Proceeds:        moneyWire(f.Proceeds),
		Order:           refWire(f.Order),
		PositionEffect:  f.PositionEffect,
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

	accrued, err := wire.AccruedInterest.money()
	if err != nil {
		return source.Trade{}, err
	}

	billable, err := wire.NetWithBillable.money()
	if err != nil {
		return source.Trade{}, err
	}

	relationships, err := decodeRelationships(wire.Relationships)
	if err != nil {
		return source.Trade{}, err
	}

	return source.NewTrade(
		source.TradeInput{
			QuotationBasis: wire.QuotationBasis, AccruedInterest: accrued, NetWithBillable: billable,
			Location: wire.Location,
			Kind:     wire.Kind, Relationships: relationships,
			Account:        account,
			Event:          event,
			EventDistinct:  wire.EventDistinct,
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

type optionData struct {
	Underlier                         *instrumentData
	Right                             source.OptionRight
	Expiry, EffectiveFrom, Multiplier string
	Strike                            moneyData
	Deliverables                      []deliverableData
}
type deliverableData struct {
	Security refData
	Quantity string
	Cash     moneyData
}

func optionWire(o source.OptionTerms) *optionData {
	f := o.Fields()
	d := &optionData{Right: f.Right, Strike: moneyData{f.Strike.Amount(), ""}, Multiplier: f.Multiplier.Value()}
	if f.Strike.Amount() != "" {
		d.Strike.Currency = f.Strike.Currency().String()
	}

	if !f.Expiry.IsZero() {
		d.Expiry = f.Expiry.String()
	}

	if !f.EffectiveFrom.IsZero() {
		d.EffectiveFrom = f.EffectiveFrom.String()
	}

	if f.Underlier.Kind() != "" {
		u := instrumentWire(f.Underlier)
		d.Underlier = &u
	}

	for _, v := range f.Deliverables {
		d.Deliverables = append(
			d.Deliverables,
			deliverableData{refWire(v.Security()), v.Quantity().Value(), moneyWire(v.Cash())},
		)
	}

	return d
}

func (d *optionData) option() (source.OptionTerms, error) {
	if d == nil {
		return source.OptionTerms{}, nil
	}

	f := source.OptionInput{Right: d.Right}

	var err error
	if d.Underlier != nil {
		f.Underlier, err = d.Underlier.instrument()
		if err != nil {
			return source.OptionTerms{}, err
		}
	}

	if d.Expiry != "" {
		f.Expiry, err = financial.ParseDate(d.Expiry)
		if err != nil {
			return source.OptionTerms{}, err
		}
	}

	if d.EffectiveFrom != "" {
		f.EffectiveFrom, err = financial.ParseDate(d.EffectiveFrom)
		if err != nil {
			return source.OptionTerms{}, err
		}
	}

	if d.Multiplier != "" {
		f.Multiplier, err = financial.NewQuantity(d.Multiplier)
		if err != nil {
			return source.OptionTerms{}, err
		}
	}

	if d.Strike.Amount != "" {
		c, e := financial.ParseCurrency(d.Strike.Currency)
		if e != nil {
			return source.OptionTerms{}, e
		}

		f.Strike, err = financial.NewPrice(d.Strike.Amount, c)
		if err != nil {
			return source.OptionTerms{}, err
		}
	}

	for _, v := range d.Deliverables {
		var component source.Deliverable
		if v.Security != (refData{}) {
			r, e := v.Security.reference()
			if e != nil {
				return source.OptionTerms{}, e
			}

			q, e := financial.NewQuantity(v.Quantity)
			if e != nil {
				return source.OptionTerms{}, e
			}

			component, err = source.NewSecurityDeliverable(r, q)
		} else {
			m, e := v.Cash.money()
			if e != nil {
				return source.OptionTerms{}, e
			}

			component, err = source.NewCashDeliverable(m)
		}

		if err != nil {
			return source.OptionTerms{}, err
		}

		f.Deliverables = append(f.Deliverables, component)
	}

	return source.NewOptionTerms(f)
}

type relationshipData struct {
	Kind   source.RelationshipKind
	Target refData
}

func relationshipsWire(links []source.Relationship) []relationshipData {
	out := []relationshipData{}
	for _, r := range links {
		out = append(out, relationshipData{r.Kind(), refWire(r.Target())})
	}

	return out
}

func decodeRelationships(links []relationshipData) ([]source.Relationship, error) {
	out := []source.Relationship{}
	for _, r := range links {
		ref, err := r.Target.reference()
		if err != nil {
			return nil, err
		}

		v, err := source.NewRelationship(r.Kind, ref)
		if err != nil {
			return nil, err
		}

		out = append(out, v)
	}

	return out, nil
}

type lifecycleData struct {
	Trade tradeData
	Kind  source.LifecycleKind
}

func encodeLifecycle(l source.Lifecycle) ([]byte, error) {
	f := l.Fields()
	d := tradeData{
		Location:      f.Location,
		Account:       refWire(f.Account),
		Event:         refWire(f.Event),
		EventDistinct: f.EventDistinct,
		Order:         refWire(f.Order),
		Instrument:    instrumentWire(f.Instrument),
		Quantity:      f.Quantity.Value(),
		TradeDate:     f.Date.String(),
		ExecutedAt:    f.At.String(),
		Relationships: relationshipsWire(f.Relationships),
	}
	for _, r := range f.EventAliases {
		d.EventAliases = append(d.EventAliases, refWire(r))
	}

	return json.Marshal(lifecycleData{d, f.Kind})
}

func decodeLifecycle(data []byte) (source.Lifecycle, error) {
	var d lifecycleData
	if err := json.Unmarshal(data, &d); err != nil {
		return source.Lifecycle{}, err
	}

	f := source.LifecycleInput{Location: d.Trade.Location, Kind: d.Kind, EventDistinct: d.Trade.EventDistinct}

	var err error

	f.Account, err = d.Trade.Account.reference()
	if err != nil {
		return source.Lifecycle{}, err
	}

	f.Event, err = d.Trade.Event.reference()
	if err != nil {
		return source.Lifecycle{}, err
	}

	f.Order, err = d.Trade.Order.reference()
	if err != nil {
		return source.Lifecycle{}, err
	}

	f.Instrument, err = d.Trade.Instrument.instrument()
	if err != nil {
		return source.Lifecycle{}, err
	}

	f.Quantity, err = financial.NewQuantity(d.Trade.Quantity)
	if err != nil {
		return source.Lifecycle{}, err
	}

	f.Date, err = financial.ParseDate(d.Trade.TradeDate)
	if err != nil {
		return source.Lifecycle{}, err
	}

	if d.Trade.ExecutedAt != "" {
		f.At, err = financial.ParseTimestamp(d.Trade.ExecutedAt)
		if err != nil {
			return source.Lifecycle{}, err
		}
	}

	for _, r := range d.Trade.EventAliases {
		ref, e := r.reference()
		if e != nil {
			return source.Lifecycle{}, e
		}

		f.EventAliases = append(f.EventAliases, ref)
	}

	f.Relationships, err = decodeRelationships(d.Trade.Relationships)
	if err != nil {
		return source.Lifecycle{}, err
	}

	return source.NewLifecycle(f)
}

type debtData struct {
	Type                                             source.DebtType
	Currency, Maturity, IssueDate, DatedDate, Coupon string
	PaymentsPerYear                                  int
}

func debtWire(d source.DebtTerms) *debtData {
	f := d.Fields()
	out := &debtData{Type: f.Type, Coupon: f.Coupon.Value(), PaymentsPerYear: f.PaymentsPerYear}
	if f.Currency != (financial.Currency{}) {
		out.Currency = f.Currency.String()
	}

	if !f.Maturity.IsZero() {
		out.Maturity = f.Maturity.String()
	}

	if !f.IssueDate.IsZero() {
		out.IssueDate = f.IssueDate.String()
	}

	if !f.DatedDate.IsZero() {
		out.DatedDate = f.DatedDate.String()
	}

	return out
}

func (d *debtData) debt() (source.DebtTerms, error) {
	if d == nil {
		return source.DebtTerms{}, nil
	}

	f := source.DebtInput{Type: d.Type, PaymentsPerYear: d.PaymentsPerYear}

	var err error
	if d.Currency != "" {
		f.Currency, err = financial.ParseCurrency(d.Currency)
		if err != nil {
			return source.DebtTerms{}, err
		}
	}

	if d.Coupon != "" {
		f.Coupon, err = financial.NewQuantity(d.Coupon)
		if err != nil {
			return source.DebtTerms{}, err
		}
	}

	for _, v := range []struct {
		text   string
		target *financial.Date
	}{{d.Maturity, &f.Maturity}, {d.IssueDate, &f.IssueDate}, {d.DatedDate, &f.DatedDate}} {
		if v.text != "" {
			*v.target, err = financial.ParseDate(v.text)
			if err != nil {
				return source.DebtTerms{}, err
			}
		}
	}

	return source.NewDebtTerms(f)
}

type (
	currencyAmountData struct{ Currency, Amount string }
	conversionData     struct {
		Location                              string
		Account, Event, Order                 refData
		EventAliases                          []refData
		EventDistinct                         bool
		Date, At, RateBase                    string
		Rate                                  moneyData
		Sent, Received                        currencyAmountData
		Gross, Proceeds, Net, NetWithBillable moneyData
		Charges                               []chargeData
	}
)

func encodeConversion(c source.Conversion) ([]byte, error) {
	f := c.Fields()
	d := conversionData{
		Location:        f.Location,
		Account:         refWire(f.Account),
		Event:           refWire(f.Event),
		EventDistinct:   f.EventDistinct,
		Order:           refWire(f.Order),
		Date:            f.Date.String(),
		At:              f.At.String(),
		Sent:            currencyAmountData{f.Sent.Currency().String(), f.Sent.Amount()},
		Received:        currencyAmountData{f.Received.Currency().String(), f.Received.Amount()},
		Gross:           moneyWire(f.Gross),
		Proceeds:        moneyWire(f.Proceeds),
		Net:             moneyWire(f.Net),
		NetWithBillable: moneyWire(f.NetWithBillable),
	}
	if f.Rate.Amount() != "" {
		d.RateBase = f.RateBase.String()
		d.Rate = moneyData{f.Rate.Amount(), f.Rate.Currency().String()}
	}

	for _, r := range f.EventAliases {
		d.EventAliases = append(d.EventAliases, refWire(r))
	}

	for _, c := range f.Charges {
		d.Charges = append(d.Charges, chargeData{c.Field(), c.Scope(), moneyWire(c.Money())})
	}

	return json.Marshal(d)
}

func decodeConversion(data []byte) (source.Conversion, error) {
	var d conversionData
	if err := json.Unmarshal(data, &d); err != nil {
		return source.Conversion{}, err
	}

	f := source.ConversionInput{EventDistinct: d.EventDistinct, Location: d.Location}

	var err error
	for _, r := range []struct {
		v      refData
		target *source.Reference
	}{{d.Account, &f.Account}, {d.Event, &f.Event}, {d.Order, &f.Order}} {
		*r.target, err = r.v.reference()
		if err != nil {
			return source.Conversion{}, err
		}
	}

	f.Date, err = financial.ParseDate(d.Date)
	if err != nil {
		return source.Conversion{}, err
	}

	if d.At != "" {
		f.At, err = financial.ParseTimestamp(d.At)
		if err != nil {
			return source.Conversion{}, err
		}
	}

	for _, a := range []struct {
		v      currencyAmountData
		target *source.CurrencyAmount
	}{{d.Sent, &f.Sent}, {d.Received, &f.Received}} {
		c, e := financial.ParseCurrency(a.v.Currency)
		if e != nil {
			return source.Conversion{}, e
		}

		*a.target, err = source.NewCurrencyAmount(c, a.v.Amount)
		if err != nil {
			return source.Conversion{}, err
		}
	}

	if d.Rate.Amount != "" {
		f.RateBase, err = financial.ParseCurrency(d.RateBase)
		if err != nil {
			return source.Conversion{}, err
		}

		c, e := financial.ParseCurrency(d.Rate.Currency)
		if e != nil {
			return source.Conversion{}, e
		}

		f.Rate, err = financial.NewPrice(d.Rate.Amount, c)
		if err != nil {
			return source.Conversion{}, err
		}
	}

	for _, m := range []struct {
		v      moneyData
		target *financial.Money
	}{{d.Gross, &f.Gross}, {d.Proceeds, &f.Proceeds}, {d.Net, &f.Net}, {d.NetWithBillable, &f.NetWithBillable}} {
		*m.target, err = m.v.money()
		if err != nil {
			return source.Conversion{}, err
		}
	}

	for _, c := range d.Charges {
		m, e := c.Money.money()
		if e != nil {
			return source.Conversion{}, e
		}

		v, e := source.NewCharge(c.Field, c.Scope, m)
		if e != nil {
			return source.Conversion{}, e
		}

		f.Charges = append(f.Charges, v)
	}

	for _, r := range d.EventAliases {
		ref, e := r.reference()
		if e != nil {
			return source.Conversion{}, e
		}

		f.EventAliases = append(f.EventAliases, ref)
	}

	return source.NewConversion(f)
}

type correctionData struct {
	Location                     string
	Account, Target, Replacement refData
	Kind                         source.CorrectionKind
}

func encodeCorrection(c source.Correction) ([]byte, error) {
	f := c.Fields()

	return json.Marshal(
		correctionData{f.Location, refWire(f.Account), refWire(f.Target), refWire(f.Replacement), f.Kind},
	)
}

func decodeCorrection(data []byte) (source.Correction, error) {
	var d correctionData
	if err := json.Unmarshal(data, &d); err != nil {
		return source.Correction{}, err
	}

	f := source.CorrectionInput{Location: d.Location, Kind: d.Kind}

	var err error
	for _, r := range []struct {
		v      refData
		target *source.Reference
	}{{d.Account, &f.Account}, {d.Target, &f.Target}, {d.Replacement, &f.Replacement}} {
		*r.target, err = r.v.reference()
		if err != nil {
			return source.Correction{}, err
		}
	}

	return source.NewCorrection(f)
}
