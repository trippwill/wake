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
