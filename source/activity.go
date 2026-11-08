// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package source

import (
	"net/url"
	"reflect"
	"slices"

	"go.trippwill.dev/wake/financial"
)

const ErrInvalidActivity Error = "invalid activity observation"

type ActivityFieldName string

const (
	FieldDeliveringBroker        ActivityFieldName = "delivering_broker"
	FieldAvailableForTradingDate ActivityFieldName = "available_for_trading_date"
	FieldCashCategory            ActivityFieldName = "cash_category"
	FieldCashAmount              ActivityFieldName = "cash_amount"
	FieldExDate                  ActivityFieldName = "ex_date"
	FieldReportedType            ActivityFieldName = "reported_type"
	FieldDescription             ActivityFieldName = "description"
	FieldSourceCode              ActivityFieldName = "source_code"
	FieldCashAction              ActivityFieldName = "cash_action"
	FieldDirection               ActivityFieldName = "direction"
	FieldReportDate              ActivityFieldName = "report_date"
	FieldTransferMethod          ActivityFieldName = "transfer_method"
	FieldTransferCompany         ActivityFieldName = "transfer_company"
	FieldCounterpartyAccount     ActivityFieldName = "counterparty_account"
	FieldCounterpartyName        ActivityFieldName = "counterparty_name"
	FieldTransferPrice           ActivityFieldName = "transfer_price"
	FieldPositionValue           ActivityFieldName = "position_value"
	FieldTransferCost            ActivityFieldName = "transfer_cost"
	FieldTransferPnL             ActivityFieldName = "transfer_pnl"
	FieldFamily                  ActivityFieldName = "family"
	FieldKind                    ActivityFieldName = "kind"
	FieldTradeKind               ActivityFieldName = "trade_kind"
	FieldSide                    ActivityFieldName = "side"
	FieldPositionEffect          ActivityFieldName = "position_effect"
	FieldQuantity                ActivityFieldName = "quantity"
	FieldPrice                   ActivityFieldName = "price"
	FieldRate                    ActivityFieldName = "rate"
	FieldRateBase                ActivityFieldName = "rate_base"
	FieldInstrument              ActivityFieldName = "instrument"
	FieldOrder                   ActivityFieldName = "order"
	FieldTradeDate               ActivityFieldName = "trade_date"
	FieldExecutedAt              ActivityFieldName = "executed_at"
	FieldSettlementDate          ActivityFieldName = "settlement_date"
	FieldSent                    ActivityFieldName = "sent"
	FieldReceived                ActivityFieldName = "received"
	FieldCharge                  ActivityFieldName = "charge"
	FieldRelationship            ActivityFieldName = "relationship"
	FieldGross                   ActivityFieldName = "gross"
	FieldProceeds                ActivityFieldName = "proceeds"
	FieldNet                     ActivityFieldName = "net"
	FieldNetBillable             ActivityFieldName = "net_billable"
	FieldAccruedInterest         ActivityFieldName = "accrued_interest"
	FieldRealizedPnL             ActivityFieldName = "realized_pnl"
	FieldReportedCostBasis       ActivityFieldName = "reported_cost_basis"
)

// ActivityFieldInput is a closed typed assertion. Only the payload belonging to
// Name may be set. Unknown is an explicit source assertion, not an omitted field.
type ActivityFieldInput struct {
	Name           ActivityFieldName
	Unknown        bool
	QuantityUnit   string
	PriceBasis     string
	ReportedOn     financial.Date
	Text           string
	Quantity       financial.Quantity
	Price          financial.Price
	Currency       financial.Currency
	Instrument     Instrument
	Reference      Reference
	Date           financial.Date
	Timestamp      financial.Timestamp
	CurrencyAmount CurrencyAmount
	Charge         Charge
	ChargeParents  []ChargeRole
	Relationship   Relationship
	Money          financial.Money
}
type ActivityField struct{ input ActivityFieldInput }

