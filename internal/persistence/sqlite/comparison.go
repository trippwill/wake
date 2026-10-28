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

		values[key] = application.CorrectionValue{Value: value.Amount(), Currency: value.Currency().String()}
	}

	price := func(key string, value financial.Price) {
		if value.Amount() == "" {
			values[key] = application.CorrectionValue{}
			return
		}

		values[key] = application.CorrectionValue{Value: value.Amount(), Currency: value.Currency().String()}
	}
	charges := func(items []source.Charge) {
		rows := make([]string, 0, len(items))
		for _, charge := range items {
			rows = append(
				rows,
				charge.Field()+" ("+charge.Scope()+"): "+charge.Money().
					Amount()+
					" "+charge.Money().
					Currency().
					String(),
			)
		}

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
					"cash: "+component.Cash().Amount()+" "+component.Cash().Currency().String(),
				)
			}
		}

		sort.Strings(components)
		text("option.deliverables", strings.Join(components, "; "))
	}
	switch kind {
	case "trade":
		t, err := decodeTrade(data)
		if err != nil {
			return nil, err
		}

		f := t.Fields()
		charges(f.Charges)
		text("quantity", f.Quantity.Value())

		v := values["quantity"]
		v.Unit = t.QuantityUnit()
		values["quantity"] = v

		price("price", f.Price)

		v = values["price"]
		v.Unit = t.PriceBasis()
		values["price"] = v

		text("side", string(f.Side))
		text("trade_date", f.TradeDate.String())
		text("executed_at", f.ExecutedAt.String())
		text("settlement_date", comparisonDate(f.SettlementDate))
		money("gross", f.Gross)
		money("proceeds", f.Proceeds)
		money("net", f.Net)
		money("net_billable", f.NetWithBillable)
		money("accrued_interest", f.AccruedInterest)
		text("position_effect", string(f.PositionEffect))
		text("quotation_basis", string(f.QuotationBasis))

		if f.Instrument.Kind() == source.Option {
			option(f.Instrument.Option())
		}

		if debt := f.Instrument.Debt(); f.Instrument.Kind() == source.Debt {
			text("debt.coupon", debt.Fields().Coupon.Value())
			text("debt.maturity", comparisonDate(debt.Fields().Maturity))
			terms := debt.Fields()
			text("debt.type", string(terms.Type))
			text("debt.currency", terms.Currency.String())
			text("debt.issue_date", comparisonDate(terms.IssueDate))
			text("debt.dated_date", comparisonDate(terms.DatedDate))

			payments := ""
			if terms.PaymentsPerYear > 0 {
				payments = strconv.Itoa(terms.PaymentsPerYear)
			}

			text("debt.payments_per_year", payments)
		}
	case "lifecycle":
		l, err := decodeLifecycle(data)
		if err != nil {
			return nil, err
		}

		f := l.Fields()
		values["quantity"] = application.CorrectionValue{Value: f.Quantity.Value(), Unit: "contracts"}
		text("kind", string(f.Kind))
		text("trade_date", f.Date.String())
		text("executed_at", f.At.String())
		option(f.Instrument.Option())
	case "conversion":
		c, err := decodeConversion(data)
		if err != nil {
			return nil, err
		}

		f := c.Fields()
		charges(f.Charges)
		values["sent_amount"] = application.CorrectionValue{
			Value:    f.Sent.Amount(),
			Currency: f.Sent.Currency().String(),
		}
		values["received_amount"] = application.CorrectionValue{
			Value:    f.Received.Amount(),
			Currency: f.Received.Currency().String(),
		}
		price("rate", f.Rate)

		if f.Rate.Amount() != "" {
			v := values["rate"]
			v.Unit = "per " + f.RateBase.String()
			values["rate"] = v
		}

		text("trade_date", f.Date.String())
		text("executed_at", f.At.String())
		money("gross", f.Gross)
		money("proceeds", f.Proceeds)
		money("net", f.Net)
		money("net_billable", f.NetWithBillable)
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
