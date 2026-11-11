// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"crypto/rand"
	"fmt"
	"strings"
	"testing"
	"time"

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/host"
	"go.trippwill.dev/wake/internal/persistence/sqlite/sqlc"
	"go.trippwill.dev/wake/provider"
	"go.trippwill.dev/wake/providers/ibkrflex"
)

func realizedReport(event, amount, cost string) string {
	return `<FlexQueryResponse type="AF"><FlexStatements><FlexStatement accountId="U1" fromDate="2026-10-20" toDate="2026-10-21"><Trades>` + fmt.Sprintf(
		`<Trade accountId="U1" assetCategory="STK" conid="100" tradeID="T%s" ibExecID="%s" transactionType="ExchTrade" levelOfDetail="EXECUTION" quantity="0.000000000123" currency="USD" tradeDate="2026-10-20" reportDate="2026-10-21" fifoPnlRealized="%s" cost="%s"/>`,
		event,
		event,
		amount,
		cost,
	) + `</Trades><FIFOPerformanceSummaryInBase totalRealizedPnl="999999"/></FlexStatement></FlexStatements></FlexQueryResponse>`
}

func retainRealizedReport(t *testing.T, s *Store, connection, body string) string {
	t.Helper()
	ctx := t.Context()

	artifact, err := provider.NewArtifact(ibkrflex.ActivityFormat, "application/xml", time.Now(), []byte(body))
	if err != nil {
		t.Fatal(err)
	}

	key := host.WorkKey{Connection: connection, Stream: ibkrflex.Activity, Operation: "acquire"}

	checkpoint, err := s.Checkpoint(ctx, key)
	if err != nil {
		t.Fatal(err)
	}

	step, _ := provider.NewStep(provider.State{}, artifact, time.Now().Add(time.Hour))

	id, err := s.Accept(ctx, key, checkpoint.Revision, step, nil, provider.Progress{}, host.PollCycle{})
	if err != nil {
		t.Fatal(err)
	}

	reg, err := ibkrflex.Registration(ibkrflex.Options{})
	if err != nil {
		t.Fatal(err)
	}

	if _, err = s.Normalize(ctx, id, ibkrflex.Version, reg.Normalizer()); err != nil {
		t.Fatal(err)
	}

	return id
}

func acquireRealized(t *testing.T, s *Store, body string) string {
	t.Helper()

	id := retainRealizedReport(t, s, "synthetic", body)
	if err := s.Process(t.Context(), id, ibkrflex.Version); err != nil {
		t.Fatal(err)
	}

	return id
}

func realizedActivity(t *testing.T, s *Store, id string) application.CanonicalResource {
	t.Helper()

	value, err := s.Resource(t.Context(), "activity", id)
	if err != nil {
		t.Fatal(err)
	}

	return value
}

func TestRealizedReportsEnrichActivityWithoutChangingTradesOrRepeatingPublication(t *testing.T) {
	s := coverageStore(t)
	coverageReport(t, s, "confirmation", equityRow("U1", "fill", "100"))

	rows, err := s.queries.Executions(t.Context())
	if err != nil || len(rows) != 1 {
		t.Fatal(rows, err)
	}

	id := rows[0].ID
	before, _ := snapshotState(t.Context(), s)
	body := realizedReport("fill", "123.45000000000000001", "-100.0000")

	first := acquireRealized(t, s, body)
	value := realizedActivity(t, s, id)
	pnl := value.Activity.ReportedRealizedPnL
	if pnl == nil || pnl.Amount == nil || pnl.Amount.Amount != "123.45000000000000001" ||
		pnl.CostBasis.Amount != "-100.0000" ||
		pnl.Amount.Currency != "USD" ||
		pnl.Conflict ||
		value.Activity.Trade.Price.Amount != "100" {
		t.Fatal(value)
	}

	after, _ := snapshotState(t.Context(), s)
	if after.Version == before.Version || tableCount(t, s, "executions") != 1 {
		t.Fatal("result duplicated activity or was not published")
	}

	if repeated := acquireRealized(t, s, body); repeated != first {
		t.Fatal("exact result body duplicated")
	}

	acquireRealized(t, s, strings.Replace(body, `fromDate="2026-10-20"`, `fromDate="2026-10-19"`, 1))

	repeated, _ := snapshotState(t.Context(), s)
	if repeated.Version != after.Version {
		t.Fatal("known claims advanced publication")
	}

	retainDecision(t, s, "activity", id, "price", "200.0000", "USD", "set")
	corrected, _ := snapshotState(t.Context(), s)
	acquireRealized(t, s, body)

	again, _ := snapshotState(t.Context(), s)
	value = realizedActivity(t, s, id)
	if again.Version != corrected.Version || value.Activity.Trade.Price.Amount != "200.0000" ||
		value.Activity.ReportedRealizedPnL.Amount.Amount != pnl.Amount.Amount {
		t.Fatal("trade correction changed source result", value)
	}
}

