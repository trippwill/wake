// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0
package reference

import (
	"testing"

	"go.trippwill.dev/wake/source"
)

func TestOptionReferenceSubject(t *testing.T) {
	equity := Record{FIGI: "listing", ShareClass: "root", Name: "Underlier", Market: "Equity"}
	if _, _, ok := Select(source.Option, []Record{equity}); ok {
		t.Fatal("underlier identified option")
	}

	option := Record{FIGI: "contract", Name: "Contract", Market: "Equity", SecurityType2: "Option"}
	if _, id, ok := Select(source.Option, []Record{option}); !ok || id != "contract" {
		t.Fatal("option rejected")
	}

	if _, _, ok := Select(source.Equity, []Record{option}); ok {
		t.Fatal("option identified equity")
	}

	if _, _, ok := Select(source.Option, []Record{option, option}); ok {
		t.Fatal("arbitrary candidate selected")
	}
}

func TestDebtReferenceNeedsNoShareClass(t *testing.T) {
	r := Record{FIGI: "issue", Name: "Treasury", Market: "Govt"}
	if _, id, ok := Select(source.Debt, []Record{r}); !ok || id != "issue" {
		t.Fatal("debt required share class")
	}

	if _, _, ok := Select(source.Equity, []Record{r}); ok {
		t.Fatal("debt identified equity")
	}
}
