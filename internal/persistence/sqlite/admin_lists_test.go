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
	"go.trippwill.dev/wake/internal/host"
	"go.trippwill.dev/wake/internal/persistence/sqlite/sqlc"
	"go.trippwill.dev/wake/provider"
	"go.trippwill.dev/wake/providers/ibkrflex"
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

	for _, test := range []struct {
		search  string
		matches []application.ReviewMatch
		want    int64
	}{
		{"identity", []application.ReviewMatch{{Kind: application.ReviewInstrument}, {Kind: application.ReviewIdentity}}, 2},
		{"unresolved", []application.ReviewMatch{{Kind: application.ReviewInstruction, Status: application.CorrectionUnresolved}, {Kind: application.ReviewIdentity}}, 2},
		{"needs review", []application.ReviewMatch{{Kind: application.ReviewInstrument}, {Kind: application.ReviewInstruction, Status: application.CorrectionUnresolved}, {Kind: application.ReviewInstruction, Status: application.CorrectionNeedsReview}}, 2},
		{"1 source claims", []application.ReviewMatch{{Kind: application.ReviewIdentity, CountKnown: true, Count: 1}}, 1},
	} {
		query, _ := application.NewListQuery(
			application.ListOptions{Search: test.search, ReviewMatches: test.matches, PageSize: 1},
			application.ReviewScope,
		)
		page, e := s.ReviewList(ctx, query)
		if e != nil || page.Matched != test.want || page.Total != 4 || len(page.Items) != 1 {
			t.Fatal("review matches disagree with bounded count/rows", test.search, page.PageInfo, e)
		}
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

func TestFocusedReadsPreserveActivityMeaning(t *testing.T) {
	s := coverageStore(t)
	ctx := t.Context()
	buy := strings.ReplaceAll(equityRow("U1", "buy-zebra", "10"), "Synthetic equity", "Zebra")
	sell := strings.ReplaceAll(
		strings.ReplaceAll(
			strings.ReplaceAll(equityRow("U1", "sell-apple", "10"), "Synthetic equity", "Apple"),
			`conid="100"`,
			`conid="101"`,
		),
		`buySell="BUY"`,
		`buySell="SELL"`,
	)
	coverageReport(t, s, "labels", buy+sell+syntheticFX)

	query, _ := application.NewListQuery(
		application.ListOptions{Sort: "label", Direction: "asc", PageSize: 1},
		application.ActivityScope,
	)

	first, err := s.ActivityList(ctx, query)
	if err != nil || len(first.Items) != 1 || first.Items[0].Resource.Activity.Side == nil ||
		*first.Items[0].Resource.Activity.Side != "buy" {
		t.Fatal("label order differs from visible action order", first, err)
	}

	for _, test := range []struct {
		search string
		want   int64
	}{
		{"shares", 2},
		{"0.000000000123 shares", 2},
		{"sent", 1},
		{"100.123456789 EUR sent", 1},
		{"received", 1},
		{"USD received", 1},
		{"EUR → USD", 1},
		{"100.123456789 EUR sent · Unknown USD received", 1},
		{"sent · Unknown USD", 1},
		{"EUR sent · Unknown EUR", 0},
	} {
		options := application.ListOptions{
			Search:   test.search,
			PageSize: 1,
			ConversionText: application.ConversionText{
				Unknown:         "Unknown",
				LabelPrefix:     "Convert ",
				PairSeparator:   " → ",
				AmountSeparator: " · ",
				Sent:            "sent",
				Received:        "received",
			},
		}

		query, _ = application.NewListQuery(options, application.ActivityScope)

		results, e := s.ActivityList(ctx, query)
		if e != nil || results.Matched != test.want {
			t.Fatal("visible quantity search", test.search, results.Matched, e)
		}
	}

	coverageReport(
		t,
		s,
		"disputed-conversion",
		strings.Replace(syntheticFX, `netCash="123.25"`, `netCash="124.25"`, 1),
	)

	query, _ = application.NewListQuery(application.ListOptions{Search: "EUR USD"}, application.ReviewScope)

	reviews, err := s.ReviewList(ctx, query)
	if err != nil || len(reviews.Items) != 1 || reviews.Items[0].Activity == nil ||
		reviews.Items[0].Activity.Family != "conversion" {
		t.Fatal("review lost currency pair and structured conversion", reviews, err)
	}
}

func TestRecoveryReadinessAndRepairPageIncludeLateConnections(t *testing.T) {
	s, _ := testStore(t)
	ctx := t.Context()
	settings, _ := provider.NewSettings(map[string]string{"query_id": "123"})
	for n := 0; n < 51; n++ {
		id := fmt.Sprintf("source-%02d", n)
		if err := s.Configure(
			ctx,
			host.Connection{ID: id, Provider: "ibkr-flex", Settings: settings},
			nil,
		); err != nil {
			t.Fatal(err)
		}

		if n < 50 {
			if _, err := s.db.ExecContext(
				ctx,
				"INSERT INTO access.credentials(connection_id,key,encrypted) VALUES(?,'token',X'01')",
				id,
			); err != nil {
				t.Fatal(err)
			}
		}
	}

	registration, err := ibkrflex.Registration(ibkrflex.Options{})
	if err != nil {
		t.Fatal(err)
	}

	h, err := host.New(ctx, s, nil, nil, nil, registration)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()

	workflow := application.New(s, h, nil, nil, nil)
	query, _ := application.NewListQuery(application.ListOptions{}, application.SourceScope)

	first, ready, err := workflow.Recovery(ctx, query)
	if err != nil || ready || len(first.Connections) != 50 || first.Total != 51 {
		t.Fatal("first page hid missing access", first, ready, err)
	}

	query, _ = application.NewListQuery(application.ListOptions{Page: 2}, application.SourceScope)

	second, ready, err := workflow.Recovery(ctx, query)
	if err != nil || ready || len(second.Connections) != 1 || !second.Connections[0].MissingAccess {
		t.Fatal("late source has no repair page", second, ready, err)
	}

	if _, err = s.db.ExecContext(
		ctx,
		"INSERT INTO access.credentials(connection_id,key,encrypted) VALUES('source-50','token',X'01')",
	); err != nil {
		t.Fatal(err)
	}

	_, ready, err = workflow.Recovery(ctx, query)
	if err != nil || !ready {
		t.Fatal("complete access remained disabled", ready, err)
	}
}

type renameDuringRecovery struct {
	*Store
	pages int
}

func (s *renameDuringRecovery) SourceAccessPage(
	ctx context.Context,
	query application.ListQuery,
) (application.SourcePage, error) {
	page, err := s.Store.SourceAccessPage(ctx, query)
	s.pages++
	if err == nil && s.pages == 2 {
		_, err = s.db.ExecContext(ctx, "UPDATE connections SET name='AAA' WHERE id='source-50'")
	}

	return page, err
}

func TestRecoveryAuthorityDoesNotEnumerateMutableUIPages(t *testing.T) {
	s, _ := testStore(t)
	settings, _ := provider.NewSettings(map[string]string{"query_id": "123"})
	for n := 0; n < 51; n++ {
		id := fmt.Sprintf("source-%02d", n)
		secrets := map[string][]byte{}
		if n < 50 {
			secrets["token"] = []byte("synthetic-encrypted")
		}

		if err := s.Configure(
			t.Context(),
			host.Connection{ID: id, Name: id, Provider: "ibkr-flex", Settings: settings},
			secrets,
		); err != nil {
			t.Fatal(err)
		}
	}

	registration, err := ibkrflex.Registration(ibkrflex.Options{})
	if err != nil {
		t.Fatal(err)
	}

	h, err := host.New(t.Context(), s, nil, nil, nil, registration)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()

	wrapped := &renameDuringRecovery{Store: s}
	workflow := application.New(wrapped, h, nil, nil, nil)
	query, _ := application.NewListQuery(application.ListOptions{}, application.SourceScope)

	_, ready, err := workflow.Recovery(t.Context(), query)
	if err != nil || ready {
		t.Fatal("mutable offset page skipped a missing credential", ready, err)
	}
}

func TestActivityReadLabelsFallBackToKnownTicker(t *testing.T) {
	s := coverageStore(t)
	row := strings.Replace(
		equityRow("U1", "ticker-only", "10"),
		`description="Synthetic equity"`,
		`description=""`,
		1,
	)
	coverageReport(t, s, "ticker-only", row)

	query, _ := application.NewListQuery(
		application.ListOptions{Sort: "label", Direction: "asc"},
		application.ActivityScope,
	)

	page, err := s.ActivityList(t.Context(), query)
	if err != nil || len(page.Items) != 1 || page.Items[0].InstrumentName != "SYN" {
		t.Fatal("known ticker lost from activity label", page, err)
	}

	coverageReport(t, s, "ticker-dispute", strings.Replace(row, `price="10"`, `price="11"`, 1))

	query, _ = application.NewListQuery(application.ListOptions{}, application.ReviewScope)

	review, err := s.ReviewList(t.Context(), query)
	if err != nil || len(review.Items) != 1 || review.Items[0].Name != "SYN" {
		t.Fatal("known ticker lost from review label", review, err)
	}
}

func TestReferenceOrderingIncludesSelectionAuthority(t *testing.T) {
	s := coverageStore(t)
	first := equityRow("U1", "reference-a", "10")
	second := strings.Replace(equityRow("U1", "reference-b", "10"), `conid="100"`, `conid="101"`, 1)
	coverageReport(t, s, "reference-order", first+second)

	query, _ := application.NewListQuery(application.ListOptions{}, application.ActivityScope)

	before, err := s.ActivityList(t.Context(), query)
	if err != nil || len(before.Items) != 2 {
		t.Fatal(before, err)
	}

	selected := before.Items[0]
	retainDecision(
		t,
		s,
		"instrument",
		*selected.Resource.Activity.InstrumentID,
		"name",
		selected.InstrumentName,
		"",
		"set",
	)

	for _, direction := range []string{"asc", "desc"} {
		query, _ = application.NewListQuery(
			application.ListOptions{Sort: "reference", Direction: direction, PageSize: 1},
			application.ActivityScope,
		)

		page, err := s.ActivityList(t.Context(), query)
		if err != nil || len(page.Items) != 1 {
			t.Fatal(page, err)
		}

		expected := application.AutomaticSelection
		if direction == "desc" {
			expected = application.OperatorSelection
		}

		if page.Items[0].Reference.Authority != expected {
			t.Fatal("reference ordering ignored visible authority", direction, page.Items[0].Reference)
		}

		query, _ = application.NewListQuery(
			application.ListOptions{Sort: "state", Direction: direction, PageSize: 1},
			application.InstrumentScope,
		)
		instruments, err := s.InstrumentList(t.Context(), query)
		if err != nil || len(instruments.Items) != 1 || instruments.Items[0].Authority != expected {
			t.Fatal("instrument reference ordering ignored authority", direction, instruments, err)
		}
	}
}

func TestFullActivityLabelSearchUsesCanonicalComponents(t *testing.T) {
	s := coverageStore(t)
	row := strings.Replace(equityRow("U1", "phrase", "10"), "Synthetic equity", "Zebra", 1)
	coverageReport(t, s, "phrase", row)

	query, _ := application.NewListQuery(
		application.ListOptions{Search: "buy zebra"},
		application.ActivityScope,
	)

	page, err := s.ActivityList(t.Context(), query)
	if err != nil || page.Matched != 1 {
		t.Fatal("full displayed activity label is not searchable", page.Matched, err)
	}

	coverageReport(t, s, "phrase-dispute", strings.Replace(row, `price="10"`, `price="11"`, 1))

	query, _ = application.NewListQuery(application.ListOptions{Search: "buy zebra"}, application.ReviewScope)

	review, err := s.ReviewList(t.Context(), query)
	if err != nil || review.Matched != 1 {
		t.Fatal("full displayed review label is not searchable", review.Matched, err)
	}
}

func TestMissingValueMatchesCountAndRowsBeforePagination(t *testing.T) {
	s := coverageStore(t)
	lifecycle := strings.Replace(
		strings.Replace(
			syntheticOption,
			`transactionType="ExchTrade"`,
			`transactionType="BookTrade" code="A"`,
			1,
		),
		"option-fill",
		"missing-outcome",
		1,
	)
	coverageReport(t, s, "missing-values", syntheticFX+lifecycle)

	for _, test := range []struct {
		matches application.DisplayMatches
		search  string
		want    int64
	}{
		{application.DisplayMatches{MissingValues: true}, "unknown", 1},
		{application.DisplayMatches{InapplicableValues: true}, "not applicable", 2},
	} {
		t.Run(test.search, func(t *testing.T) {
			query, _ := application.NewListQuery(
				application.ListOptions{Search: test.search, DisplayMatches: test.matches, PageSize: 1},
				application.ActivityScope,
			)
			page, err := s.ActivityList(t.Context(), query)
			if err != nil || page.Matched != test.want || len(page.Items) != 1 {
				t.Fatal("missing-value count/row filters differ", page.PageInfo, len(page.Items), err)
			}
		})
	}

	// A known instrument identity can legitimately have neither reported label.
	instrumentRef, _ := source.NewReference("synthetic", "security", "", "unnamed")

	instrument, err := source.NewInstrument(
		source.InstrumentInput{Reference: instrumentRef, Kind: source.Equity},
	)
	if err != nil {
		t.Fatal(err)
	}

	coverageReport(t, s, "known-trade", equityRow("U1", "known-trade", "10"))

	view, err := s.Dashboard(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	input := view.Executions[0].Terms().Fields()
	input.Location = "unnamed-fill"
	input.Event, _ = source.NewReference("synthetic", "event", "account", "unnamed-fill")
	input.EventAliases = nil
	input.Instrument = instrument

	trade, err := source.NewTrade(input)
	if err != nil {
		t.Fatal(err)
	}

	coverageClaims(
		t,
		s,
		"unnamed-fill",
		claimNormalizer(
			func(ctx context.Context, _ provider.Artifact, claims provider.Claims) (provider.Result, error) {
				if e := claims.PutTrade(ctx, trade); e != nil {
					return provider.Result{}, e
				}

				return provider.NewResult(provider.Counts{Rows: 1, Trades: 1})
			},
		),
	)

	query, _ := application.NewListQuery(
		application.ListOptions{
			Search:         "buy unknown",
			LabelSearch:    "buy unknown",
			DisplayMatches: application.DisplayMatches{MissingInstrumentAction: "buy"},
		},
		application.ActivityScope,
	)

	page, err := s.ActivityList(t.Context(), query)
	if err != nil || page.Matched != 1 || page.Items[0].InstrumentName != "" {
		t.Fatal("unknown instrument label phrase not searchable", page.Matched, err)
	}
}
