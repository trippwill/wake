// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package source

import (
	"reflect"
	"strconv"
	"testing"

	"go.trippwill.dev/wake/financial"
)

func testCharge(t *testing.T, category ChargeCategory, field, amount string, children ...Charge) Charge {
	t.Helper()

	usd, _ := financial.ParseCurrency("USD")

	var total financial.Money
	if amount != "" {
		total, _ = financial.ParseMoney(amount, usd)
	}

	c, err := NewCharge(ChargeInput{Category: category, Field: field, Total: total, Components: children})
	if err != nil {
		t.Fatal(err)
	}

	return c
}

func TestChargeHierarchyPreservesIndependentTotalsAndImmutableComponents(t *testing.T) {
	fee := testCharge(t, RegulatoryFee, "taf", "0.00000000012300")
	reg := testCharge(t, RegulatoryFee, "regulatory", "", fee)
	children := []Charge{reg}
	usd, _ := financial.ParseCurrency("USD")
	total, _ := financial.ParseMoney("-1.2300", usd)

	commission, err := NewCharge(
		ChargeInput{Category: Commission, Field: "commission", Total: total, Components: children},
	)
	if err != nil {
		t.Fatal(err)
	}

	children[0] = Charge{}
	copy := commission.Components()
	copy[0] = Charge{}

	if commission.Total().Amount() != "-1.2300" || commission.Components()[0].Field() != "regulatory" ||
		commission.Components()[0].Total().
			Amount() !=
			"" || commission.Components()[0].Components()[0].Total().Amount() != "0.00000000012300" {
		t.Fatal("reported totals or immutable components changed", commission)
	}

	fields := ChargeFields([]Charge{commission})
	if len(fields) != 2 || fields[1].Key() != "charge/commission/regulatory/taf" {
		t.Fatal(fields)
	}

	input := fields[1].Fields()
	input.ChargeParents[0] = ChargeRole{}

	if fields[1].Key() != "charge/commission/regulatory/taf" {
		t.Fatal("field path mutated")
	}

	roundtrip, err := ChargesFromFields(fields)
	if err != nil || !reflect.DeepEqual(roundtrip, []Charge{commission}) {
		t.Fatal(roundtrip, err)
	}
}

func TestChargeInvariantsAndWholeTreeLimit(t *testing.T) {
	leaf := testCharge(t, Fee, "fee", "0.00")
	for _, input := range []ChargeInput{
		{Category: Fee, Field: "empty"},
		{Category: "unclassified", Field: "bad", Total: leaf.Total()},
		{Category: Fee, Field: "", Total: leaf.Total()},
		{Category: Fee, Field: "bad-child", Components: []Charge{{}}},
		{Category: Fee, Field: "duplicate", Components: []Charge{leaf, leaf}},
	} {
		if _, err := NewCharge(input); err == nil {
			t.Fatal("invalid charge accepted", input)
		}
	}

	children := make([]Charge, 31)
	for i := range children {
		children[i] = testCharge(t, Fee, "fee"+strconv.Itoa(i), "1")
	}

	full := testCharge(t, Commission, "total", "", children...)
	if _, err := NewCharge(
		ChargeInput{Category: Commission, Field: "overflow", Components: []Charge{full}},
	); err == nil {
		t.Fatal("tree limit ignored")
	}

	if _, err := NewActivityField(ActivityFieldInput{Name: FieldCharge, Charge: full}); err == nil {
		t.Fatal("compound assertion bypassed path selection")
	}
}

func TestConflictingChargeAssertionsRemainEvidence(t *testing.T) {
	a := ChargeFields([]Charge{testCharge(t, Fee, "same", "1.00")})[0]
	b := ChargeFields([]Charge{testCharge(t, Fee, "same", "2.00")})[0]
	account, _ := NewReference("test", "account", "", "one")
	if _, err := NewActivityObservation(
		ActivityObservationInput{Location: "row", Account: account, Fields: []ActivityField{a, b}},
	); err != nil {
		t.Fatal(err)
	}

	if _, err := ChargesFromFields([]ActivityField{a, b}); err == nil {
		t.Fatal("conflict materialized into accepted tree")
	}
}

func TestCashChargeTotalUsesPostingAmount(t *testing.T) {
	account, _ := NewReference("test", "account", "", "one")
	date, _ := financial.ParseDate("2026-10-20")
	amount := testCharge(t, Fee, "amount", "-1.2500").Total()
	component := testCharge(t, Fee, "service", "-1.00")
	input := CashActivityInput{
		Location:         "row",
		Account:          account,
		Category:         CashFee,
		Amount:           amount,
		Date:             date,
		ChargeComponents: []Charge{component},
	}

	v, err := NewCashActivity(input)
	if err != nil {
		t.Fatal(err)
	}

	copy := v.Fields()
	copy.ChargeComponents[0] = Charge{}

	decoded, err := v.Observation().CashActivity()
	if err != nil || !reflect.DeepEqual(v.Fields(), decoded.Fields()) || v.Charges()[0].Total() != amount {
		t.Fatal(decoded, err)
	}

	input.Category = CashFunding

	other, err := NewCashActivity(input)
	if err != nil || len(other.Fields().ChargeComponents) != 1 {
		t.Fatal("reclassification lost source components", other, err)
	}
}
