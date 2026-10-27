// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package ibkrflex

import (
	"context"
	"strings"

	"go.trippwill.dev/wake/financial"
	"go.trippwill.dev/wake/provider"
	"go.trippwill.dev/wake/source"
)

func (s *Source) Normalize(
	ctx context.Context,
	artifact provider.Artifact,
	sink provider.Claims,
) (provider.Result, error) {
	if artifact.Format() != ConfirmationFormat {
		return provider.Result{}, provider.ErrUnavailable
	}

	body := artifact.Content()
	if int64(len(body)) > s.limit {
		return provider.Result{}, provider.ErrLimit
	}

	metadata, err := inspectReport(ctx, body, "TCF", nil)
	if err != nil {
		return provider.Result{}, err
	}

	counts := provider.Counts{}
	if metadata.tradeSections == 0 {
		if err = sink.Reject(ctx, "/FlexQueryResponse[1]", "trade-section-not-reported"); err != nil {
			return provider.Result{}, err
		}

		counts.Rows++
		counts.Rejected++
	}

	_, err = inspectReport(ctx, body, "TCF", func(path []node) error {
		counts.Rows++
		if counts.Rows > 100000 {
			return provider.ErrLimit
		}

		row := path[len(path)-1].attributes
		reason := ""
		switch {
		case row["levelOfDetail"] != "EXECUTION":
			reason = "summary-not-accepted"
		case row["transactionType"] != "ExchTrade":
			reason = "booking-not-accepted"
		case row["assetCategory"] != "STK":
			reason = "family-not-accepted"
		case nativeValue(row["origTradeID"]) != "" || nativeValue(row["origTransactionID"]) != "":
			reason = "source-correction-needs-review"
		}

		if reason != "" {
			counts.Rejected++
			return sink.Reject(ctx, location(path), reason)
		}

		claim, e := equity(row, path[2].attributes["accountId"], location(path))
		if e != nil {
			counts.Rejected++
			return sink.Reject(ctx, location(path), "invalid-equity-execution")
		}

		if e = sink.PutTrade(ctx, claim); e != nil {
			return e
		}

		counts.Trades++

		return nil
	})
	if err != nil {
		return provider.Result{}, err
	}

	return provider.NewResult(counts)
}

