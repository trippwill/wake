// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package ibkrflex

import (
	"context"

	"go.trippwill.dev/wake/financial"
	"go.trippwill.dev/wake/provider"
	"go.trippwill.dev/wake/source"
)

func (s *Source) normalizeRealized(
	ctx context.Context,
	artifact provider.Artifact,
	sink provider.Claims,
) (provider.Result, error) {
	body := artifact.Content()
	if int64(len(body)) > s.limit {
		return provider.Result{}, provider.ErrLimit
	}

	counts := provider.Counts{}

	metadata, err := inspectReport(ctx, body, "AF", func(path []node) error {
		if counts.Rows >= 100000 {
			return provider.ErrLimit
		}

		counts.Rows++
		row := path[len(path)-1].attributes
		if row["levelOfDetail"] != "EXECUTION" || row["fifoPnlRealized"] == "" && row["cost"] == "" {
			counts.Ignored++
			return nil
		}

		claim, e := reportedRealized(row, path[2].attributes["accountId"], location(path))
		if e != nil {
			counts.Rejected++
			return sink.Reject(ctx, location(path), "invalid-realized-pnl")
		}

		if e = sink.PutActivity(ctx, claim); e != nil {
			return e
		}

		counts.Activities++

		return nil
	})
	if err != nil {
		return provider.Result{}, err
	}

	if metadata.tradeSections == 0 {
		if err = sink.Reject(ctx, "/FlexQueryResponse[1]", "trade-section-not-reported"); err != nil {
			return provider.Result{}, err
		}

		counts.Rows++
		counts.Rejected++
	}

	return provider.NewResult(counts)
}

func reportedRealized(row map[string]string, parent, loc string) (source.ActivityObservation, error) {
	if row["accountId"] != "" && row["accountId"] != parent ||
		row["transactionType"] != "ExchTrade" && row["transactionType"] != "BookTrade" {
		return source.ActivityObservation{}, provider.ErrMalformed
	}

	account, err := source.NewReference("ibkr", "account", "", first(row["accountId"], parent))
	if err != nil {
		return source.ActivityObservation{}, err
	}

	f := source.ActivityObservationInput{Location: loc, Account: account}
	for _, entry := range []struct{ field, kind string }{{"ibExecID", "execution-id"}, {"tradeID", "trade-id"}, {"transactionID", "transaction-id"}} {
		if value := nativeValue(row[entry.field]); value != "" {
			r, e := source.NewReference("ibkr", entry.kind, account.Value(), value)
			if e != nil {
				return source.ActivityObservation{}, e
			}

			if f.Event.IsZero() {
				f.Event = r
				f.EventDistinct = entry.field == "ibExecID"
			} else {
				f.EventAliases = append(f.EventAliases, r)
			}
		}
	}

	if f.Event.IsZero() {
		return source.ActivityObservation{}, provider.ErrMalformed
	}

	kind := "trade"
	switch row["assetCategory"] {
	case "CASH":
		kind = "conversion"
	case "STK", "BOND", "BILL", "OPT":
		if row["assetCategory"] == "OPT" && row["transactionType"] == "BookTrade" {
			kind = "lifecycle"
		}
	default:
		return source.ActivityObservation{}, provider.ErrUnavailable
	}

	fields := []source.ActivityFieldInput{{Name: source.FieldFamily, Text: kind}}
	if row["buySell"] != "" {
		side := source.Side("")
		switch row["buySell"] {
		case "BUY":
			side = source.Buy
		case "SELL":
			side = source.Sell
		default:
			return source.ActivityObservation{}, provider.ErrMalformed
		}

		if kind == "trade" {
			fields = append(fields, source.ActivityFieldInput{Name: source.FieldSide, Text: string(side)})
		}
	}

	if row["assetCategory"] != "CASH" && nativeValue(row["conid"]) != "" {
		ref, e := source.NewReference("ibkr", "contract-id", "", nativeValue(row["conid"]))
		if e != nil {
			return source.ActivityObservation{}, e
		}

		instrument, e := source.NewInstrument(
			source.InstrumentInput{
				Kind:      activityInstrumentKind(row["assetCategory"], row["subCategory"]),
				Reference: ref,
			},
		)
		if e != nil {
			return source.ActivityObservation{}, e
		}

		fields = append(fields, source.ActivityFieldInput{Name: source.FieldInstrument, Instrument: instrument})
	}

	date, err := parseDate(row["tradeDate"])
	if err != nil {
		return source.ActivityObservation{}, err
	}

	fields = append(fields, source.ActivityFieldInput{Name: source.FieldTradeDate, Date: date})
	if row["quantity"] != "" && kind != "conversion" {
		q, e := financial.NewQuantity(row["quantity"])
		if e != nil {
			return source.ActivityObservation{}, e
		}

		fields = append(
			fields,
			source.ActivityFieldInput{
				Name:         source.FieldQuantity,
				Quantity:     q.Abs(),
				QuantityUnit: activityQuantityUnit(row["assetCategory"]),
			},
		)
	}

	reportDate := financial.Date{}
	if row["reportDate"] != "" {
		reportDate, err = parseDate(row["reportDate"])
		if err != nil {
			return source.ActivityObservation{}, err
		}
	}

	currency, err := financial.ParseCurrency(row["currency"])
	if err != nil {
		return source.ActivityObservation{}, err
	}

	for _, entry := range []struct {
		field string
		name  source.ActivityFieldName
	}{{"fifoPnlRealized", source.FieldRealizedPnL}, {"cost", source.FieldReportedCostBasis}} {
		if row[entry.field] != "" {
			money, e := financial.NewMoney(row[entry.field], currency)
			if e != nil {
				return source.ActivityObservation{}, e
			}

			fields = append(
				fields,
				source.ActivityFieldInput{Name: entry.name, Money: money, ReportedOn: reportDate},
			)
		}
	}

	for _, input := range fields {
		field, e := source.NewActivityField(input)
		if e != nil {
			return source.ActivityObservation{}, e
		}

		f.Fields = append(f.Fields, field)
	}

	return source.NewActivityObservation(f)
}

func activityQuantityUnit(category string) string {
	switch category {
	case "OPT":
		return "contracts"
	case "BOND", "BILL":
		return "nominal"
	default:
		return "shares"
	}
}

func activityInstrumentKind(category, subtype string) source.InstrumentKind {
	switch category {
	case "OPT":
		return source.Option
	case "BOND", "BILL":
		return source.Debt
	case "STK":
		if subtype == "ETF" {
			return source.Fund
		}

		if subtype != "" {
			return source.Equity
		}

		return source.UnknownInstrument
	default:
		return source.UnknownInstrument
	}
}
