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
	"time"

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
		application.ListOptions{Search: "USD", Kind: "trade", Sort: "price", Direction: "asc", PageSize: 2},
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
		"-.0001e-9",
		"-0.0000",
		"+000.00",
		"2e+0",
		"1e128",
		strings.Repeat("9", 256),
		strings.Repeat("9", 257),
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
		compareDecimalText("2", "+0002.00e0") != 0 ||
		compareDecimalText("-.0001e-9", "-0.0000000000001") != 0 ||
		compareDecimalText("-0.0000", "0") != 0 ||
		compareDecimalText(strings.Repeat("9", 256), strings.Repeat("9", 257)) >= 0 ||
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

func TestActivitySearchReadsCommittedSnapshotDuringWrite(t *testing.T) {
	ctx := t.Context()
	s := coverageStore(t)
	coverageReport(t, s, "read-while-writing", equityRow("U1", "read-fill", "10.0000"))

	query, err := application.NewListQuery(
		application.ListOptions{Search: "10.0000", Kind: "trade", Sort: "price"}, application.ActivityScope,
	)
	if err != nil {
		t.Fatal(err)
	}

	before, err := s.ActivityList(ctx, query)
	if err != nil || before.Matched != 1 {
		t.Fatal(before, err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = tx.Rollback() }()

	if _, err = tx.ExecContext(ctx, `
UPDATE published_resources
SET snapshot=CAST(json_set(CAST(snapshot AS TEXT),'$.activity.price.amount','11.0000') AS BLOB)
WHERE kind='activity'`); err != nil {
		t.Fatal(err)
	}

	read, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	during, err := s.ActivityList(read, query)
	if err != nil || !reflect.DeepEqual(before, during) {
		t.Fatal("Activity search did not see the committed snapshot", during, err)
	}

	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}

	after, err := s.ActivityList(ctx, query)
	if err != nil || after.Total != 1 || after.Matched != 0 || len(after.Items) != 0 {
		t.Fatal("Activity search did not see the committed price update", after, err)
	}
}

