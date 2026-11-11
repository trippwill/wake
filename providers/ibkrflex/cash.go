// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package ibkrflex

import (
	"maps"
	"strings"

	"go.trippwill.dev/wake/financial"
	"go.trippwill.dev/wake/provider"
	"go.trippwill.dev/wake/source"
)

func cashCategory(kind string) source.CashCategory {
	switch kind {
	case "Deposits/Withdrawals":
		return source.CashFunding
	case "Internal Transfers", "Account Transfers":
		return source.CashTransfer
	case "Dividends":
		return source.CashDividend
	case "Payment In Lieu Of Dividends", "Payment in Lieu of Dividends", "Payment In Lieu":
		return source.CashPaymentInLieu
	case "Broker Interest Paid", "Broker Interest Received", "Bond Interest Paid", "Bond Interest Received":
		return source.CashInterest
	case "Withholding Tax", "871(m) Withholding":
		return source.CashWithholding
	case "Other Fees", "Advisor Fees", "Client Fees", "Broker Fees":
		return source.CashFee
	case "Sales Tax", "Transaction Tax":
		return source.CashTax
	case "Cash Adjustment", "Cash Adjustments", "Adjustment":
		return source.CashAdjustment
	default:
		return source.CashOther
	}
}

func cashPosting(row map[string]string, parent, loc string) (source.ActivityObservation, error) {
	if !safeText(row["type"], 1024) {
		return source.ActivityObservation{}, provider.ErrMalformed
	}

	return cashPostingAs(row, parent, loc, cashCategory(row["type"]))
}

func cashPostingAs(
	row map[string]string,
	parent, loc string,
	category source.CashCategory,
) (source.ActivityObservation, error) {
	if row["accountId"] != "" && row["accountId"] != parent {
		return source.ActivityObservation{}, provider.ErrMalformed
	}

	f := source.CashActivityInput{
		Location:     loc,
		Category:     category,
		ReportedType: row["type"],
		Description:  row["description"],
		Code:         row["code"],
	}

	var err error

	f.Account, err = source.NewReference("ibkr", "account", "", parent)
	if err != nil {
		return source.ActivityObservation{}, err
	}

	if id := nativeValue(row["transactionID"]); id != "" {
		f.Event, err = source.NewReference("ibkr", "cash-transaction-id", parent, id)
		if err != nil {
			return source.ActivityObservation{}, err
		}

		f.EventDistinct = true
	}

	if id := nativeValue(row["actionID"]); id != "" {
		f.Action, err = source.NewReference("ibkr", "cash-action-id", parent, id)
		if err != nil {
			return source.ActivityObservation{}, err
		}
	}

	c, err := financial.ParseCurrency(row["currency"])
	if err != nil {
		return source.ActivityObservation{}, err
	}

	f.Amount, err = financial.ParseMoney(row["amount"], c)
	if err != nil {
		return source.ActivityObservation{}, err
	}

	date := first(row["date"], row["dateTime"])
	if len(date) >= 10 && date[4] == '-' {
		date = date[:10]
	} else if len(date) >= 8 {
		date = date[:8]
	}

	f.Date, err = parseDate(date)
	if err != nil {
		return source.ActivityObservation{}, err
	}

	if at := row["dateTime"]; len(at) > 10 {
		f.At, err = parseReportedTimestamp(at)
		if err != nil {
			return source.ActivityObservation{}, err
		}
	}

	for _, entry := range []struct {
		key    string
		target *financial.Date
	}{{"reportDate", &f.ReportDate}, {"settleDate", &f.SettlementDate}, {"exDate", &f.ExDate}, {"availableForTradingDate", &f.AvailableForTradingDate}} {
		if row[entry.key] != "" {
			*entry.target, err = parseDate(row[entry.key])
			if err != nil {
				return source.ActivityObservation{}, err
			}
		}
	}

	if nativeValue(row["conid"]) != "" || row["isin"] != "" || row["cusip"] != "" || row["sedol"] != "" ||
		row["figi"] != "" ||
		row["securityID"] != "" || row["symbol"] != "" {
		ref, ids, e := rowSecurityReferences(row)
		if e != nil {
			return source.ActivityObservation{}, e
		}

		if !ref.IsZero() || len(ids) > 0 || row["symbol"] != "" {
			// A posting description and currency do not describe an instrument's name or issue terms.
			f.Instrument, err = source.NewInstrument(
				source.InstrumentInput{
					Reference:   ref,
					Identifiers: ids,
					Kind:        activityInstrumentKind(row["assetCategory"], row["subCategory"]),
					Ticker:      row["symbol"],
					Venue:       row["listingExchange"],
				},
			)
			if err != nil {
				return source.ActivityObservation{}, err
			}
		}
	}

	if id := nativeValue(row["taxableTransactionID"]); id != "" {
		ref, e := source.NewReference("ibkr", "cash-transaction-id", parent, id)
		if e != nil {
			return source.ActivityObservation{}, e
		}

		r, e := source.NewRelationship(source.TaxLink, ref)
		if e != nil {
			return source.ActivityObservation{}, e
		}

		f.Relationships = append(f.Relationships, r)
	}
	// An explicit trade reference identifies a trade component, not a second income event.
	if id := nativeValue(
		row["tradeID"],
	); id != "" && category == source.CashInterest &&
		(row["type"] == "Bond Interest Paid" || row["type"] == "Bond Interest Received") {
		event, e := source.NewReference("ibkr", "trade-id", parent, id)
		if e != nil {
			return source.ActivityObservation{}, e
		}

		fields := []source.ActivityFieldInput{
			{Name: source.FieldFamily, Text: "trade"},
			{Name: source.FieldAccruedInterest, Money: f.Amount, ReportedOn: f.ReportDate},
		}
		if f.Instrument.Kind() != "" {
			fields = append(
				fields,
				source.ActivityFieldInput{Name: source.FieldInstrument, Instrument: f.Instrument},
			)
		}

		return activityObservation(loc, f.Account, event, false, fields)
	}

	if id := nativeValue(row["tradeID"]); id != "" {
		ref, e := source.NewReference("ibkr", "trade-id", parent, id)
		if e != nil {
			return source.ActivityObservation{}, e
		}

		link, e := source.NewRelationship(source.RelatedTradeLink, ref)
		if e != nil {
			return source.ActivityObservation{}, e
		}

		f.Relationships = append(f.Relationships, link)
	}

	v, err := source.NewCashActivity(f)
	if err != nil {
		return source.ActivityObservation{}, err
	}

	return v.Observation(), nil
}

