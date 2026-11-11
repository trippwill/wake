// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package snaptrade

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"go.trippwill.dev/wake/financial"
	"go.trippwill.dev/wake/provider"
	"go.trippwill.dev/wake/source"
)

type symbol struct {
	ID        string `json:"id"`
	Ticker    string `json:"symbol"`
	RawTicker string `json:"raw_symbol"`
	Name      string `json:"description"`
	ISIN      string `json:"isin"`
	CUSIP     string `json:"cusip"`
	FIGI      string `json:"figi_code"`
	Type      struct {
		Code string `json:"code"`
	} `json:"type"`
	Currency struct {
		Code string `json:"code"`
	} `json:"currency"`
	Exchange struct {
		MIC string `json:"mic_code"`
	} `json:"exchange"`
	FIGIs struct {
		Venue      string `json:"figi_code"`
		ShareClass string `json:"figi_share_class"`
	} `json:"figi_instrument"`
}

type option struct {
	ID        string          `json:"id"`
	Ticker    string          `json:"ticker"`
	Right     string          `json:"option_type"`
	Strike    json.RawMessage `json:"strike_price"`
	Expiry    string          `json:"expiration_date"`
	Mini      bool            `json:"is_mini_option"`
	Underlier *symbol         `json:"underlying_symbol"`
}

type activity struct {
	Type           string          `json:"type"`
	OptionAction   string          `json:"option_type"`
	Units          json.RawMessage `json:"units"`
	Price          json.RawMessage `json:"price"`
	Fee            json.RawMessage `json:"fee"`
	Date           string          `json:"trade_date"`
	SettlementDate string          `json:"settlement_date"`
	Currency       struct {
		Code string `json:"code"`
	} `json:"currency"`
	Symbol *symbol `json:"symbol"`
	Option *option `json:"option_symbol"`
}

func (a *adapter) Normalize(
	ctx context.Context,
	artifact provider.Artifact,
	sink provider.Claims,
) (provider.Result, error) {
	if artifact.Format() != format && artifact.Format() != positionsFormat {
		return provider.Result{}, provider.ErrUnavailable
	}

	body := artifact.Content()
	if int64(len(body)) > a.limit {
		return provider.Result{}, provider.ErrLimit
	}

	if err := validateJSON(ctx, body); err != nil {
		return provider.Result{}, err
	}

	if artifact.Format() == positionsFormat {
		return normalizePositions(ctx, artifact.SourceAccount(), body, sink)
	}

	// Acquisition preflight also preserves exact authorization and account
	// responses, which contribute no financial observations.
	if bytes.HasPrefix(bytes.TrimSpace(body), []byte("[")) {
		_, err := authorizationScopes(body)
		return provider.Result{}, err
	}

	var envelope struct {
		Results json.RawMessage `json:"results"`
		Data    json.RawMessage `json:"data"`
	}
	if json.Unmarshal(body, &envelope) != nil {
		return provider.Result{}, provider.ErrMalformed
	}

	if envelope.Results != nil && envelope.Data == nil {
		_, err := candidateAccounts(body, nil)
		return provider.Result{}, err
	}

	page, ids, err := parsePage(body)
	if err != nil {
		return provider.Result{}, err
	}

	account := artifact.SourceAccount()

	scope, err := sourceScope(account)
	if err != nil {
		return provider.Result{}, err
	}

	counts := provider.Counts{Rows: uint64(len(page.Data))}
	for i, raw := range page.Data {
		if err = ctx.Err(); err != nil {
			return provider.Result{}, err
		}

		location := "/data/" + strconv.Itoa(i)

		var row activity

		reason := "invalid-activity"
		if ids[i] != "" && json.Unmarshal(raw, &row) == nil {
			if row.Type != "BUY" && row.Type != "SELL" {
				reason = "unsupported-activity"
			} else {
				trade, err := row.trade(ids[i], location, account, scope.Broker == fidelity)
				if err == nil {
					if err = sink.PutActivity(ctx, trade.Observation()); err != nil {
						return provider.Result{}, err
					}

					counts.Activities++

					continue
				}

				if err == provider.ErrUnavailable {
					reason = "unsupported-instrument"
				}
			}
		}

		if err = sink.Reject(ctx, location, reason); err != nil {
			return provider.Result{}, err
		}

		counts.Rejected++
	}

	return provider.NewResult(counts)
}

func number(raw json.RawMessage) string {
	if len(raw) == 0 || raw[0] != '-' && (raw[0] < '0' || raw[0] > '9') {
		return ""
	}

	return string(raw)
}

func reportedDate(value string) (financial.Date, financial.Timestamp, error) {
	if len(value) < 10 {
		return financial.Date{}, financial.Timestamp{}, provider.ErrMalformed
	}

	date, err := financial.ParseDate(value[:10])
	if err != nil || len(value) == 10 {
		return date, financial.Timestamp{}, err
	}

	at, err := financial.ParseTimestamp(value)

	return date, at, err
}

