// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/providers/ibkrflex"
)

func observationRowsForTest(t *testing.T, s *Store, kind, id string) []application.Observation {
	t.Helper()

	var rows []application.Observation
	for page := 1; ; page++ {
		query, err := application.NewListQuery(
			application.ListOptions{Page: page, PageSize: 50},
			application.ObservationScope,
		)
		if err != nil {
			t.Fatal(err)
		}

		result, err := s.ObservationList(t.Context(), kind, id, query)
		if err != nil {
			t.Fatal(err)
		}

		rows = append(rows, result.Items...)
		if int64(len(rows)) >= result.Matched {
			return rows
		}

		if len(result.Items) == 0 {
			t.Fatal("observation paging stopped before matched count")
		}
	}
}

func TestObservationPagesRetainMultiplicityAndFilterBeforePaging(t *testing.T) {
	store := coverageStore(t)
	row := equityRow("U1", "repeat-observation", "100")
	for i := range 55 {
		coverageReport(
			t,
			store,
			fmt.Sprintf("appearance-%03d", i),
			fmt.Sprintf("<!-- distinct body %d -->", i)+row+row,
		)
	}

	view, err := store.Dashboard(t.Context())
	if err != nil || len(view.Executions) != 1 {
		t.Fatal(view, err)
	}

	id := view.Executions[0].ID()
	first, _ := application.NewListQuery(
		application.ListOptions{Sort: "location", Direction: "asc"},
		application.ObservationScope,
	)

	page, err := store.ObservationList(t.Context(), "activity", id, first)
	if err != nil || page.Total != 110 || page.Matched != 110 || len(page.Items) != 50 {
		t.Fatal("default page lost appearance multiplicity", page, err)
	}

	repeat, err := store.ObservationList(t.Context(), "activity", id, first)
	if err != nil || !reflect.DeepEqual(page, repeat) {
		t.Fatal("stable observation identity ties changed", err)
	}

	filtered, _ := application.NewListQuery(
		application.ListOptions{Search: "TradeConfirm[2]", Page: 2, Sort: "location"},
		application.ObservationScope,
	)

	page, err = store.ObservationList(t.Context(), "activity", id, filtered)
	if err != nil || page.Total != 110 || page.Matched != 55 || len(page.Items) != 5 {
		t.Fatal("filter applied after paging", page, err)
	}

	for _, item := range page.Items {
		if !strings.HasSuffix(item.Location, "/TradeConfirm[2]") || item.Retrieved == "" {
			t.Fatal("qualified evidence context lost", item)
		}
	}

	matched, _ := application.NewListQuery(
		application.ListOptions{
			Search:             "accepted",
			ObservationMatches: []application.ObservationOutcome{application.ObservationAccepted},
		},
		application.ObservationScope,
	)

	page, err = store.ObservationList(t.Context(), "activity", id, matched)
	if err != nil || page.Matched != 55 || len(page.Items) != 50 {
		t.Fatal("typed outcome filter did not apply before count/page", page, err)
	}

	corroborated, _ := application.NewListQuery(
		application.ListOptions{
			Search:             "corroborating observation",
			ObservationMatches: []application.ObservationOutcome{application.ObservationCorroborated},
		},
		application.ObservationScope,
	)

	page, err = store.ObservationList(t.Context(), "activity", id, corroborated)
	if err != nil || page.Matched != 55 || len(page.Items) != 50 {
		t.Fatal("duplicate-row corroboration status lost", page, err)
	}

	if _, err = store.ObservationList(
		t.Context(),
		"instrument",
		"not-present",
		first,
	); !errors.Is(
		err,
		application.ErrNotFound,
	) {
		t.Fatal("missing observation subject", err)
	}

	if _, err = store.ObservationList(
		t.Context(),
		"account",
		id,
		first,
	); !errors.Is(
		err,
		application.ErrInvalid,
	) {
		t.Fatal("invalid observation subject", err)
	}
}

func TestUnderlierObservationPageUsesOriginalClaimBinding(t *testing.T) {
	store := coverageStore(t)
	coverageReport(t, store, "underlier-appearance", syntheticOption)

	view, err := store.Dashboard(t.Context())
	if err != nil || len(view.Executions) != 1 {
		t.Fatal(view, err)
	}

	detail, err := store.InstrumentDetail(t.Context(), view.Executions[0].Instrument())
	if err != nil || detail.Underlier == "" {
		t.Fatal(detail, err)
	}

	rows := observationRowsForTest(t, store, "instrument", detail.Underlier)
	if len(rows) != 1 || rows[0].Artifact != "underlier-appearance" {
		t.Fatal("underlier source appearance is not inspectable", rows)
	}
}

