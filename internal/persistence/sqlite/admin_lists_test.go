// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"go.trippwill.dev/wake/financial"
	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/persistence/sqlite/sqlc"
	"go.trippwill.dev/wake/provider"
	"go.trippwill.dev/wake/source"
)

func TestActivityListOrdersExactDecimalsAndPagesOnServer(t *testing.T) {
	s := coverageStore(t)
	ctx := t.Context()
	coverageReport(
		t,
		s,
		"precise-list",
		equityRow(
			"U1",
			"higher",
			"9007199254740993.0000000000002",
		)+equityRow(
			"U1",
			"lower",
			"9007199254740993.0000000000001",
		)+equityRow(
			"U1",
			"small",
			"1.0000000000001",
		),
	)

	query, err := application.NewListQuery(
		application.ListOptions{Sort: "price", Direction: "asc", PageSize: 2},
		application.ActivityScope,
	)
	if err != nil {
		t.Fatal(err)
	}

	first, err := s.ActivityList(ctx, query)
	if err != nil || len(first.Items) != 2 || first.Matched != 3 || first.Total != 3 {
		t.Fatal(first, err)
	}

	if first.Items[0].Resource.Activity.Price.Amount != "1.0000000000001" ||
		first.Items[1].Resource.Activity.Price.Amount != "9007199254740993.0000000000001" {
		t.Fatal("exact price ordering lost", first)
	}

	options := query.Options()
	options.Page = 2

	query, err = application.NewListQuery(options, application.ActivityScope)
	if err != nil {
		t.Fatal(err)
	}

	second, err := s.ActivityList(ctx, query)
	if err != nil || len(second.Items) != 1 ||
		second.Items[0].Resource.Activity.Price.Amount != "9007199254740993.0000000000002" {
		t.Fatal("server page did not continue exact order", second, err)
	}

	operatorID := first.Items[0].Resource.ID
	retainDecision(t, s, "activity", operatorID, "price", "1.0000000000001", "USD", "set")

	state, err := application.NewActivityState(application.Accepted, 0, application.OperatorSelection)
	if err != nil {
		t.Fatal(err)
	}

	query, err = application.NewListQuery(
		application.ListOptions{Search: "operator", StateMatches: []application.ActivityState{state}},
		application.ActivityScope,
	)
	if err != nil {
		t.Fatal(err)
	}

	matched, err := s.ActivityList(ctx, query)
	if err != nil || len(matched.Items) != 1 || matched.Items[0].Resource.ID != operatorID ||
		matched.Matched != 1 {
		t.Fatal("rendered-label search did not use structured state", matched, err)
	}
}

func TestDecimalCollationIsExactAndTransitiveIncludingInvalidText(t *testing.T) {
	values := []string{
		"-1000",
		"-0.0000000000001",
		"0",
		"0.0000000000001",
		"2",
		"2.00",
		"10",
		"9007199254740993.0000000000001",
		"9007199254740993.0000000000002",
		"1/2",
		"1a",
		"bad",
		"",
	}
	for _, a := range values {
		for _, b := range values {
			if compareDecimalText(a, b) != -compareDecimalText(b, a) {
				t.Fatal("collation is not antisymmetric", a, b)
			}

			for _, c := range values {
				if compareDecimalText(a, b) <= 0 && compareDecimalText(b, c) <= 0 && compareDecimalText(a, c) > 0 {
					t.Fatal("collation is not transitive", a, b, c)
				}
			}
		}
	}

	if compareDecimalText("2", "2.00") != 0 ||
		compareDecimalText("9007199254740993.0000000000001", "9007199254740993.0000000000002") >= 0 {
		t.Fatal("numeric precision or equality lost")
	}
}

func TestStatusAndPrerequisitesDoNotReadFinancialSnapshots(t *testing.T) {
	s := coverageStore(t)
	ctx := t.Context()
	coverageReport(t, s, "independent-status", equityRow("U1", "fill", "10"))

	if _, err := s.db.ExecContext(
		ctx,
		"UPDATE published_resources SET snapshot=X'00' WHERE kind='activity'",
	); err != nil {
		t.Fatal(err)
	}

	if _, err := s.Prerequisites(ctx); err != nil {
		t.Fatal("prerequisites read financial data", err)
	}

	if _, err := s.SystemStatus(ctx); err != nil {
		t.Fatal("status read financial data", err)
	}

	query, _ := application.NewListQuery(application.ListOptions{}, application.SourceScope)
	if _, err := s.SourcePage(ctx, query); err != nil {
		t.Fatal("sources read financial data", err)
	}

	query, _ = application.NewListQuery(application.ListOptions{}, application.ActivityScope)
	if _, err := s.ActivityList(ctx, query); err == nil {
		t.Fatal("fixture did not corrupt financial snapshot")
	}
}

