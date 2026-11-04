// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"context"
	"strings"
	"testing"
	"time"

	"go.trippwill.dev/wake/financial"
	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/host"
	"go.trippwill.dev/wake/provider"
	"go.trippwill.dev/wake/providers/ibkrflex"
	"go.trippwill.dev/wake/source"
)

func TestMovementPartialObservationsCollectivelyEstablishRequiredTerms(t *testing.T) {
	account, _ := source.NewReference("ibkr", "account", "", "U1")
	event, _ := source.NewReference("fixture", "movement", "U1", "partial")
	instrument, _ := source.NewInstrument(source.InstrumentInput{Kind: source.Equity, Ticker: "SYN"})
	quantity, _ := financial.NewQuantity("1.2300")
	date, _ := financial.ParseDate("2026-10-20")
	currency, _ := financial.ParseCurrency("USD")
	amount, _ := financial.NewMoney("-12.3400", currency)

	transfer, _ := source.NewSecurityTransfer(
		source.SecurityTransferInput{
			Location:      "row",
			Account:       account,
			Event:         event,
			EventDistinct: true,
			Instrument:    instrument,
			Direction:     source.TransferIn,
			Quantity:      quantity,
			Date:          date,
		},
	)
	cash, _ := source.NewCashActivity(
		source.CashActivityInput{
			Location:      "row",
			Account:       account,
			Event:         event,
			EventDistinct: true,
			Category:      source.CashFee,
			Amount:        amount,
			Date:          date,
		},
	)
	for _, tc := range []struct {
		name     string
		value    source.ActivityObservation
		required source.ActivityFieldName
	}{{"security-transfer", transfer.Observation(), source.FieldQuantity}, {"cash", cash.Observation(), source.FieldCashAmount}} {
		t.Run(tc.name, func(t *testing.T) {
			s := coverageStore(t)

			subject := tc.value.Fields()
			base := []source.ActivityField{}

			var missing source.ActivityField
			for _, field := range subject.Fields {
				if field.Name() == tc.required {
					missing = field
				} else {
					base = append(base, field)
				}
			}

			processPartial(t, s, "incomplete", partialObservation(t, subject, base...))

			if tableCount(t, s, "executions") != 0 {
				t.Fatal("incomplete movement accepted")
			}

			processPartial(t, s, "completion", partialObservation(t, subject, missing))

			if tableCount(t, s, "executions") != 1 {
				t.Fatal("compatible partials did not establish movement")
			}

			before, _ := snapshotState(t.Context(), s)
			processPartial(t, s, "repeat-completion", partialObservation(t, subject, missing))

			after, _ := snapshotState(t.Context(), s)
			if before.Version != after.Version {
				t.Fatal("repeated partial changed publication")
			}
		})
	}
}

const transferRow = `<Transfer accountId="U1" transactionID="transfer1" assetCategory="STK" subCategory="COMMON" conid="100" symbol="TEST" description="Synthetic security" currency="USD" levelOfDetail="TRANSFER" type="ACATS" direction="IN" quantity="12.0000000000001" date="2026-10-20" dateTime="2026-10-20" reportDate="2026-10-21" settleDate="2026-10-22" transferPrice="123.4500" positionAmount="1481.400000" cost="999.0000" pnlAmount="0.0000" deliveringBroker="Synthetic delivering broker" company="Synthetic broker" account="SYNTHETIC"/>`

func movementReport(section, rows string) string {
	return `<FlexQueryResponse type="AF"><FlexStatements><FlexStatement accountId="U1" fromDate="2026-10-20" toDate="2026-10-21"><` + section + `>` + rows + `</` + section + `></FlexStatement></FlexStatements></FlexQueryResponse>`
}

const cashRow = `<CashTransaction accountId="U1" transactionID="cash1" actionID="action1" levelOfDetail="DETAIL" type="Dividends" amount="12.34000000000000001" currency="USD" dateTime="2026-10-20" reportDate="2026-10-21" availableForTradingDate="2026-10-23" description="Synthetic dividend"/>`

