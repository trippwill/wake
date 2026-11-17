// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package recordcodec

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"go.trippwill.dev/wake/source"
)

func TestSourceClaimKeysPreservePrecisionUnknownsAndQualifiedIdentity(t *testing.T) {
	original := observationData{
		Location: "first/path",
		Event:    refData{"source", "event", "scope", "original"},
		Fields: []activityFieldData{
			{Name: source.FieldPrice, Price: moneyData{Amount: "100", Currency: "USD"}},
			{Name: source.FieldAt, Timestamp: "2026-10-20T13:01:55.120Z"},
			{Name: source.FieldNetCash, Unknown: true},
		},
	}

	original.EventAliases = []refData{{"source", "event", "scope", "one"}, {"source", "event", "scope", "two"}}
	original.Fields = append(
		original.Fields,
		activityFieldData{
			Name: source.FieldCharge,
			Charge: &chargeData{
				Field:    "b",
				Category: source.Fee,
				Total:    moneyData{Amount: "1.00", Currency: "USD"},
			},
		},
		activityFieldData{
			Name:   source.FieldCharge,
			Charge: &chargeData{Field: "a", Category: source.Fee, Total: moneyData{Amount: "2", Currency: "USD"}},
		},
	)
	baseline, _ := json.Marshal(original)

	canonical, err := ClaimKeyData("activity", baseline)
	if err != nil {
		t.Fatal(err)
	}

	reordered := original
	reordered.EventAliases = slices.Clone(original.EventAliases)
	slices.Reverse(reordered.EventAliases)
	reordered.Fields = slices.Clone(original.Fields)
	slices.Reverse(reordered.Fields)
	reordered.Location = "different/path"
	encoded, _ := json.Marshal(reordered)

	same, err := ClaimKeyData("activity", encoded)
	if err != nil || !bytes.Equal(same, canonical) {
		t.Fatal("structural order or locator changed source meaning", err)
	}

	for _, mutate := range []func(*observationData){
		func(d *observationData) {
			for i := range d.Fields {
				if d.Fields[i].Name == source.FieldPrice {
					d.Fields[i].Price.Amount = "100.0"
				}
			}
		},
		func(d *observationData) {
			for i := range d.Fields {
				if d.Fields[i].Name == source.FieldAt {
					d.Fields[i].Timestamp = strings.Replace(d.Fields[i].Timestamp, ".120", ".12", 1)
				}
			}
		},
		func(d *observationData) { d.Event[2] = "other-scope" },
		func(d *observationData) {
			d.Fields = slices.DeleteFunc(d.Fields, func(f activityFieldData) bool { return f.Name == source.FieldNetCash })
		},
		func(d *observationData) { d.Fields = append(d.Fields, d.Fields[len(d.Fields)-1]) },
	} {
		changed := original
		changed.Fields = slices.Clone(original.Fields)
		mutate(&changed)

		encoded, _ = json.Marshal(changed)
		next, e := ClaimKeyData("activity", encoded)
		if e != nil || bytes.Equal(next, canonical) {
			t.Fatal("meaning or multiplicity lost", changed, e)
		}
	}
}
