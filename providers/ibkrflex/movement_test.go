// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package ibkrflex

import (
	"strings"
	"testing"
	"time"

	"go.trippwill.dev/wake/provider"
	"go.trippwill.dev/wake/source"
)

func TestCashCategoriesPreserveReportedMeaning(t *testing.T) {
	for _, tc := range []struct {
		kind string
		want source.CashCategory
	}{
		{"Deposits/Withdrawals", source.CashFunding},
		{"Internal Transfers", source.CashTransfer},
		{"Dividends", source.CashDividend},
		{"Payment In Lieu Of Dividends", source.CashPaymentInLieu},
		{"Broker Interest Paid", source.CashInterest},
		{"Broker Interest Received", source.CashInterest},
		{"Bond Interest Received", source.CashInterest},
		{"Withholding Tax", source.CashWithholding},
		{"Other Fees", source.CashFee},
		{"Sales Tax", source.CashTax},
		{"Cash Adjustment", source.CashAdjustment},
		{"Unfamiliar Reported Posting", source.CashOther},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			row := map[string]string{
				"accountId":     "U1",
				"transactionID": "1",
				"type":          tc.kind,
				"amount":        "-0.00000000012300",
				"currency":      "USD",
				"dateTime":      "2026-10-20",
				"actionID":      "shared",
			}

			o, err := cashPosting(row, "U1", "/cash")
			if err != nil {
				t.Fatal(err)
			}

			v, err := o.CashActivity()
			if err != nil || v.Fields().Category != tc.want || v.Fields().Amount.Amount() != row["amount"] ||
				!v.Fields().At.IsZero() ||
				v.Fields().ReportedType != tc.kind ||
				len(v.Fields().EventAliases) != 0 {
				t.Fatal(v, err)
			}
		})
	}
}

func TestActivityCashDetailAndTaxRowsExcludeSummaries(t *testing.T) {
	reg, _ := Registration(Options{})
	row := `<CashTransaction accountId="U1" transactionID="1" type="Other Fees" amount="1.0000" currency="USD" dateTime="2026-10-20" levelOfDetail="DETAIL"/>`
	body := `<FlexQueryResponse type="AF"><FlexStatements><FlexStatement accountId="U1"><CashTransactions>` + row + strings.ReplaceAll(
		row,
		`levelOfDetail="DETAIL"`,
		`levelOfDetail="SUMMARY"`,
	) + `</CashTransactions><SalesTaxes><SalesTax transactionID="2" taxableTransactionID="1" currency="USD" salesTax="-0.2000" date="2026-10-20"/></SalesTaxes></FlexStatement></FlexStatements></FlexQueryResponse>`
	a, _ := provider.NewArtifact(ActivityFormat, "application/xml", time.Now(), []byte(body))
	sink := &testClaims{}

	r, err := reg.Normalizer().Normalize(t.Context(), a, sink)
	if err != nil || r.Counts().Activities != 2 || r.Ignored() != 1 || r.Rejected() != 0 {
		t.Fatal(r, err)
	}

	var seen []source.CashActivity
	for _, claim := range sink.activities {
		v, e := claim.CashActivity()
		if e != nil {
			t.Fatal(e)
		}

		seen = append(seen, v)
	}

	if len(seen) != 2 || seen[0].Fields().Amount.Amount() != "1.0000" ||
		seen[1].Fields().Relationships[0].Kind() != source.TaxLink {
		t.Fatal(seen)
	}
}

func TestCashUnsupportedSecurityIdentifiersLeaveFinancialTermsAvailable(t *testing.T) {
	reg, _ := Registration(Options{})
	for _, tc := range []struct {
		name, attributes, reference, ticker string
		identifiers                         int
		instrument                          bool
	}{
		{"unsupported-only", `securityID="synthetic-local" securityIDType="LOCAL"`, "", "", 0, false},
		{"untyped-only", `securityID="synthetic-local"`, "", "", 0, false},
		{"native-reference", `securityID="synthetic-local" securityIDType="LOCAL" conid="100"`, "100", "", 0, true},
		{"reported-symbol", `securityID="synthetic-local" securityIDType="LOCAL" symbol="SYN"`, "", "SYN", 0, true},
		{"supported-identifier", `securityID="US0378331005" securityIDType="ISIN"`, "", "", 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := `<FlexQueryResponse type="AF"><FlexStatements><FlexStatement accountId="U1"><CashTransactions><CashTransaction ` +
				tc.attributes + ` transactionID="cash1" type="Dividends" amount="12.34000000000000001" currency="USD" dateTime="2026-10-20" levelOfDetail="DETAIL"/></CashTransactions></FlexStatement></FlexStatements></FlexQueryResponse>`
			artifact, _ := provider.NewArtifact(ActivityFormat, "application/xml", time.Now(), []byte(body))

			sink := &testClaims{}
			result, err := reg.Normalizer().Normalize(t.Context(), artifact, sink)
			if err != nil || result.Counts().Activities != 1 || result.Rejected() != 0 ||
				len(sink.activities) != 1 {
				t.Fatal("complete cash posting rejected", result, err)
			}

			value, err := sink.activities[0].CashActivity()
			if err != nil {
				t.Fatal(err)
			}

			f := value.Fields()
			if f.Amount.Amount() != "12.34000000000000001" || f.Category != source.CashDividend ||
				f.Date.String() != "2026-10-20" || f.Instrument.Reference().Value() != tc.reference ||
				f.Instrument.Ticker() != tc.ticker || len(f.Instrument.Identifiers()) != tc.identifiers {
				t.Fatal("cash or supported instrument terms changed", f)
			}

			if _, present := sink.activities[0].Field(source.FieldInstrument); present != tc.instrument {
				t.Fatal("unsupported identifier established an instrument", f.Instrument)
			}
		})
	}
}

