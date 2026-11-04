// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package source

import (
	"slices"

	"go.trippwill.dev/wake/financial"
)

type CashCategory string

const (
	CashFunding       CashCategory = "funding"
	CashTransfer      CashCategory = "transfer"
	CashDividend      CashCategory = "dividend"
	CashPaymentInLieu CashCategory = "payment-in-lieu"
	CashInterest      CashCategory = "interest"
	CashWithholding   CashCategory = "withholding"
	CashFee           CashCategory = "fee"
	CashTax           CashCategory = "tax"
	CashAdjustment    CashCategory = "adjustment"
	CashOther         CashCategory = "other"
)

type CashActivityInput struct {
	Location                                                          string
	Account, Event                                                    Reference
	EventAliases                                                      []Reference
	EventDistinct                                                     bool
	Instrument                                                        Instrument
	Category                                                          CashCategory
	Amount                                                            financial.Money
	Date, ReportDate, SettlementDate, ExDate, AvailableForTradingDate financial.Date
	At                                                                financial.Timestamp
	ReportedType, Description, Code                                   string
	Action                                                            Reference
	Relationships                                                     []Relationship
}

type CashActivity struct{ input CashActivityInput }

func NewCashActivity(f CashActivityInput) (CashActivity, error) {
	if !validCashCategory(f.Category) || f.Amount.Amount() == "" || f.Date.IsZero() ||
		!validRelationships(f.Relationships) {
		return CashActivity{}, ErrInvalidActivity
	}

	f.EventAliases = slices.Clone(f.EventAliases)
	f.Relationships = slices.Clone(f.Relationships)

	v := CashActivity{f}
	if v.Observation().IsZero() {
		return CashActivity{}, ErrInvalidActivity
	}

	return v, nil
}

func validCashCategory(c CashCategory) bool {
	switch c {
	case CashFunding,
		CashTransfer,
		CashDividend,
		CashPaymentInLieu,
		CashInterest,
		CashWithholding,
		CashFee,
		CashTax,
		CashAdjustment,
		CashOther:
		return true
	}

	return false
}

func (v CashActivity) Fields() CashActivityInput {
	f := v.input
	f.EventAliases = slices.Clone(f.EventAliases)
	f.Relationships = slices.Clone(f.Relationships)

	return f
}

func (v CashActivity) Observation() ActivityObservation {
	f := v.input
	fields := []ActivityFieldInput{
		{Name: FieldFamily, Text: "cash"},
		{Name: FieldCashCategory, Text: string(f.Category)},
		{Name: FieldCashAmount, Money: f.Amount},
		{Name: FieldInstrument, Instrument: f.Instrument},
		{Name: FieldTradeDate, Date: f.Date},
		{Name: FieldReportDate, Date: f.ReportDate},
		{Name: FieldAvailableForTradingDate, Date: f.AvailableForTradingDate},
		{Name: FieldSettlementDate, Date: f.SettlementDate},
		{Name: FieldExDate, Date: f.ExDate},
		{Name: FieldExecutedAt, Timestamp: f.At},
		{Name: FieldReportedType, Text: f.ReportedType},
		{Name: FieldDescription, Text: f.Description},
		{Name: FieldSourceCode, Text: f.Code},
		{Name: FieldCashAction, Reference: f.Action},
	}
	for _, r := range f.Relationships {
		fields = append(fields, ActivityFieldInput{Name: FieldRelationship, Relationship: r})
	}

	return completeObservation(f.Location, f.Account, f.Event, f.EventAliases, f.EventDistinct, fields)
}

func (o ActivityObservation) CashActivity() (CashActivity, error) {
	s := o.Fields()
	f := CashActivityInput{
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
		case FieldCashCategory:
			f.Category = CashCategory(x.Text)
		case FieldCashAmount:
			f.Amount = x.Money
		case FieldInstrument:
			f.Instrument = x.Instrument
		case FieldTradeDate:
			f.Date = x.Date
		case FieldReportDate:
			f.ReportDate = x.Date
		case FieldSettlementDate:
			f.SettlementDate = x.Date
		case FieldAvailableForTradingDate:
			f.AvailableForTradingDate = x.Date
		case FieldExDate:
			f.ExDate = x.Date
		case FieldExecutedAt:
			f.At = x.Timestamp
		case FieldReportedType:
			f.ReportedType = x.Text
		case FieldDescription:
			f.Description = x.Text
		case FieldSourceCode:
			f.Code = x.Text
		case FieldCashAction:
			f.Action = x.Reference
		case FieldRelationship:
			f.Relationships = append(f.Relationships, x.Relationship)
		}
	}

	if family != "cash" {
		return CashActivity{}, ErrInvalidActivity
	}

	return NewCashActivity(f)
}