func NewActivityField(f ActivityFieldInput) (ActivityField, error) {
	expected := ActivityFieldInput{Name: f.Name, Unknown: f.Unknown, ReportedOn: f.ReportedOn}
	valid := false
	switch f.Name {
	case FieldFamily:
		valid = f.Unknown ||
			(f.Text == "trade" || f.Text == "lifecycle" || f.Text == "conversion" || f.Text == "security-transfer" || f.Text == "cash")
		if !f.Unknown {
			expected.Text = f.Text
		}
	case FieldCashCategory:
		valid = f.Unknown || validCashCategory(CashCategory(f.Text))
		if !f.Unknown {
			expected.Text = f.Text
		}
	case FieldCashAction:
		valid = f.Unknown || !f.Reference.IsZero()
		if !f.Unknown {
			expected.Reference = f.Reference
		}
	case FieldDirection:
		valid = f.Unknown || f.Text == string(TransferIn) || f.Text == string(TransferOut)
		if !f.Unknown {
			expected.Text = f.Text
		}
	case FieldDeliveringBroker, FieldTransferMethod,
		FieldTransferCompany,
		FieldCounterpartyAccount,
		FieldCounterpartyName,
		FieldReportedType,
		FieldDescription,
		FieldSourceCode:
		valid = f.Unknown || validText(f.Text, 1024)
		if !f.Unknown {
			expected.Text = f.Text
		}
	case FieldKind:
		valid = f.Unknown ||
			(f.Text == string(Exercise) || f.Text == string(Assignment) || f.Text == string(Expiration))
		if !f.Unknown {
			expected.Text = f.Text
		}
	case FieldTradeKind:
		valid = f.Unknown ||
			(f.Text == string(ExchangeExecution) || f.Text == string(StockBooking) || f.Text == string(ReportedTrade))
		if !f.Unknown {
			expected.Text = f.Text
		}
	case FieldSide:
		valid = f.Unknown || (f.Text == string(Buy) || f.Text == string(Sell))
		if !f.Unknown {
			expected.Text = f.Text
		}
	case FieldPositionEffect:
		valid = f.Unknown || (f.Text == string(Opening) || f.Text == string(Closing) || f.Text == string(Mixed))
		if !f.Unknown {
			expected.Text = f.Text
		}
	case FieldQuantity:
		valid = f.Unknown ||
			(f.Quantity.Positive() && (f.QuantityUnit == "shares" || f.QuantityUnit == "contracts" || f.QuantityUnit == "nominal"))
		if !f.Unknown {
			expected.Quantity = f.Quantity
			expected.QuantityUnit = f.QuantityUnit
		}
	case FieldTransferPrice:
		valid = f.Unknown || f.Price.Amount() != ""
		if !f.Unknown {
			expected.Price = f.Price
		}
	case FieldPrice:
		if !f.Unknown {
			expected.PriceBasis = f.PriceBasis
		}

		valid = f.Unknown ||
			(f.Price.Amount() != "" && (f.PriceBasis == "" || f.PriceBasis == "per share" || f.PriceBasis == "premium per underlying unit"))
		if !f.Unknown {
			expected.Price = f.Price
		}
	case FieldRate:
		valid = f.Unknown || (f.Price.Amount() != "")
		if !f.Unknown {
			expected.Price = f.Price
		}
	case FieldRateBase:
		valid = f.Unknown || (f.Currency != (financial.Currency{}))
		if !f.Unknown {
			expected.Currency = f.Currency
		}
	case FieldInstrument:
		valid = f.Unknown || (f.Instrument.Kind() != "")
		if !f.Unknown {
			expected.Instrument = f.Instrument
		}
	case FieldOrder:
		valid = f.Unknown || (!f.Reference.IsZero())
		if !f.Unknown {
			expected.Reference = f.Reference
		}
	case FieldTradeDate:
		valid = f.Unknown || (!f.Date.IsZero())
		if !f.Unknown {
			expected.Date = f.Date
		}
	case FieldExecutedAt:
		valid = f.Unknown || (!f.Timestamp.IsZero())
		if !f.Unknown {
			expected.Timestamp = f.Timestamp
		}
	case FieldSettlementDate, FieldReportDate, FieldExDate, FieldAvailableForTradingDate:
		valid = f.Unknown || (!f.Date.IsZero())
		if !f.Unknown {
			expected.Date = f.Date
		}
	case FieldSent:
		valid = f.Unknown || (f.CurrencyAmount.Currency() != (financial.Currency{}))
		if !f.Unknown {
			expected.CurrencyAmount = f.CurrencyAmount
		}
	case FieldReceived:
		valid = f.Unknown || (f.CurrencyAmount.Currency() != (financial.Currency{}))
		if !f.Unknown {
			expected.CurrencyAmount = f.CurrencyAmount
		}
	case FieldCharge:
		valid = f.Charge.Field() != "" && f.Charge.Total().Amount() != "" && len(f.Charge.Components()) == 0 &&
			len(f.ChargeParents) < 32
		for _, parent := range f.ChargeParents {
			valid = valid && parent.Field() != ""
		}

		if !f.Unknown {
			expected.Charge = f.Charge
			expected.ChargeParents = f.ChargeParents
		}
	case FieldRelationship:
		valid = f.Unknown || (!f.Relationship.Target().IsZero())
		if !f.Unknown {
			expected.Relationship = f.Relationship
		}
	case FieldGross, FieldPositionValue, FieldTransferCost, FieldTransferPnL, FieldCashAmount:
		valid = f.Unknown || (f.Money.Amount() != "")
		if !f.Unknown {
			expected.Money = f.Money
		}
	case FieldProceeds:
		valid = f.Unknown || (f.Money.Amount() != "")
		if !f.Unknown {
			expected.Money = f.Money
		}
	case FieldNet:
		valid = f.Unknown || (f.Money.Amount() != "")
		if !f.Unknown {
			expected.Money = f.Money
		}
	case FieldNetBillable:
		valid = f.Unknown || (f.Money.Amount() != "")
		if !f.Unknown {
			expected.Money = f.Money
		}
	case FieldAccruedInterest:
		valid = f.Unknown || (f.Money.Amount() != "")
		if !f.Unknown {
			expected.Money = f.Money
		}
	case FieldRealizedPnL:
		valid = f.Unknown || (f.Money.Amount() != "")
		if !f.Unknown {
			expected.Money = f.Money
		}
	case FieldReportedCostBasis:
		valid = f.Unknown || (f.Money.Amount() != "")
		if !f.Unknown {
			expected.Money = f.Money
		}
	}

	if !valid || !reflect.DeepEqual(expected, f) ||
		(f.Unknown && (f.Name == FieldCharge || f.Name == FieldRelationship)) {
		return ActivityField{}, ErrInvalidActivity
	}

	f.ChargeParents = slices.Clone(f.ChargeParents)

	return ActivityField{f}, nil
}

