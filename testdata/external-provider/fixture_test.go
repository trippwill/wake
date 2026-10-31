// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package fixture

import (
	"go.trippwill.dev/wake/financial"
	"testing"
)

func TestRegistrationIsSideEffectFree(t *testing.T) {
	r, err := Register()
	if err != nil || r.Descriptor().ID() != "fixture" || r.Acquirer() == nil || r.Normalizer() == nil || r.Discoverer() == nil {
		t.Fatal(r, err)
	}
}

func TestIndependentCapabilitiesAndSharedScalarParsers(t *testing.T) {
	registration, err := RegisterDocument()
	if err != nil || registration.Normalizer() == nil || registration.Acquirer() != nil || registration.Discoverer() != nil || len(registration.Descriptor().Fields()) != 0 {
		t.Fatal(registration, err)
	}
	currency, err := financial.ParseCurrency("USD")
	if err != nil {
		t.Fatal(err)
	}
	money, err := financial.ParseMoney("1.2345000000000000000001e2", currency)
	if err != nil || money.Amount() != "123.45000000000000000001" {
		t.Fatal("exact decimal parsing lost scale", money, err)
	}
	timestamp, err := financial.ParseTimestamp("2026-10-25T12:00:00.1200Z")
	if err != nil || timestamp.String() != "2026-10-25T12:00:00.1200Z" {
		t.Fatal("timestamp precision lost", timestamp, err)
	}
	if _, err = financial.ParseMoney("1,234.50", currency); err == nil {
		t.Fatal("shared scalar parser guessed a locale")
	}
}