func equity(row map[string]string, parent, loc string) (source.Trade, error) {
	accountID := first(row["accountId"], parent)
	if row["accountId"] != "" && row["accountId"] != parent {
		return source.Trade{}, provider.ErrMalformed
	}

	account, err := source.NewReference("ibkr", "account", "", accountID)
	if err != nil {
		return source.Trade{}, err
	}

	var native source.Reference
	if id := nativeValue(row["conid"]); id != "" {
		native, err = source.NewReference("ibkr", "contract-id", "", id)
		if err != nil {
			return source.Trade{}, err
		}
	}

	ids := []source.Identifier{}
	for _, entry := range []struct {
		field string
		kind  source.IdentifierKind
	}{{"isin", source.ISIN}, {"cusip", source.CUSIP}, {"sedol", source.SEDOL}, {"figi", source.UnclassifiedFIGI}} {
		if row[entry.field] == "" {
			continue
		}

		id, e := source.NewIdentifier(entry.kind, row[entry.field], entry.field)
		if e != nil {
			return source.Trade{}, e
		}

		ids = append(ids, id)
	}

	if value := row["securityID"]; value != "" {
		kind := source.IdentifierKind(strings.ToLower(row["securityIDType"]))
		if kind == "figi" {
			kind = source.UnclassifiedFIGI
		}

		if kind == source.ISIN || kind == source.CUSIP || kind == source.SEDOL ||
			kind == source.UnclassifiedFIGI {
			id, e := source.NewIdentifier(kind, value, "securityID")
			if e != nil {
				return source.Trade{}, e
			}

			ids = append(ids, id)
		}
	}

	kind := source.Equity
	if row["subCategory"] == "ETF" {
		kind = source.Fund
	}

	instrument, err := source.NewInstrument(
		source.InstrumentInput{
			Reference:   native,
			Kind:        kind,
			Ticker:      row["symbol"],
			Name:        row["description"],
			Venue:       row["listingExchange"],
			Identifiers: ids,
		},
	)
	if err != nil {
		return source.Trade{}, err
	}

	currency, err := financial.ParseCurrency(row["currency"])
	if err != nil {
		return source.Trade{}, err
	}

	quantity, err := financial.NewQuantity(row["quantity"])
	if err != nil {
		return source.Trade{}, err
	}

	price, err := financial.NewPrice(row["price"], currency)
	if err != nil {
		return source.Trade{}, err
	}

	date, err := parseDate(row["tradeDate"])
	if err != nil {
		return source.Trade{}, err
	}

	side := source.Side("")
	switch row["buySell"] {
	case "BUY":
		side = source.Buy
	case "SELL":
		side = source.Sell
	default:
		return source.Trade{}, provider.ErrMalformed
	}

	fields := source.TradeInput{
		Location:   loc,
		Account:    account,
		Instrument: instrument,
		Side:       side,
		Quantity:   quantity.Abs(),
		Price:      price,
		TradeDate:  date,
	}
	for _, entry := range []struct{ field, kind string }{{"execID", "execution-id"}, {"tradeID", "trade-id"}} {
		value := nativeValue(row[entry.field])
		if value == "" {
			continue
		}

		ref, e := source.NewReference("ibkr", entry.kind, accountID, value)
		if e != nil {
			return source.Trade{}, e
		}

		if fields.Event.IsZero() {
			fields.Event = ref
		} else {
			fields.EventAliases = append(fields.EventAliases, ref)
		}
	}

	if id := nativeValue(row["orderID"]); id != "" {
		fields.Order, err = source.NewReference("ibkr", "order-id", accountID, id)
		if err != nil {
			return source.Trade{}, err
		}
	}

	if value := row["settleDate"]; value != "" {
		fields.SettlementDate, err = parseDate(value)
		if err != nil {
			return source.Trade{}, err
		}
	}

	if value := row["dateTime"]; value != "" {
		fields.ExecutedAt, err = parseReportedTimestamp(value)
		if err != nil {
			return source.Trade{}, err
		}
	}

	qualification := row["openCloseIndicator"]
	if qualification == "" {
		for _, code := range strings.Split(row["code"], ";") {
			if code == "O" || code == "C" {
				if qualification != "" {
					qualification += ";"
				}

				qualification += code
			}
		}
	}

	switch qualification {
	case "":
	case "O":
		fields.PositionEffect = source.Opening
	case "C":
		fields.PositionEffect = source.Closing
	case "O;C", "C;O":
		fields.PositionEffect = source.Mixed
	default:
		return source.Trade{}, provider.ErrMalformed
	}

	for _, entry := range []struct {
		field  string
		target *financial.Money
	}{{"amount", &fields.Gross}, {"proceeds", &fields.Proceeds}, {"netCash", &fields.Net}} {
		if row[entry.field] != "" {
			*entry.target, err = financial.NewMoney(row[entry.field], currency)
			if err != nil {
				return source.Trade{}, err
			}
		}
	}

	for _, entry := range []struct{ field, scope string }{{"commission", "total"}, {"brokerExecutionCommission", "component"}, {"brokerClearingCommission", "component"}, {"thirdPartyExecutionCommission", "component"}, {"thirdPartyClearingCommission", "component"}, {"thirdPartyRegulatoryCommission", "component"}, {"regFINRATradingActivityFee", "component"}, {"regSection31TransactionFee", "component"}, {"regOther", "component"}, {"otherCommission", "component"}, {"tax", "total"}, {"salesTax", "component"}, {"tradeCharge", "total"}, {"otherTax", "component"}} {
		if row[entry.field] == "" {
			continue
		}

		cc := currency
		if strings.Contains(strings.ToLower(entry.field), "commission") {
			cc, err = financial.ParseCurrency(row["commissionCurrency"])
			if err != nil {
				return source.Trade{}, err
			}
		}

		amount, e := financial.NewMoney(row[entry.field], cc)
		if e != nil {
			return source.Trade{}, e
		}

		charge, e := source.NewCharge(entry.field, entry.scope, amount)
		if e != nil {
			return source.Trade{}, e
		}

		fields.Charges = append(fields.Charges, charge)
	}

	return source.NewTrade(fields)
}
