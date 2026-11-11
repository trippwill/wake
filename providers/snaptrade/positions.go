// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package snaptrade

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"go.trippwill.dev/wake/financial"
	"go.trippwill.dev/wake/provider"
	"go.trippwill.dev/wake/source"
)

func positionsPending(body []byte, account string) bool {
	var accounts struct {
		Results []struct {
			ID   string `json:"id"`
			Sync struct {
				Positions json.RawMessage `json:"positions"`
			} `json:"sync_status"`
		} `json:"results"`
	}
	if json.Unmarshal(body, &accounts) != nil {
		return true
	}

	for _, row := range accounts.Results {
		if row.ID == account {
			// An omitted status supplies no information. Explicit null means never synced.
			return string(row.Sync.Positions) == "null"
		}
	}

	return true
}

type positionInstrument struct {
	Kind      string `json:"kind"`
	ID        string `json:"id"`
	Ticker    string `json:"symbol"`
	RawTicker string `json:"raw_symbol"`
	Name      string `json:"description"`
	Currency  string `json:"currency"`
	Exchange  string `json:"exchange"`
	FIGIs     struct {
		Venue      string `json:"figi_code"`
		ShareClass string `json:"figi_share_class"`
	} `json:"figi_instrument"`
}

type positionRow struct {
	Instrument json.RawMessage `json:"instrument"`
	Units      string          `json:"units"`
	CostBasis  json.RawMessage `json:"cost_basis"`
	Currency   json.RawMessage `json:"currency"`
}

func (row positionRow) unitCostBasis() financial.Price {
	var amount, code string
	if json.Unmarshal(row.CostBasis, &amount) != nil || amount == "" ||
		json.Unmarshal(row.Currency, &code) != nil {
		return financial.Price{}
	}

	currency, err := financial.ParseCurrency(code)
	if err != nil {
		return financial.Price{}
	}

	price, _ := financial.ParsePrice(amount, currency)

	return price
}

func normalizePositions(
	ctx context.Context, account source.Reference, body []byte, sink provider.Claims,
) (provider.Result, error) {
	scope, err := sourceScope(account)
	if err != nil || scope.Broker != fidelity {
		return provider.Result{}, provider.ErrInvalid
	}

	var envelope struct {
		Results   json.RawMessage `json:"results"`
		Freshness json.RawMessage `json:"data_freshness"`
	}

	var freshness struct {
		At string `json:"as_of"`
	}

	input := source.PositionSetInput{Location: "/results", Account: account, Reason: "incomplete"}
	if json.Unmarshal(body, &envelope) == nil &&
		json.Unmarshal(envelope.Freshness, &freshness) == nil {
		if at, e := financial.ParseTimestamp(freshness.At); e == nil {
			input.At = at
			input.Date, _ = financial.ParseDate(at.Time().Format(time.DateOnly))
			input.TimeBasis = source.PositionSourceFetched
		}
	}

	counts := provider.Counts{Rows: 1, PositionSets: 1}

	var rows []json.RawMessage
	if !input.At.IsZero() && json.Unmarshal(envelope.Results, &rows) == nil && rows != nil {
		if len(rows) >= 100000 {
			return provider.Result{}, provider.ErrLimit
		}

		for index, raw := range rows {
			if err = ctx.Err(); err != nil {
				return provider.Result{}, err
			}

			var row positionRow

			err = json.Unmarshal(raw, &row)
			if err == nil {
				var holding source.Position

				holding, err = row.position()
				if err == nil && holding.Quantity().Value() != "" {
					input.Positions = append(input.Positions, holding)
				}
			}

			if err != nil {
				location := "/results/" + strconv.Itoa(index)
				if err = sink.Reject(ctx, location, "unusable-position"); err != nil {
					return provider.Result{}, err
				}

				counts.Rejected++
			}
		}

		if counts.Rejected == 0 {
			input.Complete, input.Reason = true, ""
		} else {
			input.Positions = nil
		}
	}

	counts.Instruments = uint64(len(input.Positions))
	counts.Rows += counts.Instruments + counts.Rejected

	set, err := source.NewPositionSetObservation(input)
	if err != nil {
		return provider.Result{}, err
	}

	if err = sink.PutPositionSet(ctx, set); err != nil {
		return provider.Result{}, err
	}

	return provider.NewResult(counts)
}

