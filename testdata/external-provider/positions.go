// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package fixture

import (
	"go.trippwill.dev/wake/financial"
	"go.trippwill.dev/wake/source"
)

func Positions() (source.PositionSetObservation, error) {
	trade, err := Option()
	if err != nil {
		return source.PositionSetObservation{}, err
	}
	f := trade.Fields()
	q, err := financial.NewQuantity("-2.00000000000000100")
	if err != nil {
		return source.PositionSetObservation{}, err
	}
	c, _ := financial.ParseCurrency("GBP")
	cost, _ := financial.NewPrice("0.0000", c)
	p, err := source.NewPosition(f.Instrument, q, cost)
	if err != nil {
		return source.PositionSetObservation{}, err
	}
	at, err := financial.ParseTimestamp("2026-10-20T12:34:56.1200Z")
	if err != nil {
		return source.PositionSetObservation{}, err
	}
	return source.NewPositionSetObservation(source.PositionSetInput{Location: "positions", Account: f.Account, Date: f.TradeDate, At: at, Complete: true, Positions: []source.Position{p}})
}
