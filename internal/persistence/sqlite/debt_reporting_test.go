// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/source"
)

func TestDebtReportedValuesSurviveCorrectionAndReplay(t *testing.T) {
	for _, tc := range []struct {
		name, category, country, currency, quantity, price, principal, proceeds, commission, net, accrued string
	}{
		{"gilt", "BOND", "GB", "GBP", "3000", "96.543210987", "2896.30", "-2896.30", "-4.25", "-2900.55", "-8.765432100"},
		{"bill", "BILL", "US", "USD", "7000", "98.765432123456789", "6913.58", "-6913.58", "-3.50", "-6917.08", "0.000"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := coverageStore(t)
			ctx := t.Context()
			row := fmt.Sprintf(
				`<TradeConfirm accountId="U1" assetCategory="%s" issuerCountryCode="%s" conid="synthetic-debt" description="Synthetic debt" execID="debt-fill" transactionType="ExchTrade" levelOfDetail="EXECUTION" buySell="BUY" tradeDate="2026-10-20" quantity="%s" price="%s" currency="%s" amount="%s" proceeds="%s" commission="%s" commissionCurrency="%s" tax="0.00" netCash="%s" netCashWithBillable="%s" accruedInt="%s"/>`,
				tc.category,
				tc.country,
				tc.quantity,
				tc.price,
				tc.currency,
				tc.principal,
				tc.proceeds,
				tc.commission,
				tc.currency,
				tc.net,
				tc.net,
				tc.accrued,
			)
			coverageReport(t, s, "reported", row)

			view, err := s.Dashboard(ctx)
			if err != nil || len(view.Executions) != 1 {
				t.Fatal(view, err)
			}

			execution := view.Executions[0]

			terms := execution.Terms()
			f := terms.Fields()
			if terms.QuantityUnit() != "nominal" || terms.PriceBasis() != "" || f.Quantity.Value() != tc.quantity ||
				f.Price.Amount() != tc.price || f.Price.Currency().String() != tc.currency || f.Gross.Amount() != tc.principal ||
				f.Proceeds.Amount() != tc.proceeds || f.Net.Amount() != tc.net || f.NetWithBillable.Amount() != tc.net ||
				f.AccruedInterest.Amount() != tc.accrued {
				t.Fatal("reported values were inferred, combined or rounded", f)
			}

			for role, amount := range map[string]string{"commission": tc.commission, "tax": "0.00"} {
				i := slices.IndexFunc(
					f.Charges,
					func(c source.Charge) bool { return c.Field() == role && c.Scope() == "total" },
				)
				if i < 0 || f.Charges[i].Money().Amount() != amount ||
					f.Charges[i].Money().Currency().String() != tc.currency {
					t.Fatal("reported charge lost", role, f.Charges)
				}
			}

			encoded, err := encodeTrade(terms)
			if err != nil {
				t.Fatal(err)
			}

			decoded, err := decodeTrade(encoded)
			if err != nil {
				t.Fatal(err)
			}

			roundtrip, err := encodeTrade(decoded)
			if err != nil || !bytes.Equal(encoded, roundtrip) || bytes.Contains(encoded, []byte("QuotationBasis")) {
				t.Fatal("claim codec changed reported values", err)
			}

			published, err := s.Resource(ctx, "activity", execution.ID())
			if err != nil || published.Activity.PriceBasis != nil || published.Activity.Price.Amount != tc.price ||
				published.Activity.Net.Amount != tc.net || published.Activity.AccruedInterest.Amount != tc.accrued {
				t.Fatal("published debt values", published, err)
			}

			wire, err := json.Marshal(published)
			if err != nil || !bytes.Contains(wire, []byte(`"price_basis":null`)) {
				t.Fatal("debt price basis must be explicit null", string(wire), err)
			}

			var unit string
			if err = s.db.QueryRowContext(ctx, `SELECT price_unit FROM admin_activity WHERE id=?`, execution.ID()).
				Scan(&unit); err != nil ||
				unit != tc.currency {
				t.Fatal("empty basis left a price unit separator", unit, err)
			}

			form, err := s.CorrectionForm(ctx, "activity", execution.ID())
			if err != nil || slices.Contains(form.Fields, "quotation_basis") {
				t.Fatal("obsolete correction offered", form, err)
			}

			command, err := application.NewDecision(application.DecisionInput{
				TargetKind:       "activity",
				TargetID:         execution.ID(),
				Scope:            "net",
				Text:             "-1234.5678900001",
				Currency:         tc.currency,
				Action:           "set",
				Submission:       rand.Text(),
				ExpectedRevision: form.Revision,
			})
			if err != nil {
				t.Fatal(err)
			}

			if err = s.CheckCorrection(ctx, command); err != nil {
				t.Fatal("net correction preflight", err)
			}

			retainDecision(t, s, "activity", execution.ID(), "net", "-1234.5678900001", tc.currency, "set")
			coverageReport(t, s, "repeat", row)

			corrected, err := s.ExecutionDetail(ctx, execution.ID())
			if err != nil || corrected.Execution.Terms().Fields().Net.Amount() != "-1234.5678900001" ||
				corrected.Execution.Terms().Fields().AccruedInterest != f.AccruedInterest {
				t.Fatal("repeat overwrote correction or combined interest", corrected, err)
			}

			retainDecision(t, s, "activity", execution.ID(), "net", "", "", "release")

			restored, err := s.ExecutionDetail(ctx, execution.ID())
			if err != nil || restored.Execution.Terms().Fields().Net != f.Net {
				t.Fatal("release changed reported net", restored, err)
			}

			coverageReport(t, s, "disputed", strings.Replace(row, `netCash="`+tc.net+`"`, `netCash="-1.00"`, 1))

			disputed, err := s.Resource(ctx, "activity", execution.ID())
			if err != nil || !disputed.Activity.Conflict || disputed.Activity.Net.Amount != tc.net {
				t.Fatal("monetary disagreement lost", disputed, err)
			}
		})
	}
}

func TestDebtMissingMonetaryRolesStayUnknown(t *testing.T) {
	s := coverageStore(t)
	coverageReport(
		t,
		s,
		"unknown-money",
		`<TradeConfirm accountId="U1" assetCategory="BILL" issuerCountryCode="US" conid="synthetic-bill" execID="bill-fill" transactionType="ExchTrade" levelOfDetail="EXECUTION" buySell="BUY" tradeDate="2026-10-20" quantity="3000" price="97.123456789" currency="USD"/>`,
	)

	view, err := s.Dashboard(t.Context())
	if err != nil || len(view.Executions) != 1 {
		t.Fatal(view, err)
	}

	f := view.Executions[0].Terms().Fields()
	if f.Gross.Amount() != "" || f.Net.Amount() != "" || f.Proceeds.Amount() != "" ||
		f.AccruedInterest.Amount() != "" ||
		f.NetWithBillable.Amount() != "" {
		t.Fatal("missing monetary roles were calculated", f)
	}
}
