// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package fixture

import (
	"go.trippwill.dev/wake/financial"
	"go.trippwill.dev/wake/source"
)

func SecurityTransfer() (source.SecurityTransfer, error) {
	t, err := Option()
	if err != nil {
		return source.SecurityTransfer{}, err
	}
	f := t.Fields()
	event, _ := source.NewReference("fixture-broker", "transfer", "account-1", "transfer-1")
	return source.NewSecurityTransfer(source.SecurityTransferInput{Location: "transfer:1", Account: f.Account, Event: event, EventDistinct: true, Instrument: f.Instrument, Direction: source.TransferIn, Quantity: f.Quantity, Date: f.Date})
}

func CashActivity() (source.CashActivity, error) {
	t, err := Option()
	if err != nil {
		return source.CashActivity{}, err
	}
	f := t.Fields()
	event, _ := source.NewReference("fixture-broker", "cash", "account-1", "cash-1")
	c, _ := financial.ParseCurrency("GBP")
	amount, _ := financial.ParseMoney("-0.000000000000000012300", c)
	return source.NewCashActivity(source.CashActivityInput{Location: "cash:1", Account: f.Account, Event: event, EventDistinct: true, Category: source.CashFee, Amount: amount, Date: f.Date, Description: "Synthetic external fee"})
}
