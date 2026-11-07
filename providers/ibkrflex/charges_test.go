// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package ibkrflex

import (
	"testing"

	"go.trippwill.dev/wake/financial"
	"go.trippwill.dev/wake/source"
)

func TestReportedChargeHierarchyKeepsSourceAmountsAndCurrencies(t *testing.T) {
	usd, _ := financial.ParseCurrency("USD")
	row := map[string]string{
		"commission":                     "-1.0000",
		"commissionCurrency":             "GBP",
		"brokerExecutionCommission":      "-0.50",
		"thirdPartyRegulatoryCommission": "-0.12300",
		"regFINRATradingActivityFee":     "-0.00100",
		"regSection31TransactionFee":     "0.0000",
		"regOther":                       "0.0500",
		"tax":                            "0.00",
		"salesTax":                       "-0.25",
		"otherTax":                       "0",
		"tradeCharge":                    "-0.1",
	}

	charges, err := reportedCharges(row, usd)
	if err != nil || len(charges) != 5 {
		t.Fatal(charges, err)
	}

	commission := charges[0]
	reg := commission.Components()[1]
	if commission.Category() != source.Commission || commission.Total().Amount() != "-1.0000" ||
		reg.Total().Amount() != "-0.12300" ||
		len(reg.Components()) != 3 {
		t.Fatal(commission)
	}

	for _, field := range source.ChargeFields([]source.Charge{commission}) {
		if field.Fields().Charge.Total().Currency().String() != "GBP" {
			t.Fatal("commission component used trade currency", field)
		}
	}

	for _, c := range charges[1:] {
		if c.Total().Currency() != usd {
			t.Fatal("tax currency changed", c)
		}
	}

	delete(row, "commission")
	delete(row, "thirdPartyRegulatoryCommission")

	charges, err = reportedCharges(row, usd)
	if err != nil || charges[0].Total().Amount() != "" || charges[0].Components()[1].Total().Amount() != "" {
		t.Fatal("missing totals calculated", charges, err)
	}

	delete(row, "commissionCurrency")

	if _, err = reportedCharges(row, usd); err == nil {
		t.Fatal("component currency invented")
	}
}

func TestStandaloneFeeAndTaxKeepSeparateReportedTotals(t *testing.T) {
	for _, kind := range []string{"Other Fees", "Sales Tax"} {
		row := map[string]string{
			"type":          kind,
			"accountId":     "U1",
			"transactionID": "one",
			"amount":        "0.0000",
			"currency":      "USD",
			"dateTime":      "2026-10-20",
		}

		o, err := cashPosting(row, "U1", "/cash")
		if err != nil {
			t.Fatal(err)
		}

		cash, err := o.CashActivity()
		if err != nil || len(cash.Charges()) != 1 || cash.Charges()[0].Total().Amount() != "0.0000" ||
			len(cash.Charges()[0].Components()) != 0 {
			t.Fatal(cash, err)
		}
	}
}