func TestActivityDateIndexesPreserveRetainedPages(t *testing.T) {
	ctx := t.Context()
	s := coverageStore(t)
	coverageReport(t, s, "date-pages",
		equityRow("U1", "equal-a", "10.00")+equityRow("U1", "equal-b", "10.000")+
			strings.ReplaceAll(equityRow("U1", "next-day", "11.0"), "2026-10-20", "2026-10-21"))
	acquireRealized(t, s, movementReport("CashTransactions", cashRow))

	// Capture the ordinary query for both directions, date ties, filtered
	// counts, empty families and pages past the end.
	type expectedPage struct {
		query application.ListQuery
		page  application.ActivityList
	}

	var expected []expectedPage
	for _, direction := range []string{"asc", "desc"} {
		for _, kind := range []string{"", "trade", "cash", "conversion"} {
			for page := 1; page <= 5; page++ {
				query, err := application.NewListQuery(application.ListOptions{
					Kind: kind, Sort: "date", Direction: direction, Page: page, PageSize: 1,
				}, application.ActivityScope)
				if err != nil {
					t.Fatal(err)
				}

				want, err := s.readActivityPage(
					ctx, query, adminActivitySQL, "", kind, "", "", "{}",
					false, false, "[]", "[]", "[]", "date", direction, 1, query.Offset(),
				)
				if err != nil {
					t.Fatal(err)
				}

				expected = append(expected, expectedPage{query, want})
			}
		}
	}

	profile := testProfile(s.owners[0].Name())
	if _, err := s.db.ExecContext(
		ctx,
		"DROP INDEX activity_publication_date; DROP INDEX activity_publication_family_date",
	); err != nil {
		t.Fatal(err)
	}

	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(ctx, profile, nil)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = reopened.Close() })

	for _, want := range expected {
		got, err := reopened.ActivityList(ctx, want.query)
		if err != nil || !reflect.DeepEqual(got, want.page) {
			t.Fatalf(
				"options %+v: indexed page %+v, ordinary page %+v, error %v",
				want.query.Options(),
				got,
				want.page,
				err,
			)
		}
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

	options.Page = 3
	query, _ = application.NewListQuery(options, application.ActivityScope)

	beyond, err := s.ActivityList(ctx, query)
	if err != nil || len(beyond.Items) != 0 || beyond.Total != 53 || beyond.Matched != 53 {
		t.Fatal("page past the last match lost counts", beyond, err)
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
				if e := claims.PutActivity(ctx, conversion.Observation()); e != nil {
					return provider.Result{}, e
				}

				return provider.NewResult(provider.Counts{Rows: 1, Activities: 1})
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
	disputed := strings.NewReplacer(`strike="100.00000000001"`, `strike="110"`, `underlyingConid="100"`, `underlyingConid="777"`).
		Replace(syntheticOption)
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
				if e := claims.PutActivity(ctx, unidentified.Observation()); e != nil {
					return provider.Result{}, e
				}

				return provider.NewResult(provider.Counts{Rows: 1, Activities: 1})
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
			ActivityText: application.ActivityText{
				Unknown:            "Unknown",
				LifecycleSeparator: " · ",
				ConversionPrefix:   "Convert ",
				PairSeparator:      " → ",
				AmountSeparator:    " · ",
				Sent:               "sent",
				Received:           "received",
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

	h, err := host.New(ctx, s, nil, nil, nil, nil, registration)
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

	h, err := host.New(t.Context(), s, nil, nil, nil, nil, registration)
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

func TestAdminTickerIdentityPreservesDescriptionSearchAndOrdering(t *testing.T) {
	s := coverageStore(t)
	first := strings.NewReplacer(`symbol="SYN"`, `symbol="AAA"`, "Synthetic equity", "Zebra").
		Replace(equityRow("U1", "ticker-a", "10"))
	second := strings.NewReplacer(`symbol="SYN"`, `symbol="ZZZ"`, "Synthetic equity", "Alpha", `conid="100"`, `conid="101"`).
		Replace(equityRow("U1", "ticker-z", "10"))
	coverageReport(t, s, "ticker-order", first+second)

	for _, direction := range []string{"asc", "desc"} {
		query, _ := application.NewListQuery(
			application.ListOptions{Sort: "label", Direction: direction, PageSize: 1},
			application.ActivityScope,
		)
		page, err := s.ActivityList(t.Context(), query)

		want := "AAA"
		if direction == "desc" {
			want = "ZZZ"
		}

		if err != nil || len(page.Items) != 1 || page.Items[0].InstrumentTicker != want || page.Matched != 2 {
			t.Fatal("activity ordering does not follow its ticker", page, err)
		}
	}

	query, _ := application.NewListQuery(application.ListOptions{}, application.InstrumentScope)

	instruments, err := s.InstrumentList(t.Context(), query)
	if err != nil || len(instruments.Items) != 2 || *instruments.Items[0].Resource.Instrument.Ticker != "AAA" {
		t.Fatal("instruments do not default to ticker ordering", instruments, err)
	}

	coverageReport(t, s, "ticker-dispute", strings.Replace(first, `price="10"`, `price="11"`, 1))

	for _, search := range []string{"buy aaa", "Zebra"} {
		query, _ = application.NewListQuery(
			application.ListOptions{Search: search, ActivityText: application.ActivityText{Unknown: "Unknown"}},
			application.ActivityScope,
		)
		page, e := s.ActivityList(t.Context(), query)
		if e != nil || page.Matched != 1 || len(page.Items) != 1 || page.Items[0].InstrumentTicker != "AAA" ||
			page.Items[0].InstrumentName != "Zebra" {
			t.Fatal("activity ticker or description search failed", search, page, e)
		}

		query, _ = application.NewListQuery(
			application.ListOptions{Search: search, ActivityText: application.ActivityText{Unknown: "Unknown"}},
			application.ReviewScope,
		)
		review, e := s.ReviewList(t.Context(), query)
		if e != nil || review.Matched != 1 || len(review.Items) != 1 || review.Items[0].Ticker != "AAA" ||
			review.Items[0].Name != "Zebra" {
			t.Fatal("review ticker or description search failed", search, review, e)
		}
	}

	id := instruments.Items[0].Resource.ID

	form, err := s.CorrectionForm(t.Context(), "instrument", id)
	if err != nil || form.Label != "AAA · Zebra · equity" || len(form.Fields) < 2 ||
		form.Fields[0] != "ticker" ||
		form.Fields[1] != "name" {
		t.Fatal("correction context does not lead with ticker", form, err)
	}
}

func TestInstrumentTickerOrderingUsesDisplayedFallbacks(t *testing.T) {
	s := coverageStore(t)

	want := []struct{ name, ticker string }{
		{"Zebra", "aaa"},
		{"Alpha", ""},
		{"Beta", ""},
		{"", ""},
		{"First description", "ZZZ"},
	}

	var rows strings.Builder
	for i, item := range want {
		rows.WriteString(strings.NewReplacer(
			`conid="100"`, fmt.Sprintf(`conid="%d"`, 100+i),
			`symbol="SYN"`, `symbol="`+item.ticker+`"`,
			`description="Synthetic equity"`, `description="`+item.name+`"`,
		).Replace(equityRow("U1", fmt.Sprintf("ticker-fallback-%d", i), "10")))
	}

	coverageReport(t, s, "ticker-fallback-order", rows.String())

	for _, direction := range []string{"asc", "desc"} {
		for page := 1; page <= 3; page++ {
			query, err := application.NewListQuery(application.ListOptions{
				Direction: direction, Page: page, PageSize: 2,
			}, application.InstrumentScope)
			if err != nil {
				t.Fatal(err)
			}

			got, err := s.InstrumentList(t.Context(), query)
			if err != nil || got.Total != 5 || got.Matched != 5 || len(got.Items) != min(2, 5-2*(page-1)) {
				t.Fatal(got, err)
			}

			for i, item := range got.Items {
				position := 2*(page-1) + i
				if direction == "desc" {
					position = len(want) - 1 - position
				}

				name, ticker := "", ""
				if item.Resource.Instrument.Name != nil {
					name = *item.Resource.Instrument.Name
				}

				if item.Resource.Instrument.Ticker != nil {
					ticker = *item.Resource.Instrument.Ticker
				}

				if name != want[position].name || ticker != want[position].ticker {
					t.Fatalf(
						"%s page %d row %d: name %q ticker %q, want %+v",
						direction,
						page,
						i,
						name,
						ticker,
						want[position],
					)
				}
			}
		}
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
		{application.DisplayMatches{MissingValues: true}, "unknown", 2}, // Both activities lack reported P&L.
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
				if e := claims.PutActivity(ctx, trade.Observation()); e != nil {
					return provider.Result{}, e
				}

				return provider.NewResult(provider.Counts{Rows: 1, Activities: 1})
			},
		),
	)

	query, _ := application.NewListQuery(
		application.ListOptions{
			Search:       "buy unknown",
			ActivityText: application.ActivityText{Unknown: "Unknown", LifecycleSeparator: " · "},
		},
		application.ActivityScope,
	)

	page, err := s.ActivityList(t.Context(), query)
	if err != nil || page.Matched != 1 || page.Items[0].InstrumentName != "" {
		t.Fatal("unknown instrument label phrase not searchable", page.Matched, err)
	}
}

func TestConnectionAccountBindingUsesLatestDiscoverySnapshot(t *testing.T) {
	s := coverageStore(t)
	ctx := t.Context()
	connection := host.Connection{ID: "second-source", Provider: "ibkr-flex", Name: "Another source"}
	if err := s.Configure(ctx, connection, nil); err != nil {
		t.Fatal(err)
	}

	details, err := application.NewAccountDetails("Another account", "")
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	key := host.WorkKey{Connection: connection.ID, Stream: "confirmations", Operation: "discover"}
	discover := func(body string, candidates ...provider.Candidate) string {
		t.Helper()

		checkpoint, err := s.Checkpoint(ctx, key)
		if err != nil {
			t.Fatal(err)
		}

		artifact, _ := provider.NewArtifact("synthetic", "application/json", now, []byte(body))
		step, _ := provider.NewStep(provider.State{}, artifact, now)

		discovery, err := provider.NewDiscovery(step, candidates)
		if err != nil {
			t.Fatal(err)
		}

		discovery, err = discovery.WithCandidateSnapshot()
		if err != nil {
			t.Fatal(err)
		}

		id, err := s.Accept(
			ctx,
			key,
			checkpoint.Revision,
			discovery.Step(),
			candidates,
			provider.Progress{},
			host.PollCycle{},
		)
		if err != nil {
			t.Fatal(err)
		}

		return id
	}

	existing, _ := source.NewReference("ibkr", "account", "", "U1")
	duplicate, _ := provider.NewCandidate(existing, "Already bound", financial.Currency{})
	first := discover(`["already-bound"]`, duplicate)
	if _, err = s.Confirm(
		ctx,
		connection,
		first,
		existing,
		details,
	); !errors.Is(
		err,
		application.ErrAccountBound,
	) {
		t.Fatalf("duplicate authority: %v", err)
	}

	distinct, _ := source.NewReference("ibkr", "account", "", "U2")
	candidate, _ := provider.NewCandidate(distinct, "Second account", financial.Currency{})
	available := discover(`["new-account"]`, candidate)
	_ = discover(`[]`)

	if _, err = s.Confirm(
		ctx,
		connection,
		available,
		distinct,
		details,
	); !errors.Is(
		err,
		application.ErrCandidate,
	) {
		t.Fatalf("withdrawn candidate: %v", err)
	}

	available = discover(`["new-account"]`, candidate)

	second, err := s.Confirm(ctx, connection, available, distinct, details)
	if err != nil {
		t.Fatal(err)
	}

	snapshot, err := s.Snapshot(ctx, application.SnapshotCondition{})
	if err != nil || len(snapshot.Snapshot.Resources) != 2 {
		t.Fatalf("both accounts must publish: %+v %v", snapshot, err)
	}

	for id, want := range map[string]string{"account": "Synthetic account", second.ID(): details.Name()} {
		resource, err := s.Resource(ctx, "accounts", id)
		if err != nil || resource.Account == nil || resource.Account.Name != want {
			t.Fatalf("account %q: %+v %v", id, resource, err)
		}
	}
}