func TestSecurityTransferDirectionUnitsAndMixedCash(t *testing.T) {
	row := map[string]string{
		"accountId":     "U1",
		"transactionID": "1",
		"assetCategory": "STK",
		"conid":         "100",
		"currency":      "USD",
		"date":          "2026-10-20",
		"direction":     "OUT",
		"quantity":      "-2.0000",
		"cashTransfer":  "-10.5000",
		"type":          "ACATS",
	}

	claims, err := transferActivities(row, "U1", "/transfer")
	if err != nil || len(claims) != 2 {
		t.Fatal(claims, err)
	}

	v, err := claims[0].SecurityTransfer()
	if err != nil || v.Fields().Direction != source.TransferOut || v.Fields().Quantity.Value() != "2.0000" {
		t.Fatal(v, err)
	}

	c, err := claims[1].CashActivity()
	if err != nil || c.Fields().Amount.Amount() != "-10.5000" || c.Fields().ReportedType != "ACATS" ||
		len(c.Fields().Relationships) != 1 {
		t.Fatal(c, err)
	}

	row["cashTransfer"] = "0.0000"

	claims, err = transferActivities(row, "U1", "/transfer")
	if err != nil || len(claims) != 1 {
		t.Fatal(claims, err)
	}

	row["direction"] = "sideways"
	if _, err = transferActivities(row, "U1", "/transfer"); err == nil {
		t.Fatal("invalid direction accepted")
	}
}

func TestCashTradeComponentRemainsPartial(t *testing.T) {
	row := map[string]string{
		"accountId":     "U1",
		"transactionID": "cash",
		"tradeID":       "trade",
		"type":          "Bond Interest Paid",
		"amount":        "-12.3400",
		"currency":      "USD",
		"dateTime":      "2026-10-20",
	}

	o, err := cashPosting(row, "U1", "/cash")
	if err != nil {
		t.Fatal(err)
	}

	f, _ := o.Field(source.FieldAccruedInterest)
	if f.Fields().Money.Amount() != row["amount"] || o.Fields().Event.Kind() != "trade-id" {
		t.Fatal(o)
	}

	if _, err = o.CashActivity(); err == nil {
		t.Fatal("trade component became cash activity")
	}

	if _, err = o.Trade(); err == nil {
		t.Fatal("component invented complete trade")
	}
}

func TestCashRelatedTradeReferenceDoesNotEstablishEventIdentity(t *testing.T) {
	row := map[string]string{
		"transactionID": "fee",
		"tradeID":       "trade",
		"type":          "Other Fees",
		"amount":        "-1.2300",
		"currency":      "USD",
		"dateTime":      "2026-10-20",
	}

	o, err := cashPosting(row, "U1", "/fee")
	if err != nil {
		t.Fatal(err)
	}

	v, err := o.CashActivity()
	if err != nil || v.Fields().Event.Kind() != "cash-transaction-id" || len(v.Fields().EventAliases) != 0 ||
		len(v.Fields().Relationships) != 1 ||
		v.Fields().Relationships[0].Kind() != source.RelatedTradeLink {
		t.Fatal(v, err)
	}

	row["conid"] = "100"
	row["description"] = "Synthetic cash event"

	o, err = cashPosting(row, "U1", "/fee")
	if err != nil {
		t.Fatal(err)
	}

	instrument, _ := o.Field(source.FieldInstrument)
	if instrument.Fields().Instrument.Kind() != source.UnknownInstrument {
		t.Fatal("missing asset category guessed instrument family")
	}

	row["assetCategory"] = "BOND"

	o, err = cashPosting(row, "U1", "/fee")
	if err != nil {
		t.Fatal(err)
	}

	instrument, _ = o.Field(source.FieldInstrument)
	if instrument.Fields().Instrument.Name() != "" || !instrument.Fields().Instrument.Debt().IsZero() {
		t.Fatal("cash description or currency became security terms")
	}
}

func TestActivityAbsentAndEmptySectionsRemainDistinct(t *testing.T) {
	reg, _ := Registration(Options{})
	for _, section := range []string{"Transfers", "CashTransactions", "SalesTaxes", "Unrelated"} {
		body := `<FlexQueryResponse type="AF"><FlexStatements><FlexStatement accountId="U1"><` + section + `/></FlexStatement></FlexStatements></FlexQueryResponse>`
		a, _ := provider.NewArtifact(ActivityFormat, "application/xml", time.Now(), []byte(body))
		sink := &testClaims{}

		r, err := reg.Normalizer().Normalize(t.Context(), a, sink)
		if err != nil {
			t.Fatal(err)
		}

		if section == "Unrelated" && r.Rejected() != 1 ||
			section != "Unrelated" && (r.Rows() != 0 || r.Rejected() != 0) {
			t.Fatal(section, r)
		}
	}
}