func TestActivityListDefaultBoundStableTiesAndUnknowns(t *testing.T) {
	s := coverageStore(t)
	ctx := t.Context()

	var report strings.Builder
	for n := 0; n < 53; n++ {
		report.WriteString(equityRow("U1", fmt.Sprintf("equal-%02d", n), "10"))
	}

	coverageReport(t, s, "bounded-list", report.String())

	query, _ := application.NewListQuery(
		application.ListOptions{Sort: "price", Direction: "desc"},
		application.ActivityScope,
	)

	first, err := s.ActivityList(ctx, query)
	if err != nil || len(first.Items) != 50 || first.Matched != 53 {
		t.Fatal(first, err)
	}

	options := query.Options()
	options.Page = 2
	query, _ = application.NewListQuery(options, application.ActivityScope)

	second, err := s.ActivityList(ctx, query)
	if err != nil || len(second.Items) != 3 {
		t.Fatal(second, err)
	}

	prior := ""
	for _, item := range append(first.Items, second.Items...) {
		if item.Resource.ID <= prior {
			t.Fatal("equal-price pages lost stable identity order", prior, item.Resource.ID)
		}

		prior = item.Resource.ID
	}
	// The conversion rate may be explicitly unknown. It sorts last in either direction.
	prerequisite, _ := s.Prerequisites(ctx)
	date, _ := financial.ParseDate("2026-10-20")
	usd, _ := financial.ParseCurrency("USD")
	eur, _ := financial.ParseCurrency("EUR")
	sent, _ := source.NewCurrencyAmount(eur, "")
	received, _ := source.NewCurrencyAmount(usd, "")
	event, _ := source.NewReference("synthetic", "event", "account", "unknown-rate")

	conversion, e := source.NewConversion(
		source.ConversionInput{
			Location:      "unknown-rate",
			Account:       prerequisite.Account.Reference(),
			Event:         event,
			EventDistinct: true,
			Date:          date,
			Sent:          sent,
			Received:      received,
		},
	)
	if e != nil {
		t.Fatal(e)
	}

	coverageClaims(
		t,
		s,
		"unknown-price",
		claimNormalizer(
			func(ctx context.Context, _ provider.Artifact, claims provider.Claims) (provider.Result, error) {
				if e := claims.PutConversion(ctx, conversion); e != nil {
					return provider.Result{}, e
				}

				return provider.NewResult(provider.Counts{Rows: 1, Conversions: 1})
			},
		),
	)

	for _, direction := range []string{"asc", "desc"} {
		query, _ = application.NewListQuery(
			application.ListOptions{Sort: "price", Direction: direction, PageSize: 200},
			application.ActivityScope,
		)
		page, e := s.ActivityList(ctx, query)
		if e != nil || len(page.Items) != 54 || page.Items[53].Resource.Activity.Family != "conversion" ||
			page.Items[53].Resource.Activity.Rate != nil {
			t.Fatal("unknown price was not last", direction, page, e)
		}
	}
}

