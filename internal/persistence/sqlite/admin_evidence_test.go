// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/application/calculation"
	"go.trippwill.dev/wake/providers/ibkrflex"
	"go.trippwill.dev/wake/source"
)

func TestReportObservationAndTradeCarryProviderForBillWithoutContractID(t *testing.T) {
	const artifact = "bill-without-contract-id"

	s := newStoreWithSyntheticAccount(t)

	processConfirmationReport(
		t,
		s,
		artifact,
		`<TradeConfirm accountId="U1" assetCategory="BILL" issuerCountryCode="US" conid="0" isin="US912797UJ40" symbol="Synthetic bill" execID="bill-fill" transactionType="ExchTrade" levelOfDetail="EXECUTION" buySell="BUY" tradeDate="2026-10-20" quantity="10000" price="98.7654321" amount="9876.54" currency="USD"/>`,
	)

	rows := observationRowsForTest(t, s, "artifact", artifact)
	if len(rows) != 2 || rows[0].ActivityID == "" || rows[0].SourceProvider != "ibkr-flex" {
		t.Fatal("report lookup did not expose the linked bill and retained provider", rows)
	}

	instrument, ok := rows[0].Reported.Field(source.FieldInstrument)
	if !ok || !instrument.Fields().Instrument.Reference().IsZero() ||
		instrument.Fields().Instrument.Debt().Fields().Type != source.TreasuryBill {
		t.Fatal("fixture should retain an IBKR bill without an instrument reference", instrument)
	}

	retained, err := s.Retained(t.Context(), artifact)
	if err != nil || retained.Provider != "ibkr-flex" {
		t.Fatal("retained artifact lost its provider", retained.Provider, err)
	}

	detail, err := s.ExecutionDetail(t.Context(), rows[0].ActivityID)
	if err != nil || detail.Context.AccountProvider != "ibkr-flex" ||
		!detail.Execution.Terms().Fields().Instrument.Reference().IsZero() ||
		detail.Execution.Terms().Fields().Price.Amount() != "98.7654321" {
		t.Fatal("accepted detail did not retain the account provider and exact bill price", detail, err)
	}
}

