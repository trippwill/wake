// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"go.trippwill.dev/wake/financial"
	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/provider"
	"go.trippwill.dev/wake/providers/ibkrflex"
	"go.trippwill.dev/wake/source"
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

func TestObservationPagesRetainContributionsAndFilterBeforePaging(t *testing.T) {
	store := coverageStore(t)
	for group, count := range []int{55, 5} {
		var rows strings.Builder
		for i := range count {
			row := equityRow("U1", "repeat-observation", fmt.Sprint(100+group*100+i))
			rows.WriteString(row + row)
		}

		coverageReport(t, store, []string{"matching-report", "other-report"}[group], rows.String())
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
	if err != nil || page.Total != 60 || page.Matched != 60 || len(page.Items) != 50 {
		t.Fatal("default page lost distinct contributions", page, err)
	}

	repeat, err := store.ObservationList(t.Context(), "activity", id, first)
	if err != nil || !reflect.DeepEqual(page, repeat) {
		t.Fatal("stable observation identity ties changed", err)
	}

	filtered, _ := application.NewListQuery(
		application.ListOptions{Search: "matching-report", Page: 2, Sort: "location"},
		application.ObservationScope,
	)

	page, err = store.ObservationList(t.Context(), "activity", id, filtered)
	if err != nil || page.Total != 60 || page.Matched != 55 || len(page.Items) != 5 {
		t.Fatal("filter applied after paging", page, err)
	}

	for _, item := range page.Items {
		if item.Artifact != "matching-report" || item.Retrieved == "" {
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
	if err != nil || page.Matched != 1 || len(page.Items) != 1 {
		t.Fatal("typed outcome filter did not apply before count/page", page, err)
	}

	conflicting, _ := application.NewListQuery(
		application.ListOptions{
			Search:             "conflicting terms",
			ObservationMatches: []application.ObservationOutcome{application.ObservationTermsConflict},
		},
		application.ObservationScope,
	)

	page, err = store.ObservationList(t.Context(), "activity", id, conflicting)
	if err != nil || page.Matched != 59 || len(page.Items) != 50 {
		t.Fatal("distinct conflict status lost", page, err)
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

func TestInstrumentContributionKeepsRelatedActivityAfterActivitySupportDiscarded(t *testing.T) {
	s := coverageStore(t)
	row := equityRow("U1", "instrument-name", "100")
	coverageReport(t, s, "original-name", row)
	coverageReport(t, s, "changed-name", strings.Replace(row, `description="`, `description="Changed `, 1))

	view, err := s.Dashboard(t.Context())
	if err != nil || len(view.Executions) != 1 {
		t.Fatal(view, err)
	}

	activity := view.Executions[0]
	if rows := observationRowsForTest(t, s, "activity", activity.ID()); len(rows) != 1 {
		t.Fatal("name change added activity support", rows)
	}

	rows := observationRowsForTest(t, s, "instrument", activity.Instrument())
	if len(rows) != 2 {
		t.Fatal("instrument name assertion was lost", rows)
	}

	for _, item := range rows {
		if item.ActivityID != activity.ID() {
			t.Fatal("discarded activity support lost instrument context", item)
		}
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
		ids[i] = acquireClaimReport(
			t,
			store,
			singleClaimBody(
				fmt.Sprintf(
					"<!-- body%d -->",
					i,
				)+strings.Replace(
					row,
					`price="100"`,
					fmt.Sprintf(`price="%d"`, 100+i),
					1,
				),
			),
			false,
		)
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
	base := strings.Replace(row, ` netCash="-0.000000049324000000123"`, "", 1)
	coverageReport(t, store, "outcome-order", base+row+strings.Replace(row, `price="100"`, `price="101"`, 1))

	view, err := store.Dashboard(t.Context())
	if err != nil || len(view.Executions) != 1 {
		t.Fatal(view, err)
	}

	labels := map[application.ObservationOutcome]string{
		application.ObservationAccepted:      "accepted",
		application.ObservationEnriched:      "adds source information",
		application.ObservationTermsConflict: "conflicting terms; needs review",
	}
	for _, direction := range []string{"asc", "desc"} {
		expected := []application.ObservationOutcome{
			application.ObservationAccepted,
			application.ObservationEnriched,
			application.ObservationTermsConflict,
		}
		if direction == "desc" {
			expected = []application.ObservationOutcome{
				application.ObservationTermsConflict,
				application.ObservationEnriched,
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

	for i := range 2 {
		id := acquireClaimReport(
			t,
			store,
			singleClaimBody(
				fmt.Sprintf(
					"<!-- body%d -->",
					i,
				)+strings.Replace(
					row,
					`price="100"`,
					fmt.Sprintf(`price="%d"`, 100+i),
					1,
				),
			),
			false,
		)
		records, err := store.Records(t.Context(), id, ibkrflex.Version)
		if err != nil || len(records) != 1 {
			t.Fatal(records, err)
		}

		for j, version := range []string{"z-version", "a-version"} {
			fields := observedTrade(t, records[0].Observation).Fields()
			fields.Price, _ = financial.ParsePrice(fmt.Sprint(200+i*2+j), fields.Price.Currency())

			trade, err := source.NewTrade(fields)
			if err != nil {
				t.Fatal(err)
			}

			normalizer := claimNormalizer(
				func(ctx context.Context, _ provider.Artifact, claims provider.Claims) (provider.Result, error) {
					if e := claims.PutActivity(ctx, trade.Observation()); e != nil {
						return provider.Result{}, e
					}

					return provider.NewResult(provider.Counts{Rows: 1, Activities: 1})
				},
			)
			if _, err := store.Normalize(t.Context(), id, version, normalizer); err != nil {
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
