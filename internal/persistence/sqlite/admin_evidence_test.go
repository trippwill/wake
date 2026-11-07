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
	"go.trippwill.dev/wake/providers/ibkrflex"
)

func TestReportObservationPagesPreserveVersionsLinksAndOrdering(t *testing.T) {
	s := coverageStore(t)

	var rows strings.Builder
	for i := range 55 {
		rows.WriteString(equityRow("U1", fmt.Sprintf("report-fill-%02d", i), "100"))
	}

	rows.WriteString(equityRow("U1", "report-fill-00", "100"))
	coverageReport(t, s, "report", rows.String())

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

		if row.Outcome == application.ObservationNoContribution {
			noContribution++
		}
	}

	if unlinked != 2 {
		t.Fatal("financial links or unselected appearances lost", unlinked)
	}

	if noContribution != 57 {
		t.Fatal("repeated appearances claimed to add information", noContribution)
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
		ObservationMatches: []application.ObservationOutcome{application.ObservationNoContribution},
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

func TestReportObservationPagesIncludeUnlinkedAndEmptyReports(t *testing.T) {
	s := coverageStore(t)
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

	empty := acquireRealized(t, s, movementReport("CashTransactions", ""))
	result, err = s.ObservationList(t.Context(), "artifact", empty, query)
	if err != nil || result.Total != 0 || len(result.Items) != 0 {
		t.Fatal("empty report was not readable", result, err)
	}
}

func TestWarehouseSizeReadsOwnedFileWithoutPublication(t *testing.T) {
	s := coverageStore(t)
	initial, err := s.WarehouseSize()
	if err != nil || initial <= 0 {
		t.Fatal(initial, err)
	}

	coverageReport(
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

	if _, err = (&Store{owners: [2]*os.File{closed}}).WarehouseSize(); !errors.Is(err, os.ErrClosed) {
		t.Fatal("closed warehouse reported a size", err)
	}
}
