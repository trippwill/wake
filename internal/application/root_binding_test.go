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

func TestUnderlierCannotUseAnOptionBinding(t *testing.T) {
	for _, anonymous := range []bool{true, false} {
		ref, _ := source.NewReference("synthetic", "contract", "", "reported")
		if anonymous {
			ref = source.Reference{}
		}

		canonicalRef, _ := source.NewReference("synthetic", "contract", "", "canonical")
		id, _ := source.NewIdentifier(source.ISIN, "synthetic-underlier", "isin")

		underlier, err := source.NewInstrument(
			source.InstrumentInput{
				Kind:        source.UnknownInstrument,
				Reference:   ref,
				Identifiers: []source.Identifier{id},
			},
		)
		if err != nil {
			t.Fatal(err)
		}

		terms, err := source.NewOptionTerms(source.OptionInput{Underlier: underlier})
		if err != nil {
			t.Fatal(err)
		}

		original, err := source.NewInstrument(
			source.InstrumentInput{Kind: source.Option, Reference: ref, Name: "Synthetic option", Option: terms},
		)
		if err != nil {
			t.Fatal(err)
		}

		f := original.Fields()
		f.Reference = canonicalRef

		canonical, err := source.NewInstrument(f)
		if err != nil {
			t.Fatal(err)
		}

		got, err := CalculateBoundRoot(
			canonical,
			[]source.Instrument{original},
			map[source.Reference]RootBinding{ref: {Reported: original, Canonical: canonical}},
		)
		if err != nil || got.Conflict == anonymous || got.Underlier.Kind() != source.UnknownInstrument ||
			len(got.Underlier.Identifiers()) != 1 ||
			got.Underlier.Identifiers()[0] != id {
			t.Fatal("underlier was recursively substituted with an option binding", anonymous, got, err)
		}
	}
}