func (row activity) trade(
	id, location string,
	account source.Reference,
	dateOnly bool,
) (source.Trade, error) {
	currency, err := financial.ParseCurrency(row.Currency.Code)
	if err != nil {
		return source.Trade{}, err
	}

	quantity, err := financial.ParseQuantity(number(row.Units))
	if err != nil {
		return source.Trade{}, err
	}

	price, err := financial.ParsePrice(number(row.Price), currency)
	if err != nil || strings.HasPrefix(price.Amount(), "-") {
		return source.Trade{}, provider.ErrMalformed
	}

	date, at, err := reportedDate(row.Date)
	if err != nil {
		return source.Trade{}, err
	}

	if dateOnly {
		at = financial.Timestamp{}
	}

	event, err := source.NewReference("snaptrade", "activity-id", account.Value(), id)
	if err != nil {
		return source.Trade{}, err
	}

	side := source.Buy
	if row.Type == "SELL" {
		side = source.Sell
	}

	input := source.TradeInput{
		Kind: source.ReportedTrade, Location: location, Account: account, Event: event, EventDistinct: true,
		Side: side, Quantity: quantity.Abs(), Price: price, Date: date, At: at,
	}

	if row.SettlementDate != "" {
		input.SettlementDate, _, err = reportedDate(row.SettlementDate)
		if err != nil {
			return source.Trade{}, err
		}
	}

	if len(row.Fee) > 0 && string(row.Fee) != "null" {
		fee, err := financial.ParseMoney(number(row.Fee), currency)
		if err != nil {
			return source.Trade{}, err
		}

		charge, err := source.NewCharge(source.ChargeInput{Category: source.Fee, Field: "fee", Total: fee})
		if err != nil {
			return source.Trade{}, err
		}

		input.Charges = []source.Charge{charge}
	}

	if row.Option != nil {
		input.Instrument, err = row.Option.instrument()

		switch row.OptionAction {
		case row.Type + "_TO_OPEN":
			input.PositionEffect = source.Opening
		case row.Type + "_TO_CLOSE":
			input.PositionEffect = source.Closing
		case "":
		default:
			return source.Trade{}, provider.ErrMalformed
		}
	} else if row.OptionAction == "" && row.Symbol != nil {
		input.Instrument, err = row.Symbol.instrument(currency)
	}

	if err != nil {
		return source.Trade{}, err
	}

	return source.NewTrade(input)
}

func (row symbol) instrument(tradeCurrency financial.Currency) (source.Instrument, error) {
	var kind source.InstrumentKind
	switch row.Type.Code {
	case "cs", "ad", "ps", "pr", "equity", "stock":
		kind = source.Equity
	case "et", "oef", "cef", "mf", "fund", "etf":
		kind = source.Fund
	default:
		return source.Instrument{}, provider.ErrUnavailable
	}

	return row.instrumentClaim(kind, "universal-symbol", tradeCurrency)
}

func (row symbol) instrumentClaim(
	kind source.InstrumentKind, referenceKind string, tradeCurrency financial.Currency,
) (source.Instrument, error) {
	ref, err := source.NewReference("snaptrade", referenceKind, "", row.ID)
	if err != nil {
		return source.Instrument{}, err
	}

	input := source.InstrumentInput{Kind: kind, Reference: ref, Ticker: row.RawTicker, Name: row.Name}
	if input.Ticker == "" {
		input.Ticker = row.Ticker
	}

	listingCurrency, _ := financial.ParseCurrency(row.Currency.Code)
	if listingCurrency != (financial.Currency{}) && listingCurrency == tradeCurrency && row.RawTicker != "" &&
		validMIC(row.Exchange.MIC) {
		input.Venue = row.Exchange.MIC
	}

	for _, reported := range []struct {
		kind         source.IdentifierKind
		value, field string
	}{
		{source.ISIN, row.ISIN, "isin"},
		{source.CUSIP, row.CUSIP, "cusip"},
		{source.VenueFIGI, row.FIGI, "figi_code"},
		{source.VenueFIGI, row.FIGIs.Venue, "figi_instrument.figi_code"},
		{source.ShareClassFIGI, row.FIGIs.ShareClass, "figi_instrument.figi_share_class"},
	} {
		if reported.value == "" {
			continue
		}

		identifier, err := source.NewIdentifier(reported.kind, reported.value, reported.field)
		if err != nil {
			return source.Instrument{}, err
		}

		input.Identifiers = append(input.Identifiers, identifier)
	}

	return source.NewInstrument(input)
}

func validMIC(value string) bool {
	return len(value) == 4 && strings.Trim(value, "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789") == ""
}

func (row option) instrument() (source.Instrument, error) {
	ref, err := source.NewReference("snaptrade", "option-symbol", "", row.ID)
	if err != nil {
		return source.Instrument{}, err
	}

	right := source.Call
	switch row.Right {
	case "CALL":
	case "PUT":
		right = source.Put
	default:
		return source.Instrument{}, provider.ErrMalformed
	}

	expiry, _, err := reportedDate(row.Expiry)
	if err != nil {
		return source.Instrument{}, err
	}

	terms := source.OptionInput{Right: right, Expiry: expiry}
	if row.Mini {
		terms.Multiplier, _ = financial.ParseQuantity("10")
	}

	if row.Underlier != nil {
		terms.Underlier, err = row.Underlier.instrument(financial.Currency{})
		if err != nil && err != provider.ErrUnavailable {
			return source.Instrument{}, err
		}

		currency, err := financial.ParseCurrency(row.Underlier.Currency.Code)
		if err == nil && number(row.Strike) != "" {
			terms.Strike, err = financial.ParsePrice(number(row.Strike), currency)
			if err != nil {
				return source.Instrument{}, err
			}
		}
	}

	option, err := source.NewOptionTerms(terms)
	if err != nil {
		return source.Instrument{}, err
	}

	input := source.InstrumentInput{Kind: source.Option, Reference: ref, Ticker: row.Ticker, Option: option}
	if row.Ticker != "" {
		id, err := source.NewIdentifier(source.OCC, row.Ticker, "option_symbol.ticker")
		if err != nil {
			return source.Instrument{}, err
		}

		input.Identifiers = []source.Identifier{id}
	}

	return source.NewInstrument(input)
}

// ActivityFieldExpectation describes the supported cached purchase/sale format.
func (a *adapter) ActivityFieldExpectation(
	family string,
	field source.ActivityFieldName,
) provider.FieldExpectation {
	if family == "trade" && field == source.FieldNetCashWithBillable {
		return provider.NotExpected
	}

	return ""
}