func activityObservation(
	loc string,
	account, event source.Reference,
	distinct bool,
	fields []source.ActivityFieldInput,
) (source.ActivityObservation, error) {
	f := source.ActivityObservationInput{Location: loc, Account: account, Event: event, EventDistinct: distinct}
	for _, input := range fields {
		value, e := source.NewActivityField(input)
		if e != nil {
			return source.ActivityObservation{}, e
		}

		f.Fields = append(f.Fields, value)
	}

	return source.NewActivityObservation(f)
}

func transferActivities(row map[string]string, parent, loc string) ([]source.ActivityObservation, error) {
	claims := []source.ActivityObservation{}

	security := false
	if row["quantity"] != "" {
		q, err := financial.ParseQuantity(row["quantity"])
		if err != nil {
			return nil, err
		}

		security = q.Abs().Positive() && row["assetCategory"] != "CASH"
	}

	if security {
		claim, err := securityTransfer(row, parent, loc)
		if err != nil {
			return nil, err
		}

		claims = append(claims, claim)
	}

	if row["cashTransfer"] != "" {
		c, err := financial.ParseCurrency(row["currency"])
		if err != nil {
			return nil, err
		}

		m, err := financial.ParseMoney(row["cashTransfer"], c)
		if err != nil {
			return nil, err
		}

		q, _ := financial.ParseQuantity(m.Amount())
		if q.Abs().Positive() {
			copyRow := maps.Clone(row)
			copyRow["amount"] = m.Amount()
			// CashTransfer is preserved with its reported sign. Direction is not a sign convention.
			claim, e := cashPostingAs(copyRow, parent, loc+"/cash", source.CashTransfer)
			if e != nil {
				return nil, e
			}

			f := claim.Fields()
			if security && nativeValue(row["transactionID"]) != "" {
				r, _ := source.NewReference("ibkr", "security-transfer-id", parent, row["transactionID"])
				link, _ := source.NewRelationship(source.TransferLink, r)
				field, _ := source.NewActivityField(
					source.ActivityFieldInput{Name: source.FieldRelationship, Relationship: link},
				)
				f.Fields = append(f.Fields, field)
			}

			claim, e = source.NewActivityObservation(f)
			if e != nil {
				return nil, e
			}

			claims = append(claims, claim)
		}
	}

	return claims, nil
}

func salesTaxRow(row map[string]string) map[string]string {
	r := maps.Clone(row)
	r["amount"] = row["salesTax"]
	r["type"] = row["taxType"]
	r["description"] = strings.TrimSpace(row["taxType"] + " " + row["taxableDescription"])

	return r
}