func TestCashPostingsStayDistinctAndResolveTaxRelationships(t *testing.T) {
	s := coverageStore(t)
	tax := `<SalesTax accountId="U1" transactionID="tax1" taxableTransactionID="cash1" salesTax="-0.2300" currency="USD" date="2026-10-20"/>`
	acquireRealized(t, s, movementReport("SalesTaxes", tax))

	rows, _ := s.queries.Executions(t.Context())
	taxID := rows[0].ID
	if realizedActivity(t, s, taxID).Activity.Relationships[0].TargetID != nil {
		t.Fatal("invented missing tax target")
	}

	withholding := strings.NewReplacer(`transactionID="cash1"`, `transactionID="cash2"`, `type="Dividends"`, `type="Withholding Tax"`, `amount="12.34000000000000001"`, `amount="-1.2300"`).
		Replace(cashRow)
	zero := strings.NewReplacer(`transactionID="cash1"`, `transactionID="cash3"`, `amount="12.34000000000000001"`, `amount="0.0000"`).
		Replace(cashRow)
	acquireRealized(t, s, movementReport("CashTransactions", cashRow+withholding+zero))

	rows, _ = s.queries.Executions(t.Context())
	if len(rows) != 4 {
		t.Fatal("shared action collapsed cash entries", len(rows))
	}

	taxValue := realizedActivity(t, s, taxID)
	if taxValue.Activity.Relationships[0].TargetID == nil {
		t.Fatal("tax link did not resolve")
	}

	id := *taxValue.Activity.Relationships[0].TargetID
	v := realizedActivity(t, s, id)
	if v.Activity.Cash.Amount.Amount != "12.34000000000000001" || v.Activity.InstrumentID != nil ||
		v.Activity.At != nil || v.Activity.Cash.AvailableForTradingDate == nil || *v.Activity.Cash.AvailableForTradingDate != "2026-10-23" {
		t.Fatal(v)
	}

	retainDecision(t, s, "activity", id, "cash_amount", "13.0000", "USD", "set")

	if realizedActivity(t, s, id).Activity.Cash.Amount.Amount != "13.0000" {
		t.Fatal("cash correction failed")
	}

	retainDecision(t, s, "activity", id, "cash_amount", "", "", "release")

	if realizedActivity(t, s, id).Activity.Cash.Amount.Amount != "12.34000000000000001" {
		t.Fatal("cash release failed")
	}

	query, _ := application.NewListQuery(
		application.ListOptions{Kind: "cash", Sort: "amount", Direction: "asc"},
		application.ActivityScope,
	)

	list, err := s.ActivityList(t.Context(), query)
	if err != nil || len(list.Items) != 4 || list.Items[0].Resource.Activity.Cash.Amount.Amount != "-1.2300" ||
		list.Items[3].Resource.Activity.Cash.Amount.Amount != "12.34000000000000001" {
		t.Fatal(list, err)
	}

	query, _ = application.NewListQuery(
		application.ListOptions{Search: "Synthetic dividend"},
		application.ActivityScope,
	)

	list, err = s.ActivityList(t.Context(), query)
	if err != nil || len(list.Items) != 3 {
		t.Fatal("cash description search", list, err)
	}

	euro := strings.NewReplacer(`transactionID="cash1"`, `transactionID="cash-eur"`, `currency="USD"`, `currency="EUR"`, `amount="12.34000000000000001"`, `amount="1000.0000"`).
		Replace(cashRow)
	acquireRealized(t, s, movementReport("CashTransactions", euro))

	query, _ = application.NewListQuery(
		application.ListOptions{Kind: "cash", Sort: "amount", Direction: "asc"},
		application.ActivityScope,
	)

	list, err = s.ActivityList(t.Context(), query)
	if err != nil || len(list.Items) != 5 || list.Items[0].Resource.Activity.Cash.Amount.Currency != "EUR" ||
		list.Items[1].Resource.Activity.Cash.Amount.Amount != "-1.2300" {
		t.Fatal("cash ordering compared unrelated currencies", list, err)
	}
}