func (f ActivityField) Fields() ActivityFieldInput {
	v := f.input
	v.ChargeParents = slices.Clone(v.ChargeParents)

	return v
}
func (f ActivityField) Name() ActivityFieldName { return f.input.Name }
func (f ActivityField) Known() bool             { return f.input.Name != "" && !f.input.Unknown }

// Key distinguishes independent charge roles and relationships without allowing
// arbitrary financial property names.
func (f ActivityField) Key() string {
	switch f.input.Name {
	case FieldCharge:
		return string(f.input.Name) + "/" + chargePath(f.input.ChargeParents, f.input.Charge.Role())
	case FieldRelationship:
		r := f.input.Relationship.Target()

		return string(
			f.input.Name,
		) + "/" + string(
			f.input.Relationship.Kind(),
		) + "/" + url.PathEscape(r.Authority()) + "/" + url.PathEscape(r.Kind()) + "/" +
			url.PathEscape(r.Scope()) + "/" + url.PathEscape(r.Value())
	}

	return string(f.input.Name)
}

type ActivityObservationInput struct {
	Location       string
	Account, Event Reference
	EventAliases   []Reference
	EventDistinct  bool
	Fields         []ActivityField
}

// ActivityObservation retains what one source record actually asserts. It need
// not contain enough terms to construct or publish an accepted activity.
type ActivityObservation struct{ input ActivityObservationInput }

