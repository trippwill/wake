// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/persistence/sqlite/sqlc"
	"go.trippwill.dev/wake/provider"
	"go.trippwill.dev/wake/providers/ibkrflex"
)

func TestReportListDistinguishesReceiptsAndContributions(t *testing.T) {
	s := newStoreWithSyntheticAccount(t)

	reg, err := ibkrflex.Registration(ibkrflex.Options{})
	if err != nil {
		t.Fatal(err)
	}

	s.registrations = []provider.Registration{reg}
	processConfirmationReport(t, s, "original", equityRow("U1", "report-fixture", "123.45"))
	processConfirmationReport(t, s, "repeated-claim", equityRow("U1", "report-fixture", "123.45"))

	if _, err = s.db.ExecContext(t.Context(), `
UPDATE connections SET name='First source' WHERE id='synthetic';
UPDATE positions_current
SET data=CAST('{"status":"available","as_of":{"date":"2026-10-19","at":null,"basis":"broker_effective"},"positions":[]}' AS BLOB)
WHERE account_id='account';
UPDATE acquisition_receipts SET retrieved='2026-10-20T00:00:00Z',reported_from='2026-10-18',reported_to='2026-10-19' WHERE id='original';
UPDATE acquisition_receipts SET retrieved='2026-10-20T00:00:01Z' WHERE id='repeated-claim';
INSERT INTO connections(id,name,provider) VALUES('second','Second source','ibkr-flex');
INSERT INTO artifacts(id,connection_id,stream,operation,format,media_type,retrieved,hash,content)
VALUES('pending','second','activities','supply','ibkr-test','text/plain','2026-10-19T23:00:01-01:00','fixture',X'00');
INSERT INTO acquisition_receipts(id,artifact_id,operation,retrieved,hash,policy,digest,requested_from,requested_to,reported_from,reported_to) VALUES
 ('pending','pending','supply','2026-10-19T23:00:01-01:00','fixture','exact-bytes/1','fixture','','','',''),
 ('same-body','original','acquire','2026-10-20T00:00:01.000000001Z','fixture','synthetic/1','fixture','2026-10-01','2026-10-20','2026-10-16','2026-10-20'),
 ('ignored','discovery','discover','2026-10-20T00:01:00Z','fixture','exact-bytes/1','fixture','','','','');
`); err != nil {
		t.Fatal(err)
	}

	read := func(options application.ListOptions) application.ReportList {
		t.Helper()

		query, e := application.NewListQuery(options, application.ReportsScope)
		if e != nil {
			t.Fatal(e)
		}

		result, e := s.ReportList(t.Context(), query)
		if e != nil {
			t.Fatal(e)
		}

		return result
	}

	page := read(application.ListOptions{PageSize: 2})
	if page.Total != 4 || page.Matched != 4 || len(page.Items) != 2 {
		t.Fatal("unexpected report page", page)
	}

	first, second := page.Items[0], page.Items[1]
	if first.Receipt != "same-body" || !first.Received.After(second.Received) || !first.Complete ||
		!first.ReusedBody ||
		first.Accepted != 0 ||
		first.RequestedFrom != "2026-10-01" ||
		first.ReportedFrom != "2026-10-16" {
		t.Fatal("repeat receipt lost its own timing or acquired a contribution", first)
	}

	if second.Receipt != "pending" || second.ConnectionName != "Second source" || second.Complete ||
		!second.CanNormalize || second.ReusedBody || second.Accepted != 0 || second.Operation != "supply" {
		t.Fatal("pending report lost its independent identity", second)
	}

	page = read(application.ListOptions{Page: 2, PageSize: 2})
	if len(page.Items) != 2 {
		t.Fatal("unexpected second report page", page)
	}

	first, second = page.Items[0], page.Items[1]
	if first.Receipt != "repeated-claim" || first.ReusedBody || first.Repeated != 1 || first.Accepted != 0 ||
		second.Receipt != "original" || second.ReusedBody || second.Accepted != 1 || second.ReportedTo != "2026-10-19" {
		t.Fatal("new and repeated activity outcomes collapsed", first, second)
	}

	page = read(application.ListOptions{PageSize: 1, Search: "SECOND SOURCE"})
	if page.Total != 4 || page.Matched != 1 || len(page.Items) != 1 || page.Items[0].Receipt != "pending" {
		t.Fatal("search did not scope the complete report list before paging", page)
	}

	for _, tc := range []struct {
		label   string
		matches int64
	}{{"Trade confirmations", 3}, {"ACTIVITY REPORT", 1}} {
		result := read(application.ListOptions{Search: tc.label})
		if result.Matched != tc.matches || int64(len(result.Items)) != tc.matches {
			t.Fatalf("search by displayed report name %q: %+v", tc.label, result)
		}
	}

	overview, err := s.Overview(t.Context())
	if err != nil || len(overview.Reports) != 4 || overview.Reports[0].Receipt != "same-body" {
		t.Fatal("overview reports do not share global receipt ordering", overview.Reports, err)
	}

	if overview.PositionsAvailable != 1 || overview.PositionsAsOf == nil ||
		overview.PositionsAsOf.Date != "2026-10-19" {
		t.Fatal(
			"overview lost the published position set's as-of date",
			overview.PositionsAvailable,
			overview.PositionsAsOf,
		)
	}

	if pending := overview.PendingReport; pending.Artifact != "pending" ||
		pending.ConnectionName != "Second source" ||
		pending.AccountConfirmed {
		t.Fatal("overview masked pending processing or missing authority", pending)
	}

	for i := range 53 {
		if err = s.queries.PutAcquisitionReceipt(t.Context(), sqlc.PutAcquisitionReceiptParams{
			ID: fmt.Sprintf("later-%02d", i), ArtifactID: "original", Operation: "acquire",
			Retrieved: "2026-10-30T00:00:00Z", Hash: "fixture", Policy: "synthetic/1", Digest: "fixture",
		}); err != nil {
			t.Fatal(err)
		}
	}

	if err = s.ProcessingFailure(t.Context(), "pending", time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	overview, err = s.Overview(t.Context())
	if err != nil || overview.PendingReport.Artifact != "pending" || !overview.PendingReport.ProcessingFailed {
		t.Fatal(
			"Overview lost the processing failure for a report outside its latest receipts",
			overview.PendingReport,
			err,
		)
	}

	evidence, err := s.EvidenceContext(t.Context(), "original")
	if err != nil || evidence.ReceiptCount != 55 || len(evidence.Receipts) != 50 {
		t.Fatal("receipt history was not bounded", evidence.ReceiptCount, len(evidence.Receipts), err)
	}

	selected, err := s.SelectedReceipt(t.Context(), "original", "same-body")
	if err != nil || selected.ID != "same-body" || selected.Retrieved != "2026-10-20T00:00:01.000000001Z" ||
		selected.ReportedFrom != "2026-10-16" || selected.RequestedFrom != "2026-10-01" || !selected.Equivalent {
		t.Fatal("older selected receipt lost its timing, bounds or identity", selected, err)
	}

	if _, err = s.SelectedReceipt(
		t.Context(),
		"pending",
		"same-body",
	); !errors.Is(
		err,
		application.ErrNotFound,
	) {
		t.Fatal("receipt was accepted for a different retained report", err)
	}
}
