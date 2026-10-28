// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0
package application

import (
	"testing"

	"go.trippwill.dev/wake/financial"
	"go.trippwill.dev/wake/source"
)

func TestInstrumentCompatibilityIsSymmetricForCompetingIdentifiers(t *testing.T) {
	ref, _ := source.NewReference("synthetic", "instrument", "", "1")
	x, _ := source.NewIdentifier(source.ISIN, "SYNTHETIC-X", "isin")
	y, _ := source.NewIdentifier(source.ISIN, "SYNTHETIC-Y", "securityID")

	a, _ := source.NewInstrument(
		source.InstrumentInput{Reference: ref, Kind: source.Equity, Identifiers: []source.Identifier{x}},
	)
	b, _ := source.NewInstrument(
		source.InstrumentInput{Reference: ref, Kind: source.Equity, Identifiers: []source.Identifier{x, y}},
	)
	if CompatibleInstrument(a, b) || CompatibleInstrument(b, a) {
		t.Fatal("overlapping identifiers hid a competing identifier")
	}
}

func TestConversionOrderDisagreementRemainsVisible(t *testing.T) {
	usd, _ := financial.ParseCurrency("USD")
	gbp, _ := financial.ParseCurrency("GBP")
	sent, _ := source.NewCurrencyAmount(usd, "1")
	received, _ := source.NewCurrencyAmount(gbp, "")
	account, _ := source.NewReference("synthetic", "account", "", "1")
	a, _ := source.NewReference("synthetic", "order", "1", "a")
	b, _ := source.NewReference("synthetic", "order", "1", "b")
	date, _ := financial.ParseDate("2026-10-20")
	f := source.ConversionInput{
		Location: "synthetic:1",
		Account:  account,
		Date:     date,
		Sent:     sent,
		Received: received,
		Order:    a,
	}

	first, err := source.NewConversion(f)
	if err != nil {
		t.Fatal(err)
	}

	f.Order = b

	second, err := source.NewConversion(f)
	if err != nil {
		t.Fatal(err)
	}

	if CompatibleConversion(first, second) {
		t.Fatal("conflicting reported order membership accepted")
	}
}