func NewActivityObservation(f ActivityObservationInput) (ActivityObservation, error) {
	if !validText(f.Location, 1024) || f.Account.IsZero() || (f.EventDistinct && f.Event.IsZero()) ||
		len(f.EventAliases) > 16 ||
		len(f.Fields) == 0 ||
		len(f.Fields) > 128 {
		return ActivityObservation{}, ErrInvalidActivity
	}

	for _, r := range f.EventAliases {
		if r.IsZero() {
			return ActivityObservation{}, ErrInvalidActivity
		}
	}

	seen := map[string]bool{}
	for _, a := range f.Fields {
		if _, err := NewActivityField(
			a.Fields(),
		); err != nil ||
			(seen[a.Key()] && a.Name() != FieldCharge && a.Name() != FieldRelationship) {
			return ActivityObservation{}, ErrInvalidActivity
		}

		seen[a.Key()] = true
	}

	family := ""
	for _, a := range f.Fields {
		if a.Name() == FieldFamily && a.Known() {
			family = a.Fields().Text
		}
	}

	if family != "" {
		for _, a := range f.Fields {
			if a.Known() && !activityFieldApplies(family, a.Name()) {
				return ActivityObservation{}, ErrInvalidActivity
			}
		}
	}

	f.EventAliases = slices.Clone(f.EventAliases)
	f.Fields = slices.Clone(f.Fields)

	return ActivityObservation{f}, nil
}

func (o ActivityObservation) Fields() ActivityObservationInput {
	f := o.input
	f.EventAliases = slices.Clone(f.EventAliases)
	f.Fields = slices.Clone(f.Fields)

	return f
}
func (o ActivityObservation) IsZero() bool { return o.input.Location == "" }
func (o ActivityObservation) Field(name ActivityFieldName) (ActivityField, bool) {
	for _, f := range o.input.Fields {
		if f.Name() == name {
			return f, true
		}
	}

	return ActivityField{}, false
}

func completeObservation(
	location string,
	account, event Reference,
	aliases []Reference,
	distinct bool,
	fields []ActivityFieldInput,
) ActivityObservation {
	out := ActivityObservationInput{
		Location:      location,
		Account:       account,
		Event:         event,
		EventAliases:  aliases,
		EventDistinct: distinct,
	}
	for _, f := range fields {
		if reflect.DeepEqual(f, ActivityFieldInput{Name: f.Name}) {
			continue
		}

		value, err := NewActivityField(f)
		if err != nil {
			return ActivityObservation{}
		}

		out.Fields = append(out.Fields, value)
	}

	result, _ := NewActivityObservation(out)

	return result
}

func tradeObservationFields(f TradeInput) []ActivityFieldInput {
	fields := []ActivityFieldInput{
		{Name: FieldFamily, Text: "trade"},
		{Name: FieldTradeKind, Text: string(f.Kind)},
		{Name: FieldInstrument, Instrument: f.Instrument},
		{Name: FieldSide, Text: string(f.Side)},
		{Name: FieldQuantity, Quantity: f.Quantity, QuantityUnit: quantityUnit(f.Instrument.Kind())},
		{Name: FieldPrice, Price: f.Price, PriceBasis: priceBasis(f.Instrument.Kind())},
		{Name: FieldTradeDate, Date: f.TradeDate},
		{Name: FieldExecutedAt, Timestamp: f.ExecutedAt},
		{Name: FieldSettlementDate, Date: f.SettlementDate},
		{Name: FieldOrder, Reference: f.Order},
		{Name: FieldPositionEffect, Text: string(f.PositionEffect)},
		{Name: FieldGross, Money: f.Gross},
		{Name: FieldProceeds, Money: f.Proceeds},
		{Name: FieldNet, Money: f.Net},
		{Name: FieldNetBillable, Money: f.NetWithBillable},
		{Name: FieldAccruedInterest, Money: f.AccruedInterest},
	}
	for _, c := range ChargeFields(f.Charges) {
		fields = append(fields, c.Fields())
	}

	for _, r := range f.Relationships {
		fields = append(fields, ActivityFieldInput{Name: FieldRelationship, Relationship: r})
	}

	return fields
}

func (t Trade) Observation() ActivityObservation {
	if _, err := NewTrade(t.Fields()); err != nil {
		return ActivityObservation{}
	}

	f := t.Fields()

	return completeObservation(
		f.Location,
		f.Account,
		f.Event,
		f.EventAliases,
		f.EventDistinct,
		tradeObservationFields(f),
	)
}

