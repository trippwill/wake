// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package ibkrflex

import (
	"go.trippwill.dev/wake/financial"
	"go.trippwill.dev/wake/source"
)

// These roles are explicit source relationships, not a sum of matching amounts.
type chargeMapping struct {
	field      string
	category   source.ChargeCategory
	components []chargeMapping
}

var confirmationCharges = []chargeMapping{
	{"commission", source.Commission, []chargeMapping{
		{"brokerExecutionCommission", source.BrokerExecutionFee, nil},
		{"brokerClearingCommission", source.BrokerClearingFee, nil},
		{"thirdPartyExecutionCommission", source.ThirdPartyExecutionFee, nil},
		{"thirdPartyClearingCommission", source.ThirdPartyClearingFee, nil},
		{"thirdPartyRegulatoryCommission", source.RegulatoryFee, []chargeMapping{
			{"regFINRATradingActivityFee", source.RegulatoryFee, nil},
			{"regSection31TransactionFee", source.RegulatoryFee, nil},
			{"regOther", source.RegulatoryFee, nil},
		}},
		{"otherCommission", source.OtherFee, nil},
	}},
	{"tax", source.Tax, nil},
	{"salesTax", source.SalesTax, nil},
	{"tradeCharge", source.TransactionFee, nil},
	{"otherTax", source.OtherTax, nil},
}

func reportedCharges(row map[string]string, currency financial.Currency) ([]source.Charge, error) {
	var read func(chargeMapping, financial.Currency) (source.Charge, error)

	read = func(mapping chargeMapping, currency financial.Currency) (source.Charge, error) {
		f := source.ChargeInput{Category: mapping.category, Field: mapping.field}

		var err error
		if amount := row[mapping.field]; amount != "" {
			f.Total, err = financial.ParseMoney(amount, currency)
			if err != nil {
				return source.Charge{}, err
			}
		}

		for _, child := range mapping.components {
			c, e := read(child, currency)
			if e != nil {
				return source.Charge{}, e
			}

			if c.Field() != "" {
				f.Components = append(f.Components, c)
			}
		}

		if f.Total.Amount() == "" && len(f.Components) == 0 {
			return source.Charge{}, nil
		}

		return source.NewCharge(f)
	}

	var out []source.Charge
	for _, mapping := range confirmationCharges {
		cc := currency
		if mapping.category == source.Commission {
			// Parse only if a commission assertion exists, including a component-only row.
			var present func(chargeMapping) bool

			present = func(m chargeMapping) bool {
				if row[m.field] != "" {
					return true
				}

				for _, child := range m.components {
					if present(child) {
						return true
					}
				}

				return false
			}
			if !present(mapping) {
				continue
			}

			var err error

			cc, err = financial.ParseCurrency(row["commissionCurrency"])
			if err != nil {
				return nil, err
			}
		}

		c, err := read(mapping, cc)
		if err != nil {
			return nil, err
		}

		if c.Field() != "" {
			out = append(out, c)
		}
	}

	return out, nil
}
