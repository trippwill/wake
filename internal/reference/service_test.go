// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package reference

import (
	"testing"

	"go.trippwill.dev/wake/source"
)

func TestFIGILookupUsesOnlyIdentifier(t *testing.T) {
	figi, err := source.NewIdentifier(source.UnclassifiedFIGI, "BBG000BPH459", "figi")
	if err != nil {
		t.Fatal(err)
	}

	isin, err := source.NewIdentifier(source.ISIN, "US5949181045", "isin")
	if err != nil {
		t.Fatal(err)
	}

	for _, venue := range []string{"NASDAQ", "NYSE", "ARCA", "LSE", ""} {
		t.Run(venue, func(t *testing.T) {
			instrument, err := source.NewInstrument(
				source.InstrumentInput{
					Kind:        source.Equity,
					Venue:       venue,
					Identifiers: []source.Identifier{isin, figi},
				},
			)
			if err != nil {
				t.Fatal(err)
			}

			lookup, ok := job(Pending{Instrument: instrument})
			if !ok || lookup.Key() != `{"idType":"ID_BB_GLOBAL","idValue":"BBG000BPH459"}` {
				t.Fatalf("FIGI lookup contains additional filters: %s", lookup.Key())
			}
		})
	}
}