func (l Lifecycle) Observation() ActivityObservation {
	if _, err := NewLifecycle(l.Fields()); err != nil {
		return ActivityObservation{}
	}

	f := l.Fields()
	fields := []ActivityFieldInput{
		{Name: FieldFamily, Text: "lifecycle"},
		{Name: FieldKind, Text: string(f.Kind)},
		{Name: FieldInstrument, Instrument: f.Instrument},
		{Name: FieldQuantity, Quantity: f.Quantity, QuantityUnit: "contracts"},
		{Name: FieldTradeDate, Date: f.Date},
		{Name: FieldExecutedAt, Timestamp: f.At},
		{Name: FieldOrder, Reference: f.Order},
	}
	for _, r := range f.Relationships {
		fields = append(fields, ActivityFieldInput{Name: FieldRelationship, Relationship: r})
	}

	return completeObservation(f.Location, f.Account, f.Event, f.EventAliases, f.EventDistinct, fields)
}

func (v Conversion) Observation() ActivityObservation {
	if _, err := NewConversion(v.Fields()); err != nil {
		return ActivityObservation{}
	}

	f := v.Fields()
	fields := []ActivityFieldInput{
		{Name: FieldFamily, Text: "conversion"},
		{Name: FieldTradeDate, Date: f.Date},
		{Name: FieldExecutedAt, Timestamp: f.At},
		{Name: FieldOrder, Reference: f.Order},
		{Name: FieldSent, CurrencyAmount: f.Sent},
		{Name: FieldReceived, CurrencyAmount: f.Received},
		{Name: FieldRateBase, Currency: f.RateBase},
		{Name: FieldRate, Price: f.Rate},
		{Name: FieldGross, Money: f.Gross},
		{Name: FieldProceeds, Money: f.Proceeds},
		{Name: FieldNet, Money: f.Net},
		{Name: FieldNetBillable, Money: f.NetWithBillable},
	}
	for _, c := range ChargeFields(f.Charges) {
		fields = append(fields, c.Fields())
	}

	return completeObservation(f.Location, f.Account, f.Event, f.EventAliases, f.EventDistinct, fields)
}

func quantityUnit(kind InstrumentKind) string {
	switch kind {
	case Option:
		return "contracts"
	case Debt:
		return "nominal"
	default:
		return "shares"
	}
}

func priceBasis(kind InstrumentKind) string {
	switch kind {
	case Option:
		return "premium per underlying unit"
	case Debt:
		return ""
	default:
		return "per share"
	}
}

func activityFieldApplies(family string, name ActivityFieldName) bool {
	switch name {
	case FieldDeliveringBroker, FieldDirection,
		FieldTransferMethod,
		FieldTransferCompany,
		FieldCounterpartyAccount,
		FieldCounterpartyName,
		FieldTransferPrice,
		FieldPositionValue,
		FieldTransferCost,
		FieldTransferPnL:
		return family == "security-transfer"
	case FieldAvailableForTradingDate, FieldCashCategory,
		FieldCashAmount,
		FieldExDate,
		FieldReportedType,
		FieldDescription,
		FieldSourceCode,
		FieldCashAction:
		return family == "cash"
	case FieldReportDate:
		return family == "security-transfer" || family == "cash"
	case FieldSettlementDate:
		return family == "trade" || family == "security-transfer" || family == "cash"
	case FieldTradeKind, FieldSide, FieldPositionEffect, FieldPrice, FieldAccruedInterest:
		return family == "trade"
	case FieldOrder:
		return family == "trade" || family == "lifecycle" || family == "conversion"
	case FieldKind:
		return family == "lifecycle"
	case FieldRate, FieldRateBase, FieldSent, FieldReceived:
		return family == "conversion"
	case FieldInstrument, FieldRelationship:
		return family == "trade" || family == "lifecycle" || family == "security-transfer" || family == "cash"
	case FieldQuantity:
		return family == "trade" || family == "lifecycle" || family == "security-transfer"
	case FieldCharge:
		return family == "trade" || family == "conversion" || family == "cash"
	case FieldGross, FieldProceeds, FieldNet, FieldNetBillable:
		return family == "trade" || family == "conversion"
	}

	return true
}

func (f ActivityField) AppliesTo(family string) bool {
	switch family {
	case "trade", "lifecycle", "conversion", "security-transfer", "cash":
		return activityFieldApplies(family, f.Name())
	default:
		return false
	}
}
