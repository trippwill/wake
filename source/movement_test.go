// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package source

import (
	"testing"

	"go.trippwill.dev/wake/financial"
)

func TestMovementConstructorsPreserveUnitsSignsAndOwnership(t *testing.T) {
	account, _ := NewReference("fixture", "account", "", "A")
	event, _ := NewReference("fixture", "transfer", "A", "T")
	instrument, _ := NewInstrument(InstrumentInput{Kind: Equity, Ticker: "SYN"})
	quantity, _ := financial.ParseQuantity("0.000000000012300")
	date, _ := financial.ParseDate("2026-10-20")
	input := SecurityTransferInput{
		Location:     "row",
		Account:      account,
		Event:        event,
		EventAliases: []Reference{event},
		Direction:    TransferOut,
		Instrument:   instrument,
		Quantity:     quantity,
		Date:         date,
	}

	v, err := NewSecurityTransfer(input)
	if err != nil {
		t.Fatal(err)
	}

	input.EventAliases[0] = Reference{}
	fields := v.Fields()
	fields.EventAliases[0] = Reference{}
	if v.Fields().EventAliases[0].IsZero() {
		t.Fatal("caller mutated sealed transfer")
	}

	o := v.Observation().Fields()
	for i, field := range o.Fields {
		if field.Name() == FieldQuantity {
			o.Fields[i], _ = NewActivityField(
				ActivityFieldInput{Name: FieldQuantity, Quantity: quantity, QuantityUnit: "contracts"},
			)
		}
	}

	wrong, err := NewActivityObservation(o)
	if err != nil {
		t.Fatal(err)
	}

	if _, err = wrong.SecurityTransfer(); err == nil {
		t.Fatal("share transfer accepted contract units")
	}

	currency, _ := financial.ParseCurrency("USD")

	price, _ := financial.ParsePrice("1.0000", currency)
	if _, err = NewActivityField(
		ActivityFieldInput{Name: FieldTransferPrice, Price: price, QuotationBasis: PerShare},
	); err == nil {
		t.Fatal("transfer price accepted an unsupported quotation basis")
	}

	for _, text := range []string{"-0.000000000012300", "0.0000", "12.34000000000000001"} {
		amount, _ := financial.ParseMoney(text, currency)

		cash, e := NewCashActivity(
			CashActivityInput{Location: "cash", Account: account, Category: CashFee, Amount: amount, Date: date},
		)
		if e != nil {
			t.Fatal(e)
		}

		roundTrip, e := cash.Observation().CashActivity()
		if e != nil || roundTrip.Fields().Amount.Amount() != text || !roundTrip.Fields().At.IsZero() {
			t.Fatal(roundTrip, e)
		}
	}

	if _, err = NewCashActivity(
		CashActivityInput{Location: "cash", Account: account, Category: "invented", Date: date},
	); err == nil {
		t.Fatal("invalid cash category accepted")
	}
}
