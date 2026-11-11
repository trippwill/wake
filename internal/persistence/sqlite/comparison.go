// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"go.trippwill.dev/wake/financial"
	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/source"
)

// Comparison values retain the exact text and units of the decoded financial input.
func comparisonValues(kind string, data []byte) (map[string]application.CorrectionValue, error) {
	values := map[string]application.CorrectionValue{}
	text := func(key, value string) { values[key] = application.CorrectionValue{Value: value} }

	money := func(key string, value financial.Money) {
		if value.Amount() == "" {
			values[key] = application.CorrectionValue{}
			return
		}

		values[key] = application.CorrectionValue{
			Value:    value.Amount(),
			Currency: comparisonCurrency(value.Currency()),
		}
	}

	price := func(key string, value financial.Price) {
		if value.Amount() == "" {
			values[key] = application.CorrectionValue{}
			return
		}

		values[key] = application.CorrectionValue{
			Value:    value.Amount(),
			Currency: comparisonCurrency(value.Currency()),
		}
	}

	metadata := func(account, event, order source.Reference, aliases []source.Reference, relationships []source.Relationship) {
		text("reported.account", comparisonReference(account))
		text("reported.event", comparisonReference(event))
		text("reported.order", comparisonReference(order))

		values := make([]string, 0, len(aliases))
		for _, alias := range aliases {
			values = append(values, comparisonReference(alias))
		}

		sort.Strings(values)
		text("reported.event_aliases", strings.Join(values, "; "))

		values = make([]string, 0, len(relationships))
		for _, relationship := range relationships {
			values = append(values, string(relationship.Kind())+": "+comparisonReference(relationship.Target()))
		}

		sort.Strings(values)
		text("reported.relationships", strings.Join(values, "; "))
	}

	charges := func(items []source.Charge) {
		rows := []string{}

		var visit func([]source.Charge, string)

		visit = func(items []source.Charge, prefix string) {
			for _, c := range items {
				path := prefix + c.Field()
				amount := "Total not reported"
				if c.Total().Amount() != "" {
					amount = c.Total().Amount() + " " + c.Total().Currency().String()
				}

				rows = append(rows, path+" ("+string(c.Category())+"): "+amount)
				visit(c.Components(), path+" / ")
			}
		}
		visit(items, "")
		sort.Strings(rows)

		value := strings.Join(rows, "; ")
		if value == "" {
			value = "None reported"
		}

		text("charges", value)
	}

	option := func(value source.OptionTerms) {
		f := value.Fields()
		price("option.strike", f.Strike)
		text("option.multiplier", f.Multiplier.Value())
		text("option.right", string(f.Right))
		text("option.expiry", comparisonDate(f.Expiry))
		text("option.effective_from", comparisonDate(f.EffectiveFrom))
		text("option.reported_underlier", comparisonInstrument(f.Underlier))

		components := make([]string, 0, len(f.Deliverables))
		for _, component := range f.Deliverables {
			if ref := component.Security(); !ref.IsZero() {
				components = append(
					components,
					fmt.Sprintf(
						"security authority=%q kind=%q scope=%q value=%q: %s units",
						ref.Authority(),
						ref.Kind(),
						ref.Scope(),
						ref.Value(),
						component.Quantity().Value(),
					),
				)
			} else {
				components = append(
					components,
					"cash: "+component.Cash().Amount()+" "+comparisonCurrency(component.Cash().Currency()),
				)
			}
		}

		sort.Strings(components)
		text("option.deliverables", strings.Join(components, "; "))
	}
	debt := func(value source.DebtTerms) {
		terms := value.Fields()
		text("debt.coupon_rate", terms.CouponRate.Value())
		text("debt.maturity", comparisonDate(terms.Maturity))
		text("debt.type", string(terms.Type))
		text("debt.currency", comparisonCurrency(terms.Currency))
		text("debt.issue_date", comparisonDate(terms.IssueDate))
		text("debt.dated_date", comparisonDate(terms.DatedDate))

		payments := ""
		if terms.PaymentsPerYear > 0 {
			payments = strconv.Itoa(terms.PaymentsPerYear)
		}

		text("debt.payments_per_year", payments)
	}
	activity := func(o source.ActivityObservation) {
		chargeFields := []source.ActivityField{}
		for _, field := range o.Fields().Fields {
			f := field.Fields()
			key := field.Key()
			switch f.Name {
			case source.FieldInstrument, source.FieldOrder, source.FieldTradeKind:
				key = "reported." + key
			case source.FieldSent, source.FieldReceived:
				key += "_amount"
			}

			if f.Name == source.FieldRealizedPnL {
				text("realized_report_date", optionalSourceDate(f.ReportedOn))
			}

			if f.Name == source.FieldReportedCostBasis {
				text("cost_basis_report_date", optionalSourceDate(f.ReportedOn))
			}

			if !field.Known() {
				values[key] = application.CorrectionValue{}
				continue
			}

			switch f.Name {
			case source.FieldCashAmount,
				source.FieldPositionValue,
				source.FieldTransferCost,
				source.FieldTransferPnL,
				source.FieldGrossAmount,
				source.FieldProceeds,
				source.FieldNetCash,
				source.FieldNetCashWithBillable,
				source.FieldAccruedInterest,
				source.FieldRealizedPnL,
				source.FieldReportedCostBasis:
				money(key, f.Money)
			case source.FieldPrice, source.FieldRate, source.FieldTransferPrice:
				price(key, f.Price)

				v := values[key]
				v.Unit = string(f.QuotationBasis)
				if f.Name == source.FieldRate {
					if base, ok := o.Field(source.FieldRateBase); ok && base.Known() {
						v.Unit = "per " + base.Fields().Currency.String()
					}
				}

				values[key] = v
			case source.FieldQuantity:
				values[key] = application.CorrectionValue{Value: f.Quantity.Value(), Unit: f.QuantityUnit}
			case source.FieldDate,
				source.FieldSettlementDate,
				source.FieldReportDate,
				source.FieldExDate,
				source.FieldAvailableForTradingDate:
				text(key, f.Date.String())
			case source.FieldAt:
				text(key, f.Timestamp.String())
			case source.FieldInstrument:
				text("reported.instrument", comparisonInstrument(f.Instrument))

				if f.Instrument.Kind() == source.Option {
					option(f.Instrument.Option())
				}

				if f.Instrument.Kind() == source.Debt {
					debt(f.Instrument.Debt())
				}
			case source.FieldTradeKind:
				text("reported.trade_kind", f.Text)
			case source.FieldCashAction:
				text(key, comparisonReference(f.Reference))
			case source.FieldOrder:
				text("reported.order", comparisonReference(f.Reference))
			case source.FieldSent, source.FieldReceived:
				values[key] = application.CorrectionValue{
					Value: f.CurrencyAmount.Amount(), Currency: comparisonCurrency(f.CurrencyAmount.Currency()),
				}
			case source.FieldRateBase:
				text(key, f.Currency.String())
			case source.FieldCharge:
				money(key, f.Charge.Total())

				chargeFields = append(chargeFields, field)
			case source.FieldRelationship:
				text(key, comparisonReference(f.Relationship.Target()))
			default:
				text(key, f.Text)
			}
		}

		if len(chargeFields) > 0 {
			// Comparisons also retain internally conflicting raw assertions.
			rows := []string{}
			for _, f := range chargeFields {
				assertion := f.Fields()
				c := assertion.Charge
				categories := make([]string, 0, len(assertion.ChargeParents)+1)
				for _, parent := range assertion.ChargeParents {
					categories = append(categories, string(parent.Category()))
				}

				categories = append(categories, string(c.Category()))
				rows = append(
					rows,
					fmt.Sprintf("%s (%s): %s %s", f.Key(), strings.Join(categories, " / "),
						c.Total().Amount(), c.Total().Currency().String()),
				)
			}

			sort.Strings(rows)
			text("charges", strings.Join(rows, "; "))
		}
	}
	switch kind {
	case "security-transfer", "cash":
		o, err := decodeObservation(data)
		if err != nil {
			return nil, err
		}

		activity(o)
	case "activity":
		o, err := decodeObservation(data)
		if err != nil {
			return nil, err
		}

		activity(o)
	case "trade":
		t, err := decodeTrade(data)
		if err != nil {
			return nil, err
		}

		f := t.Fields()
		metadata(f.Account, f.Event, f.Order, f.EventAliases, f.Relationships)
		text("reported.event_distinct", strconv.FormatBool(f.EventDistinct))
		text("reported.trade_kind", string(f.Kind))
		text("reported.instrument", comparisonInstrument(f.Instrument))
		charges(f.Charges)
		text("quantity", f.Quantity.Value())

		v := values["quantity"]
		v.Unit = t.QuantityUnit()
		values["quantity"] = v

		price("price", f.Price)

		v = values["price"]
		v.Unit = string(t.QuotationBasis())
		values["price"] = v

		text("side", string(f.Side))
		text("date", f.Date.String())
		text("at", f.At.String())
		text("settlement_date", comparisonDate(f.SettlementDate))
		money("gross_amount", f.GrossAmount)
		money("proceeds", f.Proceeds)
		money("net_cash", f.NetCash)
		money("net_cash_with_billable", f.NetCashWithBillable)
		money("accrued_interest", f.AccruedInterest)
		text("position_effect", string(f.PositionEffect))

		if f.Instrument.Kind() == source.Option {
			option(f.Instrument.Option())
		}

		if f.Instrument.Kind() == source.Debt {
			debt(f.Instrument.Debt())
		}

		activity(t.Observation())
	case "lifecycle":
		l, err := decodeLifecycle(data)
		if err != nil {
			return nil, err
		}

		f := l.Fields()
		metadata(f.Account, f.Event, f.Order, f.EventAliases, f.Relationships)
		text("reported.event_distinct", strconv.FormatBool(f.EventDistinct))
		text("reported.instrument", comparisonInstrument(f.Instrument))
		values["quantity"] = application.CorrectionValue{Value: f.Quantity.Value(), Unit: "contracts"}
		text("kind", string(f.Kind))
		text("date", f.Date.String())
		text("at", f.At.String())
		option(f.Instrument.Option())
		activity(l.Observation())
	case "conversion":
		c, err := decodeConversion(data)
		if err != nil {
			return nil, err
		}

		f := c.Fields()
		metadata(f.Account, f.Event, f.Order, f.EventAliases, nil)
		text("reported.event_distinct", strconv.FormatBool(f.EventDistinct))
		charges(f.Charges)
		values["sent_amount"] = application.CorrectionValue{
			Value:    f.Sent.Amount(),
			Currency: comparisonCurrency(f.Sent.Currency()),
		}
		values["received_amount"] = application.CorrectionValue{
			Value:    f.Received.Amount(),
			Currency: comparisonCurrency(f.Received.Currency()),
		}
		price("rate", f.Rate)

		if f.Rate.Amount() != "" {
			v := values["rate"]
			v.Unit = "per " + comparisonCurrency(f.RateBase)
			values["rate"] = v
		}

		text("date", f.Date.String())
		text("at", f.At.String())
		money("gross_amount", f.GrossAmount)
		money("proceeds", f.Proceeds)
		money("net_cash", f.NetCash)
		money("net_cash_with_billable", f.NetCashWithBillable)
		activity(c.Observation())
	default:
		return nil, application.ErrInvalid
	}

	return values, nil
}

func comparisonDate(d financial.Date) string {
	if d.IsZero() {
		return ""
	}

	return d.String()
}

func comparisonCurrency(currency financial.Currency) string {
	if currency == (financial.Currency{}) {
		return ""
	}

	return currency.String()
}

func comparisonReference(reference source.Reference) string {
	if reference.IsZero() {
		return ""
	}

	return fmt.Sprintf(
		"authority=%q kind=%q scope=%q value=%q",
		reference.Authority(),
		reference.Kind(),
		reference.Scope(),
		reference.Value(),
	)
}

func comparisonInstrument(instrument source.Instrument) string {
	if instrument.Kind() == "" {
		return ""
	}

	values := []string{fmt.Sprintf("kind=%q", instrument.Kind())}
	if reference := comparisonReference(instrument.Reference()); reference != "" {
		values = append(values, reference)
	}

	identifiers := make([]string, 0, len(instrument.Identifiers()))
	for _, identifier := range instrument.Identifiers() {
		identifiers = append(identifiers, fmt.Sprintf("%s=%q", identifier.Kind(), identifier.Value()))
	}

	sort.Strings(identifiers)

	return strings.Join(append(values, identifiers...), "; ")
}