func TestReportObservationPagesPreserveVersionsLinksAndOrdering(t *testing.T) {
	s := newStoreWithSyntheticAccount(t)

	var rows strings.Builder
	for i := range 55 {
		rows.WriteString(equityRow("U1", fmt.Sprintf("report-fill-%02d", i), "100"))
	}

	rows.WriteString(equityRow("U1", "report-fill-00", "100"))
	processConfirmationReport(t, s, "report", rows.String())

	reg, _ := ibkrflex.Registration(ibkrflex.Options{})
	if _, err := s.Normalize(t.Context(), "report", "second-version", reg.Normalizer()); err != nil {
		t.Fatal(err)
	}

	if err := s.Process(t.Context(), "report", "second-version"); err != nil {
		t.Fatal(err)
	}

	all := observationRowsForTest(t, s, "artifact", "report")
	if len(all) != 114 {
		t.Fatal("appearances or interpretation versions collapsed", len(all))
	}

	unlinked := 0
	noContribution := 0
	for _, row := range all {
		if row.Artifact != "report" {
			t.Fatal("report scoping lost", row)
		}

		if row.ActivityID == "" {
			unlinked++
		}

		if row.Outcome == calculation.ObservationNoContribution {
			noContribution++
		}
	}

	if unlinked != 2 {
		t.Fatal("financial links or unselected appearances lost", unlinked)
	}

	if noContribution != 57 {
		t.Fatal("repeated appearances claimed to add information", noContribution)
	}

	ninth := all[8]
	entry, err := s.SourceObservation(t.Context(), application.SourceLocation{
		Artifact: ninth.Artifact, Version: ninth.Version, Location: ninth.Location,
	})
	if err != nil || entry.Reported.IsZero() || !strings.HasSuffix(ninth.Location, "/TradeConfirm[9]") ||
		!strings.HasSuffix(
			entry.Previous,
			"/TradeConfirm[8]",
		) || !strings.HasSuffix(entry.Next, "/TradeConfirm[10]") ||
		entry.ActivityID != ninth.ActivityID || entry.Outcome != ninth.Outcome {
		t.Fatal("exact appearance or report order lost", entry, err)
	}

	if _, err = s.SourceObservation(t.Context(), application.SourceLocation{
		Artifact: ninth.Artifact, Version: "nonexistent", Location: ninth.Location,
	}); !errors.Is(err, application.ErrNotFound) {
		t.Fatal("an unrelated interpretation resolved to a source entry", err)
	}

	for _, direction := range []string{"asc", "desc"} {
		query, _ := application.NewListQuery(
			application.ListOptions{PageSize: 200, Sort: "activity", Direction: direction},
			application.ObservationScope,
		)

		result, err := s.ObservationList(t.Context(), "artifact", "report", query)
		if err != nil || len(result.Items) != 114 {
			t.Fatal(result, err)
		}

		for i := 1; i < len(result.Items); i++ {
			previous, current := result.Items[i-1].ActivityID, result.Items[i].ActivityID
			if direction == "asc" && previous > current || direction == "desc" && previous < current {
				t.Fatal("activity ordering ignored", direction, previous, current)
			}
		}
	}

	neutral, _ := application.NewListQuery(application.ListOptions{
		Page: 2, PageSize: 50, Search: "no additional contribution",
		ObservationMatches: []calculation.ObservationOutcome{calculation.ObservationNoContribution},
	}, application.ObservationScope)

	matched, err := s.ObservationList(t.Context(), "artifact", "report", neutral)
	if err != nil || matched.Total != 114 || matched.Matched != 57 || len(matched.Items) != 7 {
		t.Fatal("contribution filter was not applied before paging", matched, err)
	}

	query, _ := application.NewListQuery(application.ListOptions{
		PageSize:  10,
		Search:    "second-version",
		Sort:      "location",
		Direction: "asc",
	}, application.ObservationScope)

	result, err := s.ObservationList(t.Context(), "artifact", "report", query)
	if err != nil || result.Total != 114 || result.Matched != 57 || len(result.Items) != 10 ||
		result.Items[0].Location > result.Items[1].Location {
		t.Fatal("filtering or explicit ordering ignored", result, err)
	}

	query, _ = application.NewListQuery(
		application.ListOptions{Search: all[0].ActivityID},
		application.ObservationScope,
	)

	result, err = s.ObservationList(t.Context(), "artifact", "report", query)
	if err != nil || result.Matched < 2 {
		t.Fatal("linked activity was not searchable", result, err)
	}

	if _, err = s.ObservationList(
		t.Context(),
		"artifact",
		"absent",
		query,
	); !errors.Is(
		err,
		application.ErrNotFound,
	) {
		t.Fatal("missing report was not distinguished", err)
	}
}

func TestReportObservationSearchFindsDisplayedTradeTitle(t *testing.T) {
	s := newStoreWithSyntheticAccount(t)

	microsoft := strings.Replace(equityRow("U1", "target-fill", "99"), `symbol="SYN"`, `symbol="MSFT"`, 1)
	sell := strings.Replace(
		strings.Replace(microsoft, "target-fill", "sell-fill", 2),
		`buySell="BUY"`,
		`buySell="SELL"`,
		1,
	)
	processConfirmationReport(t, s, "report", equityRow("U1", "other-fill", "100")+microsoft+sell)

	query, err := application.NewListQuery(application.ListOptions{
		PageSize: 1,
		Search:   "Buy MSFT",
	}, application.ObservationScope)
	if err != nil {
		t.Fatal(err)
	}

	result, err := s.ObservationList(t.Context(), "artifact", "report", query)
	if err != nil || result.Total != 4 || result.Matched != 1 || len(result.Items) != 1 {
		t.Fatal(
			"search did not find the displayed title before pagination",
			result.PageInfo,
			len(result.Items),
			err,
		)
	}

	if id := result.Items[0].Reported.Fields().Event.Value(); id != "target-fill" {
		t.Fatal("search returned a different fill", id)
	}
}

