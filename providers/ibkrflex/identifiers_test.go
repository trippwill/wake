// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package ibkrflex

import (
	"testing"

	"go.trippwill.dev/wake/source"
)

func TestReportedUnderlierAndOCCIdentifiers(t *testing.T) {
	for _, id := range []string{"US0378331005", "US11135F1012", "AN8068571086", "US9311421039"} {
		if !underlierISIN(id) {
			t.Fatal("valid ISIN", id)
		}
	}

	for _, id := range []string{"", "US0378331006", "us0378331005", "265598", "US037833100A"} {
		if underlierISIN(id) {
			t.Fatal("invalid ISIN", id)
		}
	}

	row := map[string]string{
		"underlyingConid":      "265598",
		"underlyingSecurityID": "US0378331005",
		"underlyingSymbol":     "AAPL",
		"putCall":              "C",
		"expiry":               "2026-01-16",
		"strike":               "14.500",
		"currency":             "USD",
	}

	terms, err := optionTerms(row)
	if err != nil {
		t.Fatal(err)
	}

	ids := terms.Fields().Underlier.Identifiers()
	if len(ids) != 1 || ids[0].Kind() != source.ISIN || ids[0].Field() != "underlyingSecurityID" {
		t.Fatal(ids)
	}

	id, ok := occIdentifier("F     260116C00014500", terms)
	if !ok || id.Kind() != source.OCC {
		t.Fatal(id, ok)
	}

	for _, symbol := range []string{"F 260116C00014500", " F    260116C00014500", "F A   260116C00014500", "F     260117C00014500", "F     260116P00014500", "F     260116C00014501"} {
		if _, ok = occIdentifier(symbol, terms); ok {
			t.Fatal("incompatible OCC", symbol)
		}
	}

	row["underlyingSecurityID"] = "not-an-isin"

	terms, err = optionTerms(row)
	if err != nil || len(terms.Fields().Underlier.Identifiers()) != 0 {
		t.Fatal(terms, err)
	}
}

func TestUnderlierISINDoesNotRequireNativeReference(t *testing.T) {
	for _, native := range []string{"", "0"} {
		terms, err := optionTerms(
			map[string]string{"underlyingConid": native, "underlyingSecurityID": "US0378331005"},
		)
		if err != nil || terms.Fields().Underlier.Kind() != source.UnknownInstrument ||
			!terms.Fields().Underlier.Reference().IsZero() ||
			len(terms.Fields().Underlier.Identifiers()) != 1 {
			t.Fatal("ISIN-only underlier lost", native, terms, err)
		}
	}
}
