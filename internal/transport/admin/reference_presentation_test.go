// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package admin

import (
	"bytes"
	"strings"
	"testing"

	"go.trippwill.dev/wake/financial"
	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/source"
)

func TestDebtFactsDistinguishesAbsentAndInapplicableTerms(t *testing.T) {
	zero, _ := financial.ParseQuantity("0.000")
	for _, tc := range []struct {
		name         string
		input        source.DebtInput
		want, absent []string
	}{
		{"bill", source.DebtInput{Type: source.TreasuryBill}, []string{"None (Treasury bill)", "Not provided", "Maturity", "Original issue date"}, []string{"Unknown", "Coupon rate (reported)", "Payments per year", "Dated date"}},
		{"explicit bill zero", source.DebtInput{Type: source.TreasuryBill, Coupon: zero}, []string{"Coupon rate (reported)", "0.000"}, []string{"Unknown", "Payments per year"}},
		{"bond", source.DebtInput{Type: source.TreasuryBond}, []string{"Coupon rate (reported)", "Payments per year", "Dated date", "Not provided"}, []string{"Unknown", "None (Treasury bill)"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			terms, err := source.NewDebtTerms(tc.input)
			if err != nil {
				t.Fatal(err)
			}

			var out bytes.Buffer
			if err = DebtFacts(terms).Render(t.Context(), &out); err != nil {
				t.Fatal(err)
			}

			for _, want := range tc.want {
				if !strings.Contains(out.String(), want) {
					t.Fatalf("missing %q: %s", want, &out)
				}
			}

			for _, absent := range tc.absent {
				if strings.Contains(out.String(), absent) {
					t.Fatalf("unexpected %q: %s", absent, &out)
				}
			}

			if strings.Contains(out.String(), "0000-00-00") {
				t.Fatal("absent debt date rendered as a calendar date", out.String())
			}
		})
	}
}

func TestReferenceEvidenceHasOneActionAndReadableSeparatePayloads(t *testing.T) {
	evidence := application.ReferenceEvidence{
		Retrieved: "2026-11-02",
		Request:   `[{"idType":"ID_BB_GLOBAL","idValue":"synthetic"}]`,
		Response:  `[{"data":[{"figi":"synthetic","name":"<script>"}]}]`,
	}

	var out bytes.Buffer
	if err := EvidencePage(evidence).Render(t.Context(), &out); err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{"<summary>Inspect retained request and response</summary>", `aria-label="Retained request"`, `aria-label="Retained response"`, "\n", "&#34;idType&#34;", "&lt;script&gt;"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q: %s", want, &out)
		}
	}

	if strings.Contains(out.String(), `href="#original-content"`) {
		t.Fatal("duplicate dead-end action")
	}

	if got := readableJSON("not JSON\n"); got != "not JSON\n" {
		t.Fatal("non-JSON response changed", got)
	}
}

func presentationInstrument(t *testing.T) application.InstrumentDetail {
	t.Helper()

	identifier, err := source.NewIdentifier(source.CompositeFIGI, "synthetic-composite", "figi")
	if err != nil {
		t.Fatal(err)
	}

	reported, err := source.NewInstrument(
		source.InstrumentInput{
			Kind:        source.Equity,
			Name:        "Synthetic issuer",
			Ticker:      "SYN",
			Identifiers: []source.Identifier{identifier},
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	instrument, err := application.NewInstrument(
		"instrument",
		"Synthetic issuer",
		"SYN",
		"equity",
		"resolved",
		"synthetic-share",
		"evidence",
		reported,
		"rule",
		1,
		application.AutomaticSelection,
		application.ReferenceMetadata{Allocation: "share_class"},
	)
	if err != nil {
		t.Fatal(err)
	}

	listing, err := application.NewListing("listing", "instrument", "NASDAQ", "SYN", "USD", "")
	if err != nil {
		t.Fatal(err)
	}

	return application.InstrumentDetail{
		Instrument: instrument,
		Listings:   []application.Listing{listing},
		References: []application.ReferenceAttempt{{Evidence: "evidence", Status: "resolved"}},
	}
}

func TestInstrumentPresentationExplainsFIGILevels(t *testing.T) {
	var out bytes.Buffer
	if err := InstrumentPage(presentationInstrument(t)).Render(t.Context(), &out); err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{"Venue FIGI", "Not established", "Share-class FIGI", "/reference-evidence/evidence", "Composite FIGI: synthetic-composite"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q: %s", want, &out)
		}
	}

	if strings.Contains(out.String(), "unclassified_figi") {
		t.Fatal("internal identifier enum displayed")
	}
}