func TestReportAppearanceSearchFindsDisplayedReportTitle(t *testing.T) {
	s := newStoreWithSyntheticAccount(t)
	row := equityRow("U1", "repeated-fill", "99")
	processConfirmationReport(t, s, "first", row)
	processConfirmationReport(t, s, "second", row)

	original := observationRowsForTest(t, s, "artifact", "first")[0]
	if original.ActivityID == "" {
		t.Fatal("first appearance has no accepted activity")
	}

	processPartial(t, s, "other-stream", original.Reported)

	query, err := application.NewListQuery(application.ListOptions{
		PageSize: 1,
		Search:   "Trade confirmations",
	}, application.ObservationScope)
	if err != nil {
		t.Fatal(err)
	}

	result, err := s.ObservationList(t.Context(), "activity-appearances", original.ActivityID, query)
	if err != nil || result.Total != 3 || result.Matched != 2 || len(result.Items) != 1 {
		t.Fatal(
			"search did not filter appearances by displayed report title",
			result.PageInfo,
			len(result.Items),
			err,
		)
	}

	if stream := result.Items[0].Stream; stream != "confirmations" {
		t.Fatal("search returned a different report stream", stream)
	}
}

func TestReportObservationPagesIncludeUnlinkedAndEmptyReports(t *testing.T) {
	s := newStoreWithSyntheticAccount(t)
	id := acquireRealized(
		t,
		s,
		movementReport(
			"CashTransactions",
			`<CashTransaction accountId="U1" transactionID="interest" tradeID="unseen" type="Bond Interest Paid" amount="-1.0000" currency="USD" dateTime="2026-10-20" levelOfDetail="DETAIL"/>`,
		),
	)
	query, _ := application.NewListQuery(application.ListOptions{}, application.ObservationScope)

	result, err := s.ObservationList(t.Context(), "artifact", id, query)
	if err != nil || result.Total != 1 || len(result.Items) != 1 || result.Items[0].ActivityID != "" {
		t.Fatal("unlinked observation lost", result, err)
	}

	unlinked := result.Items[0]
	entry, err := s.SourceObservation(t.Context(), application.SourceLocation{
		Artifact: id, Version: unlinked.Version, Location: unlinked.Location,
	})
	if err != nil || entry.ActivityID != "" || entry.Outcome != unlinked.Outcome || entry.Reported.IsZero() {
		t.Fatal("unlinked source entry was lost", entry, err)
	}

	empty := acquireRealized(t, s, movementReport("CashTransactions", ""))
	result, err = s.ObservationList(t.Context(), "artifact", empty, query)
	if err != nil || result.Total != 0 || len(result.Items) != 0 {
		t.Fatal("empty report was not readable", result, err)
	}
}

func TestWarehouseSizeReadsOwnedFileWithoutPublication(t *testing.T) {
	s := newStoreWithSyntheticAccount(t)
	initial, err := s.WarehouseSize()
	if err != nil || initial <= 0 {
		t.Fatal(initial, err)
	}

	processConfirmationReport(
		t,
		s,
		"size",
		equityRow("U1", "size-fill", "100")+"<!--"+strings.Repeat("synthetic", 8192)+"-->",
	)

	before, err := snapshotState(t.Context(), s)
	if err != nil {
		t.Fatal(err)
	}

	size, err := s.WarehouseSize()
	file, statErr := s.owners[0].Stat()
	if err != nil || statErr != nil || size <= initial || size != file.Size() {
		t.Fatal("size did not describe the current warehouse file", initial, size, err, statErr)
	}

	after, err := snapshotState(t.Context(), s)
	if err != nil || after.Version != before.Version {
		t.Fatal("metadata inspection changed publication", after, err)
	}

	if _, err = (&Store{}).WarehouseSize(); err == nil {
		t.Fatal("unavailable file reported a size")
	}

	closed, err := os.CreateTemp(t.TempDir(), "closed-warehouse")
	if err != nil {
		t.Fatal(err)
	}

	if err = closed.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err = (&Store{owners: [storeCount]*os.File{closed}}).WarehouseSize(); !errors.Is(err, os.ErrClosed) {
		t.Fatal("closed warehouse reported a size", err)
	}
}