func TestReviewListRetainsFinancialQuestionsWithoutRepeatedAppearances(t *testing.T) {
	s := coverageStore(t)
	ctx := t.Context()
	coverageReport(t, s, "initial-option", syntheticOption)

	view, err := s.Dashboard(ctx)
	if err != nil {
		t.Fatal(err)
	}

	fields := view.Executions[0].Terms().Fields()
	disputed := strings.Replace(syntheticOption, `strike="100.00000000001"`, `strike="110"`, 1)
	coverageReport(t, s, "disputed-option", disputed)

	target, _ := source.NewReference("synthetic", "event", "account", "not-yet-reported")

	instruction, err := source.NewCorrection(
		source.CorrectionInput{
			Location: "pending-instruction",
			Account:  fields.Account,
			Kind:     source.Cancellation,
			Target:   target,
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	coverageClaims(
		t,
		s,
		"pending-instruction",
		claimNormalizer(
			func(ctx context.Context, _ provider.Artifact, claims provider.Claims) (provider.Result, error) {
				if e := claims.PutCorrection(ctx, instruction); e != nil {
					return provider.Result{}, e
				}

				return provider.NewResult(provider.Counts{Rows: 1, Corrections: 1})
			},
		),
	)

	fields.Location = "unknown-identity"
	fields.Event = source.Reference{}
	fields.EventAliases = nil
	fields.EventDistinct = false

	unidentified, err := source.NewTrade(fields)
	if err != nil {
		t.Fatal(err)
	}

	coverageClaims(
		t,
		s,
		"unknown-identity",
		claimNormalizer(
			func(ctx context.Context, _ provider.Artifact, claims provider.Claims) (provider.Result, error) {
				if e := claims.PutTrade(ctx, unidentified); e != nil {
					return provider.Result{}, e
				}

				return provider.NewResult(provider.Counts{Rows: 1, Trades: 1})
			},
		),
	)

	query, _ := application.NewListQuery(application.ListOptions{PageSize: 2}, application.ReviewScope)

	first, err := s.ReviewList(ctx, query)
	if err != nil || first.Total != 4 || len(first.Items) != 2 {
		t.Fatal(first, err)
	}

	options := query.Options()
	options.Page = 2
	query, _ = application.NewListQuery(options, application.ReviewScope)

	second, err := s.ReviewList(ctx, query)
	if err != nil || len(second.Items) != 2 {
		t.Fatal(second, err)
	}

	kinds := map[application.ReviewKind]bool{}
	for _, entry := range append(first.Items, second.Items...) {
		kinds[entry.Kind] = true
	}

	if len(kinds) != 4 {
		t.Fatal("review categories collapsed", kinds)
	}

	coverageReport(t, s, "repeat-dispute", disputed)

	repeated, err := s.ReviewList(ctx, query)
	if err != nil || repeated.Total != 4 {
		t.Fatal("provenance appeared as another review question", repeated, err)
	}

	overview, err := s.Overview(ctx)
	if err != nil || overview.Review.Total != 4 || overview.ActivityCount != 1 {
		t.Fatal("overview differs from focused lists", overview, err)
	}
}

func TestSourceCandidatesPageAndExactQualifiedLookup(t *testing.T) {
	s := coverageStore(t)
	ctx := t.Context()
	recordTestReceipt(t, s, "discovery")

	for n := 0; n < 53; n++ {
		if err := s.queries.PutCandidate(
			ctx,
			sqlc.PutCandidateParams{
				ReceiptID:  "discovery",
				ArtifactID: "discovery",
				Authority:  "synthetic",
				Kind:       "account",
				Scope:      "tenant",
				Value:      fmt.Sprintf("candidate-%02d", n),
				Name:       fmt.Sprintf("Candidate %02d", n),
			},
		); err != nil {
			t.Fatal(err)
		}
	}

	query, _ := application.NewListQuery(application.ListOptions{}, application.SourceScope)

	first, err := s.SourceDetail(ctx, "synthetic", query)
	if err != nil || len(first.Connection.Candidates) != 50 || first.Total != 53 {
		t.Fatal(first, err)
	}

	options := query.Options()
	options.Page = 2
	query, _ = application.NewListQuery(options, application.SourceScope)

	second, err := s.SourceDetail(ctx, "synthetic", query)
	if err != nil || len(second.Connection.Candidates) != 3 {
		t.Fatal(second, err)
	}

	selected := second.Connection.Candidates[2]
	exact, err := s.SourceCandidate(ctx, "synthetic", selected.Artifact, selected.Candidate.Reference())
	if err != nil || exact.Candidate.Reference() != selected.Candidate.Reference() {
		t.Fatal("candidate beyond first page cannot be confirmed", exact, err)
	}

	wrong, _ := source.NewReference("another", "account", "tenant", selected.Candidate.Reference().Value())
	if _, err = s.SourceCandidate(
		ctx,
		"synthetic",
		selected.Artifact,
		wrong,
	); !errors.Is(
		err,
		application.ErrCandidate,
	) {
		t.Fatal("unqualified candidate lookup crossed authorities", err)
	}
}

func TestServerSearchPreservesUnicodeCaseMatching(t *testing.T) {
	s := coverageStore(t)
	ctx := t.Context()
	coverageReport(t, s, "unicode", equityRow("U1", "unicode-fill", "10"))

	view, err := s.Dashboard(ctx)
	if err != nil {
		t.Fatal(err)
	}

	retainDecision(t, s, "instrument", view.Executions[0].Instrument(), "name", "ÉCOLE ΑΛΦΑ", "", "set")

	query, _ := application.NewListQuery(
		application.ListOptions{Search: "école αλφα"},
		application.ActivityScope,
	)

	activities, err := s.ActivityList(ctx, query)
	if err != nil || activities.Matched != 1 {
		t.Fatal("server activity search lost Unicode matching", activities, err)
	}

	query, _ = application.NewListQuery(
		application.ListOptions{Search: "école αλφα"},
		application.InstrumentScope,
	)

	instruments, err := s.InstrumentList(ctx, query)
	if err != nil || instruments.Matched != 1 {
		t.Fatal("server instrument search lost Unicode matching", instruments, err)
	}
}