func (row positionRow) position() (source.Position, error) {
	var instrument positionInstrument
	if json.Unmarshal(row.Instrument, &instrument) != nil || instrument.Kind == "" {
		return source.Position{}, provider.ErrMalformed
	}

	if instrument.Kind == "cash" {
		return source.Position{}, nil
	}

	quantity, err := financial.ParseQuantity(row.Units)
	if err != nil {
		return source.Position{}, err
	}

	if !quantity.Abs().Positive() {
		return source.Position{}, nil
	}

	var claim source.Instrument
	if instrument.Kind == "option" {
		claim, err = instrument.option(row.Instrument)
	} else {
		claim, err = instrument.security()
	}

	if err != nil {
		return source.Position{}, err
	}

	holding, err := source.NewPosition(claim, quantity, row.unitCostBasis())
	if err != nil {
		return source.Position{}, err
	}

	if claim.Kind() == source.Debt {
		return holding.WithReportedUnits()
	}

	return holding, nil
}

func (i positionInstrument) security() (source.Instrument, error) {
	var kind source.InstrumentKind
	switch i.Kind {
	case "stock", "preferred_stock", "adr":
		kind = source.Equity
	case "etf", "mutualfund", "cef":
		kind = source.Fund
	case "bond":
		kind = source.Debt
	case "other":
		kind = source.UnknownInstrument
	default:
		return source.Instrument{}, provider.ErrUnavailable
	}

	row := symbol{ID: i.ID, Ticker: i.Ticker, RawTicker: i.RawTicker, Name: i.Name}
	row.Currency.Code, row.Exchange.MIC = i.Currency, i.Exchange
	row.FIGIs.Venue, row.FIGIs.ShareClass = i.FIGIs.Venue, i.FIGIs.ShareClass
	currency, _ := financial.ParseCurrency(i.Currency)

	// V2 instrument IDs have their own namespace. Use reported identifiers to
	// reconcile with trade symbols through the usual instrument rules.
	return row.instrumentClaim(kind, "instrument", currency)
}

func (i positionInstrument) option(raw json.RawMessage) (source.Instrument, error) {
	var detail struct {
		Right      string             `json:"option_type"`
		Strike     string             `json:"strike_price"`
		Expiry     string             `json:"expiration_date"`
		Multiplier string             `json:"multiplier"`
		Underlying positionInstrument `json:"underlying"`
	}

	if json.Unmarshal(raw, &detail) != nil {
		return source.Instrument{}, provider.ErrMalformed
	}

	right := source.Call
	switch detail.Right {
	case "CALL":
	case "PUT":
		right = source.Put
	default:
		return source.Instrument{}, provider.ErrMalformed
	}

	underlier, err := detail.Underlying.security()
	if err != nil {
		return source.Instrument{}, err
	}

	expiry, err := financial.ParseDate(detail.Expiry)
	if err != nil {
		return source.Instrument{}, err
	}

	terms := source.OptionInput{Right: right, Expiry: expiry, Underlier: underlier}
	if detail.Multiplier != "" {
		terms.Multiplier, err = financial.ParseQuantity(detail.Multiplier)
		if err != nil {
			return source.Instrument{}, err
		}
	}

	if detail.Strike != "" {
		strike, e := financial.ParseQuantity(detail.Strike)
		if e != nil || strings.HasPrefix(strike.Value(), "-") {
			return source.Instrument{}, provider.ErrMalformed
		}

		if currency, e := financial.ParseCurrency(detail.Underlying.Currency); e == nil {
			terms.Strike, err = financial.ParsePrice(strike.Value(), currency)
			if err != nil {
				return source.Instrument{}, err
			}
		}
	}

	option, err := source.NewOptionTerms(terms)
	if err != nil {
		return source.Instrument{}, err
	}

	ref, err := source.NewReference("snaptrade", "instrument", "", i.ID)
	if err != nil {
		return source.Instrument{}, err
	}

	id, err := source.NewIdentifier(source.OCC, i.Ticker, "instrument.symbol")
	if err != nil {
		return source.Instrument{}, err
	}

	return source.NewInstrument(source.InstrumentInput{
		Kind: source.Option, Reference: ref, Ticker: i.Ticker, Name: i.Name,
		Option: option, Identifiers: []source.Identifier{id},
	})
}
