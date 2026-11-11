// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package source

import (
	"errors"
	"testing"

	"go.trippwill.dev/wake/financial"
)

func TestPartialActivityObservationOwnsAssertionsAndPreservesUnknowns(t *testing.T) {
	account, _ := NewReference("source", "account", "", "one")
	event, _ := NewReference("source", "event", "one", "event")
	eur, _ := financial.ParseCurrency("EUR")
	zero, _ := financial.ParseMoney("0.00000000000000000", eur)
	date, _ := financial.ParseDate("2026-10-24")

	known, err := NewActivityField(ActivityFieldInput{Name: FieldRealizedPnL, Money: zero, ReportedOn: date})
	if err != nil {
		t.Fatal(err)
	}

	unknown, err := NewActivityField(ActivityFieldInput{Name: FieldReportedCostBasis, Unknown: true})
	if err != nil {
		t.Fatal(err)
	}

	fields := []ActivityField{known, unknown}
	aliases := []Reference{event}

	value, err := NewActivityObservation(
		ActivityObservationInput{
			Location:     "row",
			Account:      account,
			Event:        event,
			EventAliases: aliases,
			Fields:       fields,
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	fields[0] = unknown
	aliases[0] = Reference{}
	copy := value.Fields()

	copy.Fields[0] = unknown
	copy.EventAliases[0] = Reference{}
	amount, ok := value.Field(FieldRealizedPnL)
	if !ok || !amount.Known() || amount.Fields().Money.Amount() != zero.Amount() ||
		amount.Fields().ReportedOn != date ||
		value.Fields().EventAliases[0] != event {
		t.Fatal("caller changed immutable observation", value)
	}

	if cost, ok := value.Field(FieldReportedCostBasis); !ok || cost.Known() {
		t.Fatal("explicit unknown lost")
	}

	if _, ok := value.Field(FieldPrice); ok {
		t.Fatal("omission became an assertion")
	}

	if _, err = value.Trade(); err == nil {
		t.Fatal("partial observation fabricated complete trade")
	}
}

func TestActivityAssertionsRejectUntypedOrContradictoryPayloads(t *testing.T) {
	usd, _ := financial.ParseCurrency("USD")

	money, _ := financial.ParseMoney("1", usd)
	quantity, _ := financial.ParseQuantity("1")
	for _, input := range []ActivityFieldInput{
		{Name: "arbitrary-provider-property", Money: money},
		{Name: FieldNetCash, Money: money, Text: "hidden"},
		{Name: FieldNetCash, Unknown: true, Money: money},
		{Name: FieldPrice, Money: money},
		{Name: FieldQuantity, Quantity: quantity},
		{Name: FieldQuantity, Quantity: quantity, QuantityUnit: "unqualified"},
	} {
		if _, err := NewActivityField(input); !errors.Is(err, ErrInvalidActivity) {
			t.Fatal(input, err)
		}
	}
}

func TestActivityRelationshipKeysPreserveQualifiedReferenceBoundaries(t *testing.T) {
	left, _ := NewReference("source", "event", "account/a", "b")
	right, _ := NewReference("source", "event", "account", "a/b")
	keys := map[string]bool{}
	for _, ref := range []Reference{left, right} {
		relationship, err := NewRelationship(LifecycleLink, ref)
		if err != nil {
			t.Fatal(err)
		}

		field, err := NewActivityField(ActivityFieldInput{Name: FieldRelationship, Relationship: relationship})
		if err != nil || keys[field.Key()] {
			t.Fatal("distinct qualified references share a selection key", field, err)
		}

		keys[field.Key()] = true
	}
}