func TestRealizedRestatementsUseSharedDecisionPreflightSaveAndRelease(t *testing.T) {
	s := coverageStore(t)
	coverageReport(t, s, "confirmation", equityRow("U1", "fill", "100"))
	rows, _ := s.queries.Executions(t.Context())
	id := rows[0].ID

	acquireRealized(t, s, realizedReport("fill", "10.0000", "-90.0000"))
	acquireRealized(t, s, realizedReport("fill", "11.0000", "-89.0000"))

	value := realizedActivity(t, s, id)
	if !value.Activity.Conflict || !value.Activity.ReportedRealizedPnL.Conflict ||
		value.Activity.ReportedRealizedPnL.Amount != nil {
		t.Fatal("arrival order selected a restatement", value)
	}

	form, err := s.CorrectionForm(t.Context(), "activity", id)
	if err != nil {
		t.Fatal(err)
	}

	choice := ""
	for _, candidate := range form.Candidates {
		if candidate.Scope == "observation" && candidate.Fields["realized_pnl"].Value == "11.0000" {
			choice = candidate.Value
		}
	}

	if choice == "" {
		t.Fatal("restatement unavailable for selection")
	}

	decision, err := application.NewDecision(
		application.DecisionInput{
			TargetKind:       "activity",
			TargetID:         id,
			Scope:            "observation",
			Text:             choice,
			Action:           "set",
			ExpectedRevision: form.Revision,
			Submission:       rand.Text(),
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	before, _ := snapshotState(t.Context(), s)
	if err = s.CheckCorrection(t.Context(), decision); err != nil {
		t.Fatal(err)
	}

	checked, _ := snapshotState(t.Context(), s)
	if checked.Version != before.Version || tableCount(t, s, "operator_decisions") != 0 {
		t.Fatal("preflight wrote state")
	}

	if err = s.SaveDecision(t.Context(), decision, "synthetic", time.Now()); err != nil {
		t.Fatal(err)
	}

	value = realizedActivity(t, s, id)
	if value.Activity.ReportedRealizedPnL.Amount.Amount != "11.0000" ||
		value.Activity.ReportedRealizedPnL.Selection != "operator" ||
		!value.Activity.ReportedRealizedPnL.Conflict {
		t.Fatal(value)
	}

	retainDecision(t, s, "activity", id, "observation", "", "", "release")

	value = realizedActivity(t, s, id)
	if value.Activity.ReportedRealizedPnL.Amount != nil ||
		value.Activity.ReportedRealizedPnL.Selection != "automatic" {
		t.Fatal("release lost baseline", value)
	}
}

func TestRealizedResultsWaitForQualifiedActivityAndKeepAmbiguousLinksVisible(t *testing.T) {
	s := coverageStore(t)
	acquireRealized(t, s, realizedReport("fill", "0.0000", "-100.0000"))

	if tableCount(t, s, "executions") != 0 {
		t.Fatal("Activity created a trade")
	}

	query, _ := application.NewListQuery(application.ListOptions{}, application.ReviewScope)

	review, err := s.ReviewList(t.Context(), query)
	if err != nil || review.Total != 1 {
		t.Fatal("unmatched result not reviewable", review, err)
	}

	coverageReport(t, s, "late-confirmation", equityRow("U1", "fill", "100"))
	rows, _ := s.queries.Executions(t.Context())
	id := rows[0].ID

	value := realizedActivity(t, s, id)
	if value.Activity.ReportedRealizedPnL == nil ||
		value.Activity.ReportedRealizedPnL.Amount.Amount != "0.0000" {
		t.Fatal("result did not link after confirmation", value)
	}

	other := strings.Replace(equityRow("U1", "other", "100"), `tradeID="Tother"`, `tradeID="Tfill"`, 1)
	coverageReport(t, s, "ambiguous-alias", other)

	value = realizedActivity(t, s, id)
	if value.Activity.ReportedRealizedPnL.Conflict || tableCount(t, s, "executions") != 2 {
		t.Fatal("shared alias overrode the distinct execution reference", value)
	}
}

func TestRealizedAuthorityAndUnknownAmountAreIndependentOfAcquisition(t *testing.T) {
	s := coverageStore(t)
	coverageReport(t, s, "confirmation", equityRow("U1", "fill", "100"))
	rows, _ := s.queries.Executions(t.Context())
	id := rows[0].ID

	if _, err := s.db.Exec(`INSERT INTO connections(id,provider) VALUES('other','ibkr-flex')`); err != nil {
		t.Fatal(err)
	}

	foreign := retainRealizedReport(t, s, "other", realizedReport("fill", "9", "-91"))
	if err := s.Process(t.Context(), foreign, ibkrflex.Version); err != nil {
		t.Fatal(err)
	}

	if realizedActivity(t, s, id).Activity.ReportedRealizedPnL != nil {
		t.Fatal("unconfirmed source contributed P&L")
	}

	acquireRealized(t, s, realizedReport("fill", "", "-100.0000"))

	value := realizedActivity(t, s, id).Activity.ReportedRealizedPnL
	if value == nil || value.Amount != nil || value.CostBasis.Amount != "-100.0000" {
		t.Fatal("unknown became zero", value)
	}

	acquireRealized(
		t,
		s,
		strings.ReplaceAll(realizedReport("fill", "8", "-92"), `accountId="U1"`, `accountId="U2"`),
	)

	if realizedActivity(t, s, id).Activity.ReportedRealizedPnL.Amount != nil {
		t.Fatal("other account contributed")
	}
}

func TestRealizedPublicationFailureRollsBackAndRestartPreservesSelections(t *testing.T) {
	s, path := testStore(t)

	_, err := s.db.Exec(`INSERT INTO connections(id,provider) VALUES('synthetic','ibkr-flex');
INSERT INTO artifacts(id,connection_id,stream,operation,format,media_type,retrieved,hash,content) VALUES('discovery','synthetic','confirmations','discover','synthetic','application/xml','2026-10-20T00:00:00Z','synthetic',X'00');
INSERT INTO accounts(id,name,currency,connection_id,provider,authority,kind,scope,value,artifact_id) VALUES('account','Synthetic account','','synthetic','ibkr-flex','ibkr','account','','U1','discovery');`)
	if err != nil {
		t.Fatal(err)
	}

	coverageReport(t, s, "confirmation", equityRow("U1", "fill", "100"))
	rows, _ := s.queries.Executions(t.Context())
	id := rows[0].ID

	acquireRealized(t, s, realizedReport("fill", "10.0000", "-90.0000"))

	before, _ := snapshotState(t.Context(), s)
	artifact := retainRealizedReport(t, s, "synthetic", realizedReport("fill", "11.0000", "-89.0000"))
	if _, err = s.db.Exec(
		`CREATE TRIGGER fail_realized_publication BEFORE UPDATE ON published_resources WHEN NEW.kind='activity' BEGIN SELECT RAISE(ABORT,'synthetic failure'); END`,
	); err != nil {
		t.Fatal(err)
	}

	if err = s.Process(t.Context(), artifact, ibkrflex.Version); err == nil {
		t.Fatal("publication failure ignored")
	}

	rolledBack, _ := snapshotState(t.Context(), s)
	complete, err := s.queries.ProcessingComplete(
		t.Context(),
		sqlc.ProcessingCompleteParams{ArtifactID: artifact, Version: ibkrflex.Version},
	)
	if err != nil || complete || rolledBack.Version != before.Version ||
		realizedActivity(t, s, id).Activity.ReportedRealizedPnL.Conflict {
		t.Fatal("partial result escaped failed publication", err)
	}

	if _, err = s.db.Exec(`DROP TRIGGER fail_realized_publication`); err != nil {
		t.Fatal(err)
	}

	if err = s.Process(t.Context(), artifact, ibkrflex.Version); err != nil {
		t.Fatal(err)
	}

	form, err := s.CorrectionForm(t.Context(), "activity", id)
	if err != nil {
		t.Fatal(err)
	}

	choice := ""
	for _, candidate := range form.Candidates {
		if candidate.Scope == "observation" && candidate.Fields["realized_pnl"].Value == "11.0000" {
			choice = candidate.Value
		}
	}

	retainDecision(t, s, "activity", id, "observation", choice, "", "set")

	published, _ := snapshotState(t.Context(), s)

	if err = s.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(t.Context(), testProfile(path), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()

	after, _ := snapshotState(t.Context(), reopened)
	value := realizedActivity(t, reopened, id).Activity.ReportedRealizedPnL
	if after.Version != published.Version || value.Amount.Amount != "11.0000" || value.Selection != "operator" {
		t.Fatal("restart lost result selection", value)
	}

	if err = reopened.Process(t.Context(), artifact, ibkrflex.Version); err != nil {
		t.Fatal(err)
	}

	again, _ := snapshotState(t.Context(), reopened)
	if again.Version != after.Version {
		t.Fatal("replay changed accepted result")
	}
}

func TestRealizedListSortsExactValuesByCurrencyWithUnknownLast(t *testing.T) {
	s := coverageStore(t)
	coverageReport(
		t,
		s,
		"confirmations",
		equityRow(
			"U1",
			"higher",
			"100",
		)+equityRow(
			"U1",
			"lower",
			"100",
		)+equityRow(
			"U1",
			"unknown",
			"100",
		)+strings.ReplaceAll(
			equityRow("U1", "gbp", "100"),
			`currency="USD"`,
			`currency="GBP"`,
		),
	)
	acquireRealized(t, s, realizedReport("higher", "9007199254740993.0000000000002", "-1"))
	acquireRealized(t, s, realizedReport("lower", "9007199254740993.0000000000001", "-1"))
	acquireRealized(
		t,
		s,
		strings.ReplaceAll(realizedReport("gbp", "-2.0000", "-1"), `currency="USD"`, `currency="GBP"`),
	)

	for _, direction := range []string{"asc", "desc"} {
		query, err := application.NewListQuery(
			application.ListOptions{Sort: "realized", Direction: direction},
			application.ActivityScope,
		)
		if err != nil {
			t.Fatal(err)
		}

		list, err := s.ActivityList(t.Context(), query)
		if err != nil || len(list.Items) != 4 {
			t.Fatal(list, err)
		}

		if list.Items[0].Resource.Activity.ReportedRealizedPnL.Amount.Currency != "GBP" ||
			list.Items[3].Resource.Activity.ReportedRealizedPnL != nil {
			t.Fatal("currency grouping or unknown order lost", list)
		}

		expected := "9007199254740993.0000000000001"
		if direction == "desc" {
			expected = "9007199254740993.0000000000002"
		}

		if list.Items[1].Resource.Activity.ReportedRealizedPnL.Amount.Amount != expected {
			t.Fatal("P&L ordered using rounded values", list)
		}
	}

	query, _ := application.NewListQuery(
		application.ListOptions{Search: "9007199254740993.0000000000002"},
		application.ActivityScope,
	)

	list, err := s.ActivityList(t.Context(), query)
	if err != nil || list.Matched != 1 {
		t.Fatal("reported P&L not searchable", list, err)
	}

	query, _ = application.NewListQuery(
		application.ListOptions{
			Search:         "unknown",
			DisplayMatches: application.DisplayMatches{MissingValues: true},
			PageSize:       1,
		},
		application.ActivityScope,
	)

	list, err = s.ActivityList(t.Context(), query)
	if err != nil || list.Matched != 1 || len(list.Items) != 1 ||
		list.Items[0].Resource.Activity.ReportedRealizedPnL != nil {
		t.Fatal("unreported P&L missing from unknown search", list, err)
	}
}
