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
		case row["transactionType"] != "ExchTrade" && row["transactionType"] != "BookTrade":
			reason = "booking-not-accepted"
		case row["assetCategory"] != "STK" && row["assetCategory"] != "OPT" && row["assetCategory"] != "BOND" && row["assetCategory"] != "BILL" && row["assetCategory"] != "CASH":
			reason = "family-not-accepted"
		}

		if reason != "" {
			counts.Rejected++
			return sink.Reject(ctx, location(path), reason)
		}

		correction, hasCorrection, e := sourceCorrection(row, path[2].attributes["accountId"], location(path))
		if e != nil {
			counts.Rejected++
			return sink.Reject(ctx, location(path), "invalid-source-correction")
		}

		if hasCorrection {
			if correction.Fields().Kind != source.Replacement {
				if e = sink.PutCorrection(ctx, correction); e != nil {
					return e
				}

				counts.Corrections++

				return nil
			}

			if e = sink.PutCorrection(ctx, correction); e != nil {
				return e
			}

			counts.Corrections++
			counts.Rows++
		}

		if row["assetCategory"] == "CASH" {
			c, e := conversion(row, path[2].attributes["accountId"], location(path))
			if e != nil {
				counts.Rejected++
				return sink.Reject(ctx, location(path), "invalid-currency-conversion")
			}

			if e = sink.PutConversion(ctx, c); e != nil {
				return e
			}

			counts.Conversions++

			return nil
		}

		if row["transactionType"] == "BookTrade" && row["assetCategory"] == "OPT" {
			claim, e := optionLifecycle(row, path[2].attributes["accountId"], location(path))
			if e != nil {
				counts.Rejected++
				return sink.Reject(ctx, location(path), "option-outcome-needs-review")
			}

			if e = sink.PutLifecycle(ctx, claim); e != nil {
				return e
			}

			counts.Lifecycles++

			return nil
		}

		claim, e := equity(row, path[2].attributes["accountId"], location(path))
		if e != nil {
			counts.Rejected++
			return sink.Reject(ctx, location(path), "invalid-security-execution")
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

func rowFields(row map[string]string, parent, loc string) (source.TradeInput, error) {
	accountID := first(row["accountId"], parent)
	if row["accountId"] != "" && row["accountId"] != parent {
		return source.TradeInput{}, provider.ErrMalformed
	}

	account, err := source.NewReference("ibkr", "account", "", accountID)
	if err != nil {
		return source.TradeInput{}, err
	}

	var native source.Reference
	if id := nativeValue(row["conid"]); id != "" {
		native, err = source.NewReference("ibkr", "contract-id", "", id)
		if err != nil {
			return source.TradeInput{}, err
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
			return source.TradeInput{}, e
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
				return source.TradeInput{}, e
			}

			ids = append(ids, id)
		}
	}

	kind := source.Equity
	if row["subCategory"] == "ETF" {
		kind = source.Fund
	}

	option := source.OptionTerms{}
	if row["assetCategory"] == "OPT" {
		kind = source.Option

		option, err = optionTerms(row)
		if err != nil {
			return source.TradeInput{}, err
		}
	}

	debt := source.DebtTerms{}
	if row["assetCategory"] == "BOND" || row["assetCategory"] == "BILL" {
		kind = source.Debt

		c, e := financial.ParseCurrency(row["currency"])
		if e != nil {
			return source.TradeInput{}, e
		}

		d := source.DebtInput{Currency: c}
		if row["assetCategory"] == "BILL" && row["issuerCountryCode"] == "US" {
			d.Type = source.TreasuryBill
		}

		debt, err = source.NewDebtTerms(d)
		if err != nil {
			return source.TradeInput{}, err
		}
	}

	instrument, err := source.NewInstrument(
		source.InstrumentInput{
			Reference:   native,
			Kind:        kind,
			Ticker:      row["symbol"],
			Name:        row["description"],
			Venue:       row["listingExchange"],
			Identifiers: ids,
			Option:      option,
			Debt:        debt,
		},
	)
	if err != nil {
		return source.TradeInput{}, err
	}

	currency := financial.Currency{}
	if row["currency"] != "" {
		currency, err = financial.ParseCurrency(row["currency"])
	}

	if err != nil {
		return source.TradeInput{}, err
	}

	quantity, err := financial.NewQuantity(row["quantity"])
	if err != nil {
		return source.TradeInput{}, err
	}

	price := financial.Price{}
	if row["price"] != "" {
		price, err = financial.NewPrice(row["price"], currency)
	}

	if err != nil {
		return source.TradeInput{}, err
	}

	date, err := parseDate(row["tradeDate"])
	if err != nil {
		return source.TradeInput{}, err
	}

	side := source.Side("")
	switch row["buySell"] {
	case "BUY":
		side = source.Buy
	case "SELL":
		side = source.Sell
	case "":
	default:
		return source.TradeInput{}, provider.ErrMalformed
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
			return source.TradeInput{}, e
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
			return source.TradeInput{}, err
		}
	}

	if value := row["settleDate"]; value != "" {
		fields.SettlementDate, err = parseDate(value)
		if err != nil {
			return source.TradeInput{}, err
		}
	}

	if value := row["dateTime"]; value != "" {
		fields.ExecutedAt, err = parseReportedTimestamp(value)
		if err != nil {
			return source.TradeInput{}, err
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
		return source.TradeInput{}, provider.ErrMalformed
	}

	for _, entry := range []struct {
		field  string
		target *financial.Money
	}{{"amount", &fields.Gross}, {"proceeds", &fields.Proceeds}, {"netCash", &fields.Net}, {"accruedInt", &fields.AccruedInterest}, {"netCashWithBillable", &fields.NetWithBillable}} {
		if row[entry.field] != "" {
			*entry.target, err = financial.NewMoney(row[entry.field], currency)
			if err != nil {
				return source.TradeInput{}, err
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
				return source.TradeInput{}, err
			}
		}

		amount, e := financial.NewMoney(row[entry.field], cc)
		if e != nil {
			return source.TradeInput{}, e
		}

		charge, e := source.NewCharge(entry.field, entry.scope, amount)
		if e != nil {
			return source.TradeInput{}, e
		}

		fields.Charges = append(fields.Charges, charge)
	}

	if row["transactionType"] == "BookTrade" {
		fields.Kind = source.StockBooking
	}

	return fields, nil
}

