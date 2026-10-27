// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package source_test

import (
	"testing"

	"go.trippwill.dev/wake/source"
)

func TestQualifiedReferencesAndImmutableIdentifiers(t *testing.T) {
	a, err := source.NewReference("broker-a", "instrument", "account-1", "123")
	if err != nil {
		t.Fatal(err)
	}

	b, err := source.NewReference("broker-b", "instrument", "account-1", "123")
	if err != nil || a == b {
		t.Fatal("authority collapsed")
	}

	c, err := source.NewReference("broker-a", "trade", "account-2", "123")
	if err != nil || a == c {
		t.Fatal("kind/scope collapsed")
	}

	id, err := source.NewIdentifier(source.ISIN, "SYNTHETIC", "reported-isin")
	if err != nil {
		t.Fatal(err)
	}

	ids := []source.Identifier{id}

	instrument, err := source.NewInstrument(
		source.InstrumentInput{Reference: a, Kind: source.Equity, Identifiers: ids},
	)
	if err != nil {
		t.Fatal(err)
	}

	ids[0] = source.Identifier{}
	copy := instrument.Identifiers()
	copy[0] = source.Identifier{}

	if instrument.Identifiers()[0] != id {
		t.Fatal("mutable identifiers")
	}

	if _, err = source.NewTrade(source.TradeInput{}); err == nil {
		t.Fatal("invalid execution accepted")
	}
}
