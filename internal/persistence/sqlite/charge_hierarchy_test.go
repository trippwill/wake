// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/providers/ibkrflex"
)

func TestChargeHierarchyPublishesWithoutRedundantSupport(t *testing.T) {
	s := coverageStore(t)
	base := equityRow("U1", "charge-fill", "100")
	coverageReport(t, s, "charge-base", base)
	before, _ := snapshotState(t.Context(), s)
	row := strings.Replace(
		base,
		`/>`,
		` thirdPartyRegulatoryCommission="-0.00200" regFINRATradingActivityFee="-0.00100" regSection31TransactionFee="0.0000"/>`,
		1,
	)
	normalizeCoverageReport(t, s, "charge-details", row)

	if _, err := s.db.Exec(
		`CREATE TRIGGER reject_charge BEFORE UPDATE ON published_resources WHEN NEW.kind='activity' BEGIN SELECT RAISE(ABORT,'synthetic charge failure'); END`,
	); err != nil {
		t.Fatal(err)
	}

	if err := s.Process(t.Context(), "charge-details", ibkrflex.Version); err == nil {
		t.Fatal("failed charge publication committed")
	}

	rolledBack, _ := snapshotState(t.Context(), s)
	if rolledBack.Version != before.Version {
		t.Fatal("partial charge publication escaped rollback")
	}

	if _, err := s.db.Exec(`DROP TRIGGER reject_charge`); err != nil {
		t.Fatal(err)
	}

	if err := s.Process(t.Context(), "charge-details", ibkrflex.Version); err != nil {
		t.Fatal(err)
	}

	view, err := s.Dashboard(t.Context())
	if err != nil || len(view.Executions) != 1 {
		t.Fatal(view, err)
	}

	id := view.Executions[0].ID()

	resource, err := s.Resource(t.Context(), "activity", id)
	if err != nil {
		t.Fatal(err)
	}

	root := resource.Activity.Charges[0]
	if root.Total.Amount != "-0.000000000001" || root.Category != "commission" || len(root.Components) != 1 ||
		len(root.Components[0].Components) != 2 {
		t.Fatal("nested wire values lost", root)
	}

	after, _ := snapshotState(t.Context(), s)
	if after.Version == before.Version {
		t.Fatal("new breakdown did not publish")
	}

	support := tableCount(t, s, "execution_observations")
	for _, record := range []struct{ id, row string }{
		{"charge-repeat", row},
		{"charge-reordered", strings.Replace(row, `regFINRATradingActivityFee="-0.00100" regSection31TransactionFee="0.0000"`, `regSection31TransactionFee="0.0000" regFINRATradingActivityFee="-0.00100"`, 1)},
		{"charge-subset", base},
	} {
		coverageReport(t, s, record.id, record.row)

		if tableCount(t, s, "execution_observations") != support {
			t.Fatal("known charges added supporting links")
		}

		current, _ := snapshotState(t.Context(), s)
		if current.Version != after.Version {
			t.Fatal("known breakdown republished")
		}

		appearances := observationRowsForTest(t, s, "artifact", record.id)
		if !slices.ContainsFunc(appearances, func(row application.Observation) bool {
			return row.ActivityID == id && row.Outcome == application.ObservationNoContribution
		}) {
			t.Fatal("discarded support erased report appearance", appearances)
		}
	}

	encoded, err := encodeTrade(view.Executions[0].Terms())
	if err != nil {
		t.Fatal(err)
	}

	decoded, err := decodeTrade(encoded)
	if err != nil || !reflect.DeepEqual(decoded.Fields().Charges, view.Executions[0].Terms().Fields().Charges) {
		t.Fatal(decoded, err)
	}

	profile := testProfile(s.owners[0].Name())
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(t.Context(), profile, nil)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = reopened.Close() })

	if err = reopened.Process(t.Context(), "charge-details", ibkrflex.Version); err != nil {
		t.Fatal(err)
	}

	restored, err := reopened.Resource(t.Context(), "activity", id)
	current, _ := snapshotState(t.Context(), reopened)
	if err != nil || current.Version != after.Version ||
		!reflect.DeepEqual(restored.Activity.Charges, resource.Activity.Charges) {
		t.Fatal("restart changed charge hierarchy", restored, err)
	}
}

func TestStandaloneChargeTotalFollowsCashCorrectionWithoutCombiningTax(t *testing.T) {
	for _, feeFirst := range []bool{true, false} {
		t.Run(map[bool]string{true: "fee-first", false: "tax-first"}[feeFirst], func(t *testing.T) {
			s := coverageStore(t)
			fee := strings.Replace(cashRow, `type="Dividends"`, `type="Other Fees"`, 1)
			tax := `<SalesTax accountId="U1" transactionID="tax1" taxableTransactionID="cash1" salesTax="-0.2300" currency="USD" date="2026-10-20"/>`

			reports := []string{movementReport("CashTransactions", fee), movementReport("SalesTaxes", tax)}
			if !feeFirst {
				reports[0], reports[1] = reports[1], reports[0]
			}

			for _, report := range reports {
				acquireRealized(t, s, report)
			}

			rows, err := s.queries.Executions(t.Context())
			if err != nil || len(rows) != 2 {
				t.Fatal(rows, err)
			}

			id := ""
			for _, row := range rows {
				value := realizedActivity(t, s, row.ID).Activity
				if len(value.Charges) != 1 || value.Charges[0].Total == nil ||
					*value.Charges[0].Total != value.Cash.Amount ||
					len(value.Charges[0].Components) != 0 {
					t.Fatal("posting charge invented a combined total", value)
				}

				if value.Cash.Category == "fee" {
					id = row.ID
				} else if len(value.Relationships) != 1 || value.Relationships[0].TargetID == nil {
					t.Fatal("tax link lost", value)
				}
			}

			retainDecision(t, s, "activity", id, "cash_amount", "13.0000", "USD", "set")

			corrected := realizedActivity(t, s, id).Activity
			if corrected.Cash.Amount.Amount != "13.0000" || corrected.Charges[0].Total.Amount != "13.0000" {
				t.Fatal("cash correction left an independent charge total", corrected)
			}

			retainDecision(t, s, "activity", id, "cash_amount", "", "", "release")

			released := realizedActivity(t, s, id).Activity
			if released.Charges[0].Total.Amount != "12.34000000000000001" {
				t.Fatal("release lost exact source total", released)
			}
		})
	}
}