func optionTerms(row map[string]string) (source.OptionTerms, error) {
	f := source.OptionInput{}

	var err error
	if id := nativeValue(row["underlyingConid"]); id != "" {
		r, e := source.NewReference("ibkr", "contract-id", "", id)
		if e != nil {
			return source.OptionTerms{}, e
		}

		f.Underlier, err = source.NewInstrument(
			source.InstrumentInput{
				Reference: r,
				Kind:      source.UnknownInstrument,
				Ticker:    row["underlyingSymbol"],
				Venue:     row["underlyingListingExchange"],
			},
		)
		if err != nil {
			return source.OptionTerms{}, err
		}
	}

	switch row["putCall"] {
	case "":
	case "P":
		f.Right = source.Put
	case "C":
		f.Right = source.Call
	default:
		return source.OptionTerms{}, provider.ErrMalformed
	}

	if row["expiry"] != "" {
		f.Expiry, err = parseDate(row["expiry"])
		if err != nil {
			return source.OptionTerms{}, err
		}
	}

	if row["strike"] != "" {
		c, e := financial.ParseCurrency(row["currency"])
		if e != nil {
			return source.OptionTerms{}, e
		}

		f.Strike, err = financial.NewPrice(row["strike"], c)
		if err != nil {
			return source.OptionTerms{}, err
		}
	}

	if row["multiplier"] != "" {
		f.Multiplier, err = financial.NewQuantity(row["multiplier"])
		if err != nil {
			return source.OptionTerms{}, err
		}
	}

	return source.NewOptionTerms(f)
}

func equity(row map[string]string, parent, loc string) (source.Trade, error) {
	f, err := rowFields(row, parent, loc)
	if err != nil {
		return source.Trade{}, err
	}

	if row["transactionType"] == "BookTrade" && outcome(row) == "" {
		return source.Trade{}, provider.ErrMalformed
	}

	return source.NewTrade(f)
}

func outcome(row map[string]string) source.LifecycleKind {
	var kind source.LifecycleKind
	for _, code := range strings.Split(row["code"], ";") {
		var next source.LifecycleKind
		switch code {
		case "Ex":
			next = source.Exercise
		case "A":
			next = source.Assignment
		case "Ep":
			next = source.Expiration
		case "GEA":
			return ""
		}

		if next != "" {
			if kind != "" && kind != next {
				return ""
			}

			kind = next
		}
	}

	return kind
}

