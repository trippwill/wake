// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package application

import (
	"fmt"
	"testing"

	"go.trippwill.dev/wake/source"
)

func TestBoundIdentityEnrichmentLimitRetainsBaselineForReview(t *testing.T) {
	a, _ := source.NewReference("synthetic", "contract", "", "a")
	b, _ := source.NewReference("synthetic", "contract", "", "b")
	instrument := func(ref source.Reference, kind source.IdentifierKind, count int) source.Instrument {
		t.Helper()

		var ids []source.Identifier
		for i := range count {
			id, err := source.NewIdentifier(kind, fmt.Sprint(i), "identifier")
			if err != nil {
				t.Fatal(err)
			}

			ids = append(ids, id)
		}

		out, err := source.NewInstrument(
			source.InstrumentInput{Reference: ref, Kind: source.Equity, Identifiers: ids},
		)
		if err != nil {
			t.Fatal(err)
		}

		return out
	}
	base := instrument(a, source.ISIN, 64)
	original := instrument(b, source.CUSIP, 0)
	claim := instrument(b, source.CUSIP, 64)

	got, err := CalculateBoundRoot(
		base,
		[]source.Instrument{claim},
		map[source.Reference]RootBinding{b: {Reported: original, Canonical: base}},
	)
	if err != nil || !got.Conflict || !CompatibleInstrument(base, got.Reported) ||
		len(got.Reported.Identifiers()) != 64 {
		t.Fatal("bounded enrichment lost baseline or stopped processing", got, err)
	}
}

func TestAnonymousBindingsDoNotCreateAnAbsentUnderlier(t *testing.T) {
	ref, _ := source.NewReference("synthetic", "contract", "", "identified")
	optionRef, _ := source.NewReference("synthetic", "contract", "", "option")
	id, _ := source.NewIdentifier(source.ISIN, "synthetic", "isin")
	anonymous, _ := source.NewInstrument(
		source.InstrumentInput{Kind: source.Equity, Identifiers: []source.Identifier{id}},
	)
	canonical, _ := source.NewInstrument(source.InstrumentInput{Kind: source.Equity, Reference: ref})
	terms, _ := source.NewOptionTerms(source.OptionInput{})

	option, err := source.NewInstrument(
		source.InstrumentInput{Kind: source.Option, Reference: optionRef, Option: terms},
	)
	if err != nil {
		t.Fatal(err)
	}

	got, err := CalculateBoundRoot(
		option,
		[]source.Instrument{option},
		map[source.Reference]RootBinding{{}: {Reported: anonymous, Canonical: canonical}},
	)
	if err != nil || got.Conflict || got.Underlier.Kind() != "" {
		t.Fatal("anonymous binding replaced an absent underlier", got, err)
	}
}
