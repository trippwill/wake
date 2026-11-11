// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package ibkrflex

import (
	"strings"
	"testing"
	"time"

	"go.trippwill.dev/wake/provider"
	"go.trippwill.dev/wake/source"
)

const positionRow = `<OpenPosition accountId="U1" assetCategory="STK" conid="100" symbol="SYN" levelOfDetail="SUMMARY" position="-0.0000000012300" side="Short" currency="USD" costBasisPrice="0.1234567890000" costBasisMoney="0.0000" reportDate="2026-11-04"/>`

func positionReport(date, sections string) string {
	return `<FlexQueryResponse type="AF"><FlexStatements><FlexStatement accountId="U1" fromDate="2026-11-01" toDate="` + date + `">` + sections + `</FlexStatement></FlexStatements></FlexQueryResponse>`
}

func TestPositionSummaryPreservesExactValuesAndWithholdsIncompleteSets(t *testing.T) {
	reg, _ := Registration(Options{})
	for _, tc := range []struct {
		name, sections, reason string
		count                  int
	}{
		{"summary and lots", "<OpenPositions>" + positionRow + strings.ReplaceAll(positionRow, `levelOfDetail="SUMMARY"`, `levelOfDetail="LOT"`) + "</OpenPositions>", "", 1},
		{"empty", "<OpenPositions/>", "", 0},
		{"omitted", "", "not_reported", 0},
		{"lot only", "<OpenPositions>" + strings.ReplaceAll(positionRow, `levelOfDetail="SUMMARY"`, `levelOfDetail="LOT"`) + "</OpenPositions>", "incomplete", 0},
		{"bad row", "<OpenPositions>" + positionRow + `<OpenPosition levelOfDetail="SUMMARY"/>` + "</OpenPositions>", "incomplete", 0},
		{"mixed dates", "<OpenPositions>" + positionRow + strings.ReplaceAll(positionRow, "2026-11-04", "2026-11-03") + "</OpenPositions>", "incomplete", 0},
		{"mixed dates ascending", "<OpenPositions>" + strings.ReplaceAll(positionRow, "2026-11-04", "2026-11-03") + positionRow + "</OpenPositions>", "incomplete", 0},
		{"unsupported", "<OpenPositions>" + strings.ReplaceAll(positionRow, `assetCategory="STK"`, `assetCategory="FUT"`) + "</OpenPositions>", "incomplete", 0},
		{"duplicate section", "<OpenPositions/><OpenPositions/>", "incomplete", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, _ := provider.NewArtifact(
				ActivityFormat,
				"application/xml",
				time.Now(),
				[]byte(positionReport("2026-11-04", tc.sections)),
			)

			sink := &testClaims{}
			result, err := reg.Normalizer().Normalize(t.Context(), a, sink)
			if err != nil || len(sink.positions) != 1 || result.Counts().PositionSets != 1 {
				t.Fatal(result, err, sink)
			}

			f := sink.positions[0].Fields()
			if f.Reason != tc.reason || len(f.Positions) != tc.count || !f.At.IsZero() ||
				f.Date.String() != "2026-11-04" {
				t.Fatal(f)
			}

			if tc.count > 0 &&
				(f.Positions[0].Quantity().Value() != "-0.0000000012300" ||
					f.Positions[0].UnitCostBasis().Amount() != "0.1234567890000" ||
					f.Positions[0].UnitCostBasisQuotationBasis() != source.PerShare) {
				t.Fatal(f)
			}
		})
	}
}

func TestPositionTermsKeepOptionsAndDebtDistinct(t *testing.T) {
	for _, tc := range []struct{ asset, unit string }{{"OPT", "contracts"}, {"BOND", "nominal"}} {
		row := map[string]string{
			"accountId":     "U1",
			"assetCategory": tc.asset,
			"conid":         "123",
			"position":      "-10.0000",
			"side":          "Short",
			"currency":      "USD",
		}
		if tc.asset == "OPT" {
			row["putCall"] = "P"
			row["strike"] = "123.4500"
			row["expiry"] = "2026-12-18"
			row["multiplier"] = "10"
			row["underlyingConid"] = "100"
		}

		p, err := reportedPosition(row, "U1")
		if err != nil || p.Unit() != tc.unit {
			t.Fatal(p, err)
		}

		if tc.asset == "OPT" &&
			(p.Instrument().Option().Fields().Multiplier.Value() != "10" || p.Instrument().Option().Fields().Strike.Amount() != "123.4500") {
			t.Fatal(p)
		}

		if tc.asset == "BOND" && p.Instrument().Debt().Fields().Currency.String() != "USD" {
			t.Fatal(p)
		}
	}
}

func TestIncompletePositionsUseReportedDatesBeforeStatementEnd(t *testing.T) {
	for _, tc := range []struct{ name, rows, date string }{
		{"malformed quantity", strings.ReplaceAll(positionRow, `position="-0.0000000012300"`, `position="invalid"`), "2026-11-04"},
		{"unsupported family", strings.ReplaceAll(positionRow, `assetCategory="STK"`, `assetCategory="FUT"`), "2026-11-04"},
		{"mixed dates", positionRow + strings.ReplaceAll(positionRow, "2026-11-04", "2026-11-05"), "2026-11-05"},
		{"mixed dates reversed", strings.ReplaceAll(positionRow, "2026-11-04", "2026-11-05") + positionRow, "2026-11-05"},
		{"missing date", strings.ReplaceAll(positionRow, `reportDate="2026-11-04"`, ""), "2026-11-06"},
		{"lot only", strings.ReplaceAll(positionRow, `levelOfDetail="SUMMARY"`, `levelOfDetail="LOT"`), "2026-11-06"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sink := &testClaims{}

			var counts provider.Counts

			err := normalizePositions(
				t.Context(),
				[]byte(positionReport("2026-11-06", "<OpenPositions>"+tc.rows+"</OpenPositions>")),
				sink,
				&counts,
			)
			if err != nil || len(sink.positions) != 1 {
				t.Fatal(err, sink)
			}

			f := sink.positions[0].Fields()
			if f.Complete || f.Reason != "incomplete" || f.Date.String() != tc.date || len(f.Positions) != 0 ||
				!f.At.IsZero() {
				t.Fatal(f)
			}
		})
	}
}