func optionLifecycle(row map[string]string, parent, loc string) (source.Lifecycle, error) {
	f, err := rowFields(row, parent, loc)
	if err != nil {
		return source.Lifecycle{}, err
	}

	return source.NewLifecycle(
		source.LifecycleInput{
			Location:     loc,
			Account:      f.Account,
			Event:        f.Event,
			EventAliases: f.EventAliases,
			Order:        f.Order,
			Instrument:   f.Instrument,
			Kind:         outcome(row),
			Quantity:     f.Quantity,
			Date:         f.TradeDate,
			At:           f.ExecutedAt,
		},
	)
}

func conversion(row map[string]string, parent, loc string) (source.Conversion, error) {
	if row["transactionType"] != "ExchTrade" {
		return source.Conversion{}, provider.ErrMalformed
	}

	f, err := rowFields(row, parent, loc)
	if err != nil {
		return source.Conversion{}, err
	}

	pair := strings.Split(row["symbol"], ".")
	if len(pair) != 2 {
		return source.Conversion{}, provider.ErrMalformed
	}

	base, err := financial.ParseCurrency(pair[0])
	if err != nil {
		return source.Conversion{}, err
	}

	quote, err := financial.ParseCurrency(pair[1])
	if err != nil || quote != f.Price.Currency() {
		return source.Conversion{}, provider.ErrMalformed
	}

	baseAmount, err := source.NewCurrencyAmount(base, f.Quantity.Value())
	if err != nil {
		return source.Conversion{}, err
	}

	quoteText := ""
	if f.Gross.Amount() != "" {
		q, e := financial.NewQuantity(f.Gross.Amount())
		if e != nil {
			return source.Conversion{}, e
		}

		quoteText = q.Abs().Value()
	}

	quoteAmount, err := source.NewCurrencyAmount(quote, quoteText)
	if err != nil {
		return source.Conversion{}, err
	}

	sent, received := quoteAmount, baseAmount
	if f.Side == source.Sell {
		sent, received = baseAmount, quoteAmount
	} else if f.Side != source.Buy {
		return source.Conversion{}, provider.ErrMalformed
	}

	return source.NewConversion(
		source.ConversionInput{
			Location:        loc,
			Account:         f.Account,
			Event:           f.Event,
			EventAliases:    f.EventAliases,
			Order:           f.Order,
			Date:            f.TradeDate,
			At:              f.ExecutedAt,
			Sent:            sent,
			Received:        received,
			RateBase:        base,
			Rate:            f.Price,
			Gross:           f.Gross,
			Proceeds:        f.Proceeds,
			Net:             f.Net,
			NetWithBillable: f.NetWithBillable,
			Charges:         f.Charges,
		},
	)
}

func sourceCorrection(row map[string]string, parent, loc string) (source.Correction, bool, error) {
	ca, co := false, false
	for _, code := range strings.Split(row["code"], ";") {
		ca = ca || code == "Ca"
		co = co || code == "Co"
	}

	orig := nativeValue(row["origTradeID"])
	if !ca && !co && orig == "" && nativeValue(row["origTransactionID"]) == "" {
		return source.Correction{}, false, nil
	}

	accountID := first(row["accountId"], parent)
	if row["accountId"] != "" && row["accountId"] != parent {
		return source.Correction{}, true, provider.ErrMalformed
	}

	account, err := source.NewReference("ibkr", "account", "", accountID)
	if err != nil {
		return source.Correction{}, true, err
	}

	f := source.CorrectionInput{Location: loc, Account: account, Kind: source.UnclearCorrection}
	if orig != "" {
		f.Target, err = source.NewReference("ibkr", "trade-id", accountID, orig)
		if err != nil {
			return source.Correction{}, true, err
		}
	}

	if ca && !co && orig != "" && nativeValue(row["origTransactionID"]) == "" {
		f.Kind = source.Cancellation
	}

	if co && !ca && orig != "" && nativeValue(row["origTransactionID"]) == "" {
		f.Kind = source.Replacement
		f.Location += "/correction"

		kind, value := "execution-id", nativeValue(row["execID"])
		if value == "" {
			kind, value = "trade-id", nativeValue(row["tradeID"])
		}

		if value != "" {
			f.Replacement, err = source.NewReference("ibkr", kind, accountID, value)
			if err != nil {
				return source.Correction{}, true, err
			}
		}
	}

	c, err := source.NewCorrection(f)

	return c, true, err
}
