// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package snaptrade

import (
	"fmt"
	"testing"
	"time"

	"go.trippwill.dev/wake/provider"
	"go.trippwill.dev/wake/source"
)

const (
	positionStock  = `{"instrument":{"kind":"stock","id":"stock-a","symbol":"SYN","raw_symbol":"SYN","currency":"USD","exchange":"XNYS"},"units":"1.230000000000000001","price":"4.500","cost_basis":"3.200","currency":"USD"}`
	positionBond   = `{"instrument":{"kind":"bond","id":"bond-a","symbol":"BOND","raw_symbol":"BOND"},"units":"10.00"}`
	positionOption = `{"instrument":{"kind":"option","id":"option-a","symbol":"SYN   261218P00240000","option_type":"PUT","strike_price":"240.00","expiration_date":"2026-12-18","multiplier":"10","underlying":{"kind":"stock","id":"stock-a","symbol":"SYN","raw_symbol":"SYN","currency":"USD","exchange":"XNYS"}},"units":"-2"}`
)

func TestNormalizeFidelityPositionSets(t *testing.T) {
	reg, err := Registration(Options{})
	if err != nil {
		t.Fatal(err)
	}

	account, err := (accountScope{Broker: fidelity}).reference("account-a")
	if err != nil {
		t.Fatal(err)
	}

	const date = `,"data_freshness":{"as_of":"2026-10-21T00:15:00.1200+01:00"}`
	for _, tc := range []struct {
		name, body      string
		count           int
		rejected        uint64
		complete, dated bool
	}{
		{"mixed", fmt.Sprintf(`{"results":[%s,%s,%s,{"instrument":{"kind":"cash","code":"USD"},"units":"5"},{"instrument":{"kind":"mutualfund","id":"fund-a","symbol":"CASHFUND","raw_symbol":"CASHFUND"},"units":"10","cash_equivalent":true}]%s}`, positionStock, positionBond, positionOption, date), 4, 0, true, true},
		{"empty", `{"results":[]` + date + `}`, 0, 0, true, true},
		{"unclassified", `{"results":[{"instrument":{"kind":"other","id":"unclassified-a","symbol":"SYN.AB","raw_symbol":"SYN.AB","exchange":"XNYS","currency":"USD"},"units":"-2.5000"}]` + date + `}`, 1, 0, true, true},
		{"absent", `{"results":null` + date + `}`, 0, 0, false, true},
		{"undated", `{"results":[]}`, 0, 0, false, false},
		{"unsupported", `{"results":[` + positionStock + `,{"instrument":{"kind":"future","id":"future-a"},"units":"1"}]` + date + `}`, 0, 1, false, true},
		{"invalid quantity", `{"results":[{"instrument":{"kind":"stock","id":"stock-a"},"units":null}]` + date + `}`, 0, 1, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			artifact, err := provider.NewArtifact(positionsFormat, "application/json", time.Now(), []byte(tc.body))
			if err != nil {
				t.Fatal(err)
			}

			sink := &testClaims{}
			result, err := reg.Normalizer().Normalize(t.Context(), artifact.WithSourceAccount(account), sink)
			if err != nil || len(sink.positions) != 1 || result.Rejected() != tc.rejected {
				t.Fatalf("normalization result: %+v %v", result, err)
			}

			set := sink.positions[0].Fields()
			if set.Complete != tc.complete || len(set.Positions) != tc.count || set.At.IsZero() == tc.dated {
				t.Fatalf("unexpected set: %+v", set)
			}

			if tc.dated && (set.Date.String() != "2026-10-20" || set.At.String() != "2026-10-20T23:15:00.1200Z" ||
				set.TimeBasis != source.PositionSourceFetched) {
				t.Fatalf("lost fetch time: %+v", set)
			}

			if tc.name == "unclassified" {
				holding := set.Positions[0]
				if holding.Instrument().Kind() != source.UnknownInstrument || holding.Unit() != "units" ||
					holding.Quantity().Value() != "-2.5000" || holding.Instrument().Venue() != "XNYS" {
					t.Fatalf("lost reported terms for unclassified holding: %+v", holding)
				}
			}

			if tc.name == "mixed" {
				equity, bond, option, fund := set.Positions[0], set.Positions[1], set.Positions[2], set.Positions[3]
				if equity.Quantity().Value() != "1.230000000000000001" ||
					equity.UnitCostBasis().
						Amount() !=
						"3.200" || equity.UnitCostBasisQuotationBasis() != source.PerShare ||
					bond.UnitCostBasis().Amount() != "" ||
					bond.Unit() != "units" || bond.Quantity().Value() != "10.00" || fund.Instrument().Kind() != source.Fund {
					t.Fatalf("lost position terms: %+v", set)
				}

				terms := option.Instrument().Option().Fields()
				if option.Quantity().Value() != "-2" || option.Unit() != "contracts" || terms.Right != source.Put ||
					terms.Strike.Amount() != "240.00" || terms.Multiplier.Value() != "10" ||
					terms.Underlier.Reference() != equity.Instrument().Reference() {
					t.Fatalf("lost option terms: %+v", terms)
				}
			}
		})
	}
}