func TestMovementInterpretationUpgradeRetriesAtomicallyFromRetainedReports(t *testing.T) {
	s := coverageStore(t)
	coverageReport(t, s, "existing", equityRow("U1", "fill", "100"))
	rows, _ := s.queries.Executions(t.Context())
	existing := rows[0].ID
	retainDecision(t, s, "activity", existing, "price", "99.0000", "USD", "set")

	body := strings.Replace(
		movementReport("Transfers", transferRow),
		"</FlexStatement>",
		"<CashTransactions>"+cashRow+"</CashTransactions></FlexStatement>",
		1,
	)
	artifact, _ := provider.NewArtifact(ibkrflex.ActivityFormat, "application/xml", time.Now(), []byte(body))
	key := host.WorkKey{Connection: "synthetic", Stream: ibkrflex.Activity, Operation: "acquire"}
	cp, _ := s.Checkpoint(t.Context(), key)
	step, _ := provider.NewStep(provider.State{}, artifact, time.Now().Add(time.Hour))

	id, err := s.Accept(t.Context(), key, cp.Revision, step, nil, provider.Progress{})
	if err != nil {
		t.Fatal(err)
	}

	old := claimNormalizer(func(context.Context, provider.Artifact, provider.Claims) (provider.Result, error) {
		return provider.NewResult(provider.Counts{Rows: 2, Ignored: 2})
	})
	if _, err = s.Normalize(t.Context(), id, "old-movements", old); err != nil {
		t.Fatal(err)
	}

	if err = s.Process(t.Context(), id, "old-movements"); err != nil {
		t.Fatal(err)
	}

	before, _ := snapshotState(t.Context(), s)
	pending, err := s.PendingArtifacts(
		t.Context(),
		time.Now(),
		map[string]string{"ibkr-flex": ibkrflex.Version},
	)
	if err != nil || len(pending) != 1 || pending[0].ID != id {
		t.Fatal(pending, err)
	}

	reg, _ := ibkrflex.Registration(ibkrflex.Options{})
	if _, err = s.Normalize(t.Context(), id, ibkrflex.Version, reg.Normalizer()); err != nil {
		t.Fatal(err)
	}

	if _, err = s.db.Exec(
		`CREATE TRIGGER fail_cash_upgrade BEFORE INSERT ON executions WHEN NEW.kind='cash' BEGIN SELECT RAISE(ABORT,'synthetic failure'); END`,
	); err != nil {
		t.Fatal(err)
	}

	if err = s.Process(t.Context(), id, ibkrflex.Version); err == nil {
		t.Fatal("upgrade failure acknowledged")
	}

	after, _ := snapshotState(t.Context(), s)
	if after.Version != before.Version || tableCount(t, s, "executions") != 1 {
		t.Fatal("partial upgrade escaped transaction")
	}

	if _, err = s.db.Exec(`DROP TRIGGER fail_cash_upgrade`); err != nil {
		t.Fatal(err)
	}

	if err = s.Process(t.Context(), id, ibkrflex.Version); err != nil {
		t.Fatal(err)
	}

	if tableCount(t, s, "executions") != 3 ||
		realizedActivity(t, s, existing).Activity.Price.Amount != "99.0000" {
		t.Fatal("upgrade lost identities or decisions")
	}

	after, _ = snapshotState(t.Context(), s)
	if after.Version == before.Version {
		t.Fatal("upgrade did not publish")
	}

	if err = s.Process(t.Context(), id, ibkrflex.Version); err != nil {
		t.Fatal(err)
	}

	again, _ := snapshotState(t.Context(), s)
	if again.Version != after.Version {
		t.Fatal("repeat upgrade republished")
	}

	pending, err = s.PendingArtifacts(t.Context(), time.Now(), map[string]string{"ibkr-flex": ibkrflex.Version})
	if err != nil || len(pending) != 0 {
		t.Fatal("completed update requeued", pending, err)
	}
}

func TestSecurityTransfersReconcileCorrectAndPublish(t *testing.T) {
	s := coverageStore(t)
	lot := strings.ReplaceAll(
		strings.ReplaceAll(transferRow, "<Transfer ", "<TransferLot "),
		`levelOfDetail="TRANSFER"`,
		`levelOfDetail="LOT"`,
	)
	acquireRealized(t, s, movementReport("Transfers", transferRow+lot))

	rows, err := s.queries.Executions(t.Context())
	if err != nil || len(rows) != 1 {
		t.Fatal(rows, err)
	}

	id := rows[0].ID

	v := realizedActivity(t, s, id)
	if v.Activity.Family != "security-transfer" || v.Activity.Quantity.Value != "12.0000000000001" ||
		v.Activity.Transfer.ReportedCost.Amount != "999.0000" ||
		v.Activity.Price != nil || v.Activity.Transfer.DeliveringBroker == nil || *v.Activity.Transfer.DeliveringBroker != "Synthetic delivering broker" {
		t.Fatal(v)
	}

	before, _ := snapshotState(t.Context(), s)
	acquireRealized(t, s, movementReport("Transfers", transferRow+lot))

	after, _ := snapshotState(t.Context(), s)
	if before.Version != after.Version {
		t.Fatal("repeat changed publication")
	}

	retainDecision(t, s, "activity", id, "direction", "out", "", "set")

	if realizedActivity(t, s, id).Activity.Transfer.Direction != "out" {
		t.Fatal("direction correction missing")
	}

	retainDecision(t, s, "activity", id, "direction", "", "", "release")

	if realizedActivity(t, s, id).Activity.Transfer.Direction != "in" {
		t.Fatal("direction release missing")
	}

	changed := strings.ReplaceAll(transferRow, `quantity="12.0000000000001"`, `quantity="13"`)
	acquireRealized(t, s, movementReport("Transfers", changed))

	v = realizedActivity(t, s, id)
	if !v.Activity.Conflict || v.Activity.Quantity.Value != "12.0000000000001" {
		t.Fatal("conflict replaced baseline", v)
	}

	q, _ := application.NewListQuery(
		application.ListOptions{Kind: "security-transfer"},
		application.ActivityScope,
	)

	list, err := s.ActivityList(t.Context(), q)
	if err != nil || len(list.Items) != 1 {
		t.Fatal(list, err)
	}
}
