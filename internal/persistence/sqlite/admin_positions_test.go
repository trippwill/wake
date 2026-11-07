// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"strings"
	"testing"

	"go.trippwill.dev/wake/internal/application"
)

func positionsQuery(t *testing.T, options application.ListOptions) application.ListQuery {
	t.Helper()

	q, err := application.NewListQuery(options, application.PositionsScope)
	if err != nil {
		t.Fatal(err)
	}

	return q
}

func TestPositionsListExactOrderingPagingAndCurrentNames(t *testing.T) {
	s := coverageStore(t)
	first := holdingXML("2026-11-01", "9007199254740993.0000000000002", "0.0000")
	second := strings.ReplaceAll(
		holdingXML("2026-11-01", "9007199254740993.0000000000001", "12.0000000000001"),
		`conid="100"`,
		`conid="101"`,
	)
	third := strings.ReplaceAll(holdingXML("2026-11-01", "-1.0000000000001", ""), `conid="100"`, `conid="102"`)
	acquireRealized(
		t,
		s,
		reportedPositionsXML("2026-11-01", "<OpenPositions>"+first+second+third+"</OpenPositions>"),
	)

	options := application.ListOptions{Sort: "quantity", Direction: "asc", PageSize: 2}

	got, err := s.PositionsList(t.Context(), positionsQuery(t, options))
	if err != nil || got.Status != "available" || got.Account.ID() != "account" || got.Total != 3 ||
		got.Matched != 3 ||
		len(got.Items) != 2 ||
		got.AsOf.Date != "2026-11-01" ||
		got.AsOf.At != nil {
		t.Fatal(got, err)
	}

	if got.Items[0].Position.Side != "short" || got.Items[0].Position.CostBasis != nil ||
		got.Items[1].Position.Quantity.Value != "9007199254740993.0000000000001" {
		t.Fatal(got.Items)
	}

	options.Page = 2

	next, err := s.PositionsList(t.Context(), positionsQuery(t, options))
	if err != nil || len(next.Items) != 1 ||
		next.Items[0].Position.Quantity.Value != "9007199254740993.0000000000002" ||
		next.Items[0].Position.CostBasis.Amount != "0.0000" ||
		next.Version != got.Version {
		t.Fatal(next, err)
	}

	options = application.ListOptions{Sort: "cost", Direction: "desc"}

	costs, err := s.PositionsList(t.Context(), positionsQuery(t, options))
	if err != nil || len(costs.Items) != 3 || costs.Items[0].Position.CostBasis.Amount != "12.0000000000001" ||
		costs.Items[2].Position.CostBasis != nil {
		t.Fatal(costs, err)
	}

	id := got.Items[0].Position.InstrumentID
	if _, err = s.db.ExecContext(
		t.Context(),
		`UPDATE instruments SET name='Readable holding' WHERE id=?`,
		id,
	); err != nil {
		t.Fatal(err)
	}

	filtered, err := s.PositionsList(
		t.Context(),
		positionsQuery(t, application.ListOptions{Search: "readable", State: "short", Kind: "equity"}),
	)
	if err != nil || len(filtered.Items) != 1 || filtered.Matched != 1 || filtered.Total != 3 ||
		filtered.Items[0].Name != "Readable holding" ||
		filtered.Version != got.Version {
		t.Fatal(filtered, err)
	}

	empty, err := s.PositionsList(
		t.Context(),
		positionsQuery(t, application.ListOptions{Search: "does not exist"}),
	)
	if err != nil || len(empty.Items) != 0 || empty.Status != "available" || empty.Total != 3 ||
		empty.Matched != 0 {
		t.Fatal(empty, err)
	}

	options.Page = 20

	beyond, err := s.PositionsList(t.Context(), positionsQuery(t, options))
	if err != nil || len(beyond.Items) != 0 || beyond.Total != 3 || beyond.AsOf.Date != got.AsOf.Date {
		t.Fatal(beyond, err)
	}
}

func TestPositionsListAvailabilityAndReplacement(t *testing.T) {
	s := coverageStore(t)
	q := positionsQuery(t, application.ListOptions{})
	read := func() application.PositionsList {
		t.Helper()

		got, err := s.PositionsList(t.Context(), q)
		if err != nil {
			t.Fatal(err)
		}

		return got
	}

	coverageReport(t, s, "initial", equityRow("U1", "baseline", "100"))

	missing := read()
	if missing.Status != "unavailable" || len(missing.Items) != 0 || *missing.Reason != "not_reported" {
		t.Fatal(missing)
	}

	acquireRealized(
		t,
		s,
		reportedPositionsXML(
			"2026-11-01",
			"<OpenPositions>"+holdingXML("2026-11-01", "2", "0")+"</OpenPositions>",
		),
	)

	populated := read()

	acquireRealized(t, s, reportedPositionsXML("2026-11-02", "<OpenPositions/>"))

	empty := read()
	if empty.Status != "available" || empty.Total != 0 || len(empty.Items) != 0 ||
		empty.AsOf.Date != "2026-11-02" ||
		empty.Version == populated.Version {
		t.Fatal(empty)
	}

	acquireRealized(
		t,
		s,
		reportedPositionsXML(
			"2026-11-03",
			"<OpenPositions>"+holdingXML("2026-11-03", "3", "1")+"</OpenPositions>",
		),
	)

	replacement := read()
	if len(replacement.Items) != 1 || replacement.Items[0].Position.Quantity.Value != "3" {
		t.Fatal(replacement)
	}

	acquireRealized(
		t,
		s,
		reportedPositionsXML(
			"2026-11-04",
			"<OpenPositions>"+holdingXML("2026-11-04", "invalid", "")+"</OpenPositions>",
		),
	)

	unavailable := read()
	if unavailable.Status != "unavailable" || *unavailable.Reason != "incomplete" ||
		len(unavailable.Items) != 0 ||
		unavailable.Total != 0 ||
		unavailable.AsOf.Date != "2026-11-04" ||
		unavailable.Version == replacement.Version {
		t.Fatal(unavailable)
	}
	// Failed writes cannot leak metadata or rows into a subsequent admin read.
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}

	if _, err = tx.ExecContext(
		t.Context(),
		`UPDATE positions_current SET revision=revision+1,data=X'7B7D'`,
	); err != nil {
		t.Fatal(err)
	}

	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}

	if got := read(); got.Version != unavailable.Version || got.Status != unavailable.Status {
		t.Fatal(got)
	}
}

func TestPositionsListNoAccountAndScopeValidation(t *testing.T) {
	s, _ := testStore(t)
	got, err := s.PositionsList(t.Context(), positionsQuery(t, application.ListOptions{}))
	if err != nil || got.Account.ID() != "" || len(got.Items) != 0 {
		t.Fatal(got, err)
	}

	q, _ := application.NewListQuery(application.ListOptions{}, application.ActivityScope)
	if _, err = s.PositionsList(t.Context(), q); err != application.ErrInvalid {
		t.Fatal(err)
	}

	for _, opt := range []application.ListOptions{{Sort: "terms"}, {State: "accepted"}, {Kind: "cash"}, {Page: -1}, {PageSize: 201}} {
		if _, err = application.NewListQuery(opt, application.PositionsScope); err != application.ErrInvalid {
			t.Fatal(opt, err)
		}
	}
}
