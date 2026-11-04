// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package source

import (
	"slices"

	"go.trippwill.dev/wake/financial"
)

type TransferDirection string

const (
	TransferIn  TransferDirection = "in"
	TransferOut TransferDirection = "out"
)

// SecurityTransferInput describes a movement, not a purchase or a lot allocation.
type SecurityTransferInput struct {
	Location                                                                 string
	Account, Event                                                           Reference
	EventAliases                                                             []Reference
	EventDistinct                                                            bool
	Instrument                                                               Instrument
	Direction                                                                TransferDirection
	Quantity                                                                 financial.Quantity
	Date, ReportDate, SettlementDate                                         financial.Date
	At                                                                       financial.Timestamp
	Method, Company, CounterpartyAccount, CounterpartyName, DeliveringBroker string
	TransferPrice                                                            financial.Price
	PositionValue, ReportedCost, ReportedPnL                                 financial.Money
	Relationships                                                            []Relationship
}

type SecurityTransfer struct{ input SecurityTransferInput }

func NewSecurityTransfer(f SecurityTransferInput) (SecurityTransfer, error) {
	if !f.Quantity.Positive() || f.Instrument.Kind() == "" || f.Instrument.Kind() == UnknownInstrument ||
		(f.Direction != TransferIn && f.Direction != TransferOut) || f.Date.IsZero() ||
		!validRelationships(f.Relationships) {
		return SecurityTransfer{}, ErrInvalidActivity
	}

	f.EventAliases = slices.Clone(f.EventAliases)
	f.Relationships = slices.Clone(f.Relationships)

	v := SecurityTransfer{f}
	if v.Observation().IsZero() {
		return SecurityTransfer{}, ErrInvalidActivity
	}

	return v, nil
}

func (v SecurityTransfer) Fields() SecurityTransferInput {
	f := v.input
	f.EventAliases = slices.Clone(f.EventAliases)
	f.Relationships = slices.Clone(f.Relationships)

	return f
}

func (v SecurityTransfer) Observation() ActivityObservation {
	f := v.input
	fields := []ActivityFieldInput{
		{Name: FieldFamily, Text: "security-transfer"},
		{Name: FieldInstrument, Instrument: f.Instrument},
		{Name: FieldDirection, Text: string(f.Direction)},
		{Name: FieldQuantity, Quantity: f.Quantity, QuantityUnit: quantityUnit(f.Instrument.Kind())},
		{Name: FieldTradeDate, Date: f.Date},
		{Name: FieldReportDate, Date: f.ReportDate},
		{Name: FieldSettlementDate, Date: f.SettlementDate},
		{Name: FieldExecutedAt, Timestamp: f.At},
		{Name: FieldTransferMethod, Text: f.Method},
		{Name: FieldTransferCompany, Text: f.Company},
		{Name: FieldDeliveringBroker, Text: f.DeliveringBroker},
		{Name: FieldCounterpartyAccount, Text: f.CounterpartyAccount},
		{Name: FieldCounterpartyName, Text: f.CounterpartyName},
		{Name: FieldTransferPrice, Price: f.TransferPrice},
		{Name: FieldPositionValue, Money: f.PositionValue},
		{Name: FieldTransferCost, Money: f.ReportedCost},
		{Name: FieldTransferPnL, Money: f.ReportedPnL},
	}
	for _, r := range f.Relationships {
		fields = append(fields, ActivityFieldInput{Name: FieldRelationship, Relationship: r})
	}

	return completeObservation(f.Location, f.Account, f.Event, f.EventAliases, f.EventDistinct, fields)
}

func (o ActivityObservation) SecurityTransfer() (SecurityTransfer, error) {
	s := o.Fields()
	f := SecurityTransferInput{
		Location:      s.Location,
		Account:       s.Account,
		Event:         s.Event,
		EventAliases:  s.EventAliases,
		EventDistinct: s.EventDistinct,
	}
	family := ""
	for _, value := range s.Fields {
		if !value.Known() {
			continue
		}

		x := value.Fields()
		switch x.Name {
		case FieldFamily:
			family = x.Text
		case FieldInstrument:
			f.Instrument = x.Instrument
		case FieldDirection:
			f.Direction = TransferDirection(x.Text)
		case FieldQuantity:
			f.Quantity = x.Quantity
			if x.QuantityUnit != quantityUnit(familyInstrument(o)) {
				return SecurityTransfer{}, ErrInvalidActivity
			}
		case FieldTradeDate:
			f.Date = x.Date
		case FieldReportDate:
			f.ReportDate = x.Date
		case FieldSettlementDate:
			f.SettlementDate = x.Date
		case FieldExecutedAt:
			f.At = x.Timestamp
		case FieldTransferMethod:
			f.Method = x.Text
		case FieldDeliveringBroker:
			f.DeliveringBroker = x.Text
		case FieldTransferCompany:
			f.Company = x.Text
		case FieldCounterpartyAccount:
			f.CounterpartyAccount = x.Text
		case FieldCounterpartyName:
			f.CounterpartyName = x.Text
		case FieldTransferPrice:
			f.TransferPrice = x.Price
		case FieldPositionValue:
			f.PositionValue = x.Money
		case FieldTransferCost:
			f.ReportedCost = x.Money
		case FieldTransferPnL:
			f.ReportedPnL = x.Money
		case FieldRelationship:
			f.Relationships = append(f.Relationships, x.Relationship)
		}
	}

	if family != "security-transfer" {
		return SecurityTransfer{}, ErrInvalidActivity
	}

	return NewSecurityTransfer(f)
}

func familyInstrument(o ActivityObservation) InstrumentKind {
	f, _ := o.Field(FieldInstrument)
	return f.Fields().Instrument.Kind()
}