func TestObservationReportOrderingUsesAcquisitionInstant(t *testing.T) {
	store := coverageStore(t)

	row := equityRow("U1", "out-of-order", "100")
	stamps := []string{
		"2026-10-31T12:00:00.000000002Z",
		"2026-10-31T12:00:00Z",
		"2026-10-31T07:00:00.000000001-05:00",
	}
	ids := make([]string, len(stamps))
	for i, stamp := range stamps {
		ids[i] = acquireClaimReport(t, store, singleClaimBody(fmt.Sprintf("<!-- body%d -->", i)+row), false)
		if _, err := store.db.ExecContext(
			t.Context(),
			"UPDATE artifacts SET retrieved=? WHERE id=?",
			stamp,
			ids[i],
		); err != nil {
			t.Fatal(err)
		}
	}

	view, err := store.Dashboard(t.Context())
	if err != nil || len(view.Executions) != 1 {
		t.Fatal(view, err)
	}

	for _, direction := range []string{"asc", "desc"} {
		expected := []string{ids[1], ids[2], ids[0]}
		if direction == "desc" {
			expected = []string{ids[0], ids[2], ids[1]}
		}

		for i, want := range expected {
			query, err := application.NewListQuery(
				application.ListOptions{Sort: "report", Direction: direction, Page: i + 1, PageSize: 1},
				application.ObservationScope,
			)
			if err != nil {
				t.Fatal(err)
			}

			page, err := store.ObservationList(t.Context(), "activity", view.Executions[0].ID(), query)
			if err != nil || len(page.Items) != 1 || page.Items[0].Artifact != want {
				t.Fatal("timestamp order follows processing instead of exact acquisition", direction, i, page, err)
			}
		}
	}
}

func TestObservationOutcomeOrderingUsesPresentationLabels(t *testing.T) {
	store := coverageStore(t)
	row := equityRow("U1", "outcome-order", "100")
	coverageReport(t, store, "outcome-order", row+row+strings.Replace(row, `price="100"`, `price="101"`, 1))

	view, err := store.Dashboard(t.Context())
	if err != nil || len(view.Executions) != 1 {
		t.Fatal(view, err)
	}

	labels := map[application.ObservationOutcome]string{
		application.ObservationAccepted:      "accepted",
		application.ObservationCorroborated:  "corroborating observation",
		application.ObservationTermsConflict: "conflicting terms; needs review",
	}
	for _, direction := range []string{"asc", "desc"} {
		expected := []application.ObservationOutcome{
			application.ObservationAccepted,
			application.ObservationTermsConflict,
			application.ObservationCorroborated,
		}
		if direction == "desc" {
			expected = []application.ObservationOutcome{
				application.ObservationCorroborated,
				application.ObservationTermsConflict,
				application.ObservationAccepted,
			}
		}

		for i, want := range expected {
			query, err := application.NewListQuery(
				application.ListOptions{
					Sort:              "outcome",
					Direction:         direction,
					Page:              i + 1,
					PageSize:          1,
					ObservationLabels: labels,
				},
				application.ObservationScope,
			)
			if err != nil {
				t.Fatal(err)
			}

			page, err := store.ObservationList(t.Context(), "activity", view.Executions[0].ID(), query)
			if err != nil || len(page.Items) != 1 || page.Items[0].Outcome != want {
				t.Fatal("outcome page ignores displayed ordering", direction, i, page, err)
			}
		}
	}
}

func TestObservationRuleOrderingIncludesNormalizationVersion(t *testing.T) {
	store := coverageStore(t)
	row := equityRow("U1", "rule-order", "100")

	registration, err := ibkrflex.Registration(ibkrflex.Options{})
	if err != nil {
		t.Fatal(err)
	}

	for i := range 2 {
		id := acquireClaimReport(t, store, singleClaimBody(fmt.Sprintf("<!-- body%d -->", i)+row), false)
		for _, version := range []string{"z-version", "a-version"} {
			if _, err := store.Normalize(t.Context(), id, version, registration.Normalizer()); err != nil {
				t.Fatal(err)
			}

			if err := store.Process(t.Context(), id, version); err != nil {
				t.Fatal(err)
			}
		}
	}

	view, err := store.Dashboard(t.Context())
	if err != nil || len(view.Executions) != 1 {
		t.Fatal(view, err)
	}

	for _, direction := range []string{"asc", "desc"} {
		query, err := application.NewListQuery(
			application.ListOptions{Sort: "rule", Direction: direction},
			application.ObservationScope,
		)
		if err != nil {
			t.Fatal(err)
		}

		page, err := store.ObservationList(t.Context(), "activity", view.Executions[0].ID(), query)
		if err != nil || len(page.Items) != 6 {
			t.Fatal(page, err)
		}

		for i := 1; i < len(page.Items); i++ {
			previous := page.Items[i-1].Rule + " · " + page.Items[i-1].Version
			current := page.Items[i].Rule + " · " + page.Items[i].Version
			if direction == "asc" && previous > current || direction == "desc" && previous < current {
				t.Fatal("rule ordering ignored displayed version", page.Items)
			}
		}
	}
}
