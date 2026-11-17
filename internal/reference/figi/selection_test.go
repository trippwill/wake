// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0
package figi

import (
	"testing"

	"go.trippwill.dev/wake/source"
)

func TestOptionReferenceSubject(t *testing.T) {
	equity := Record{FIGI: "listing", ShareClass: "root", Name: "Underlier", Market: "Equity"}
	if selected := SelectIdentity(source.Option, []Record{equity}); selected.FIGI != "" {
		t.Fatal("underlier identified option")
	}

	option := Record{
		FIGI:          "venue",
		Composite:     "contract",
		Name:          "Contract",
		Market:        "Equity",
		SecurityType2: "Option",
	}
	if selected := SelectIdentity(source.Option, []Record{option}); selected.FIGI != "contract" {
		t.Fatal("option rejected")
	}

	if selected := SelectIdentity(source.Equity, []Record{option}); selected.FIGI != "" {
		t.Fatal("option identified equity")
	}

	if selected := SelectIdentity(source.Option, []Record{option, option}); selected.FIGI != "contract" {
		t.Fatal("duplicate representations did not agree")
	}
}

func TestDebtReferenceNeedsNoShareClass(t *testing.T) {
	r := Record{FIGI: "issue", Name: "Treasury", Market: "Govt"}
	if selected := SelectIdentity(source.Debt, []Record{r}); selected.FIGI != "issue" {
		t.Fatal("debt required share class")
	}

	if selected := SelectIdentity(source.Equity, []Record{r}); selected.FIGI != "" {
		t.Fatal("debt identified equity")
	}
}
