// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package ibkrflex

import (
	"go.trippwill.dev/wake/financial"
	"go.trippwill.dev/wake/provider"
	"go.trippwill.dev/wake/source"
)

func securityTransfer(row map[string]string, parent, loc string) (source.ActivityObservation, error) {
	if row["accountId"] != "" && row["accountId"] != parent {
		return source.ActivityObservation{}, provider.ErrMalformed
	}

	if row["assetCategory"] != "STK" && row["assetCategory"] != "OPT" && row["assetCategory"] != "BOND" &&
		row["assetCategory"] != "BILL" {
		return source.ActivityObservation{}, provider.ErrUnavailable
	}

	f := source.SecurityTransferInput{
		Location:            loc,
		Method:              row["type"],
		DeliveringBroker:    row["deliveringBroker"],
		Company:             row["company"],
		CounterpartyAccount: row["account"],
		CounterpartyName:    row["accountName"],
	}

	var err error

	f.Account, err = source.NewReference("ibkr", "account", "", parent)
	if err != nil {
		return source.ActivityObservation{}, err
	}

	if id := nativeValue(row["transactionID"]); id != "" {
		f.Event, err = source.NewReference("ibkr", "security-transfer-id", parent, id)
		if err != nil {
			return source.ActivityObservation{}, err
		}

		f.EventDistinct = true
	}

	f.Instrument, err = rowInstrument(row)
	if err != nil {
		return source.ActivityObservation{}, err
	}

	f.Quantity, err = financial.ParseQuantity(row["quantity"])
	if err != nil {
		return source.ActivityObservation{}, err
	}

	f.Quantity = f.Quantity.Abs()
	switch row["direction"] {
	case "IN":
		f.Direction = source.TransferIn
	case "OUT":
		f.Direction = source.TransferOut
	default:
		return source.ActivityObservation{}, provider.ErrMalformed
	}

	f.Date, err = parseDate(row["date"])
	if err != nil {
		return source.ActivityObservation{}, err
	}

	for _, entry := range []struct {
		key    string
		target *financial.Date
	}{{"reportDate", &f.ReportDate}, {"settleDate", &f.SettlementDate}} {
		if row[entry.key] != "" {
			*entry.target, err = parseDate(row[entry.key])
			if err != nil {
				return source.ActivityObservation{}, err
			}
		}
	}

	if len(row["dateTime"]) > 10 {
		f.At, err = parseReportedTimestamp(row["dateTime"])
		if err != nil {
			return source.ActivityObservation{}, err
		}
	}

	c, err := financial.ParseCurrency(row["currency"])
	if err != nil {
		return source.ActivityObservation{}, err
	}

	if row["transferPrice"] != "" {
		f.TransferPrice, err = financial.ParsePrice(row["transferPrice"], c)
		if err != nil {
			return source.ActivityObservation{}, err
		}
	}

	for _, entry := range []struct {
		key    string
		target *financial.Money
	}{{"positionAmount", &f.PositionValue}, {"cost", &f.ReportedCost}, {"pnlAmount", &f.ReportedPnL}} {
		if row[entry.key] != "" {
			*entry.target, err = financial.ParseMoney(row[entry.key], c)
			if err != nil {
				return source.ActivityObservation{}, err
			}
		}
	}

	v, err := source.NewSecurityTransfer(f)
	if err != nil {
		return source.ActivityObservation{}, err
	}

	return v.Observation(), nil
}
