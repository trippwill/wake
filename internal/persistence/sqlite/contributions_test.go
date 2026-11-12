// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"go.trippwill.dev/wake/financial"
	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/persistence/sqlite/sqlc"
	"go.trippwill.dev/wake/provider"
	"go.trippwill.dev/wake/providers/ibkrflex"
	"go.trippwill.dev/wake/source"
)

func TestRepeatedInternallyConflictingChargesDoNotAccumulateActivitySupport(t *testing.T) {
	s := newStoreWithSyntheticAccount(t)
	trade := underlierTrade(t, underlierTemplate(t), "repeated-charges", "", true)
	retainUnderlierTrades(t, s, "established", trade)
	base := trade.Observation().Fields()

	base.EventDistinct = true
	base.EventAliases = nil
	usd, _ := financial.ParseCurrency("USD")
	for _, amount := range []string{"1", "2"} {
		money, _ := financial.ParseMoney(amount, usd)
		charge, _ := source.NewCharge(
			source.ChargeInput{Category: source.Fee, Field: "reported-fee", Total: money},
		)

		field, err := source.NewActivityField(source.ActivityFieldInput{Name: source.FieldCharge, Charge: charge})
		if err != nil {
			t.Fatal(err)
		}

		base.Fields = append(base.Fields, field)
	}

	normalizer := claimNormalizer(
		func(ctx context.Context, _ provider.Artifact, claims provider.Claims) (provider.Result, error) {
			for _, location := range []string{"first", "duplicate"} {
				input := base
				input.Location = location

				observation, err := source.NewActivityObservation(input)
				if err != nil {
					return provider.Result{}, err
				}

				if err = claims.PutActivity(ctx, observation); err != nil {
					return provider.Result{}, err
				}
			}

			return provider.NewResult(provider.Counts{Rows: 2, Activities: 2})
		},
	)
	processSyntheticClaims(t, s, "charge-report", normalizer)

	baseline, err := snapshotState(t.Context(), s)
	if err != nil {
		t.Fatal(err)
	}

	for _, step := range []struct{ report, version string }{
		{"charge-report", "synthetic-claims/1"},
		{"repeat-charge-report", "synthetic-claims/1"},
		{"charge-report", "charge-reinterpretation/2"},
	} {
		if step.report != "charge-report" {
			processSyntheticClaims(t, s, step.report, normalizer)
		} else if step.version != "synthetic-claims/1" {
			if _, err = s.Normalize(t.Context(), step.report, step.version, normalizer); err != nil {
				t.Fatal(err)
			}

			if err = s.Process(t.Context(), step.report, step.version); err != nil {
				t.Fatal(err)
			}
		}

		var support int
		if err = s.db.QueryRow(`SELECT count(*) FROM execution_observations WHERE execution_id IS NOT NULL`).
			Scan(&support); err != nil ||
			support != 2 {
			t.Fatal("repeated internal disagreement added activity support", support, err)
		}

		records, err := s.Records(t.Context(), step.report, step.version)
		if err != nil || len(records) != 2 {
			t.Fatal("duplicate report appearances were lost", records, err)
		}

		if changes := snapshotChanges(t, s, baseline); len(changes.Items) != 0 {
			t.Fatal("repeated internal disagreement published", changes.Items)
		}
	}
}

func TestDistinctActivitiesShareOneInstrumentContribution(t *testing.T) {
	checkActivityContributionVolume(t, 3)
}

func TestCompoundInstrumentAssertionsDoNotAccumulateActivitySupport(t *testing.T) {
	s := newStoreWithSyntheticAccount(t)
	base := partialTrade(t).Fields()

	isin, _ := source.NewIdentifier(source.ISIN, "US0000000001", "isin")
	cusip, _ := source.NewIdentifier(source.CUSIP, "000000001", "cusip")

	var baseline application.FinancialSnapshot
	for index, ids := range [][]source.Identifier{{isin}, {cusip}, {isin, cusip}, {cusip, isin}, {isin, cusip}} {
		f := base.Instrument.Fields()
		f.Identifiers = ids
		input := base
		input.Instrument, _ = source.NewInstrument(f)

		trade, err := source.NewTrade(input)
		if err != nil {
			t.Fatal(err)
		}

		report := fmt.Sprint("compound-", index)
		processPartial(t, s, report, trade.Observation())

		if index == 1 {
			baseline, err = snapshotState(t.Context(), s)
			if err != nil {
				t.Fatal(err)
			}
		}

		if index < 2 {
			continue
		}

		if tableCount(t, s, "execution_observations") != 2 || tableCount(t, s, "instrument_claims") != 2 {
			t.Fatal("recombined identifiers added supporting links")
		}

		if changes := snapshotChanges(t, s, baseline); len(changes.Items) != 0 {
			t.Fatal("recombined identifiers published", changes.Items)
		}

		if rows := observationRowsForTest(t, s, "artifact", report); len(rows) != 1 ||
			rows[0].ActivityID == "" || rows[0].Outcome != application.ObservationNoContribution {
			t.Fatal("recombined source appearance was lost", rows)
		}
	}
}

func checkActivityContributionVolume(t *testing.T, count int) {
	t.Helper()
	s := newStoreWithSyntheticAccount(t)

	var rows strings.Builder
	for i := range count {
		rows.WriteString(equityRow("U1", fmt.Sprintf("fill-%d", i), fmt.Sprintf("%d", 100+i)))
	}

	processConfirmationReport(t, s, "many-activities", rows.String())

	view, err := s.Dashboard(t.Context())
	if err != nil || len(view.Executions) != count || len(view.Instruments) != 1 {
		t.Fatal("distinct activities lost", len(view.Executions), len(view.Instruments), err)
	}

	var supports, activities int
	if err = s.db.QueryRow(`SELECT count(*),count(DISTINCT execution_id) FROM execution_observations WHERE execution_id IS NOT NULL`).
		Scan(&supports, &activities); err != nil ||
		supports != count ||
		activities != count {
		t.Fatal("each activity must retain exactly one support", supports, activities, err)
	}

	instrument := view.Instruments[0].ID()

	observations := observationRowsForTest(t, s, "instrument", instrument)
	if len(observations) != 1 || observations[0].Artifact != "many-activities" ||
		observations[0].Outcome != application.ObservationAccepted {
		t.Fatal("instrument support grew with activities", observations)
	}

	if got := tableCount(t, s, "instrument_claims"); got != 1 {
		t.Fatal("redundant instrument links persisted", got)
	}

	for _, activity := range []application.Execution{view.Executions[0], view.Executions[count-1]} {
		if got := observationRowsForTest(t, s, "activity", activity.ID()); len(got) != 1 {
			t.Fatal("activity lost its own first observation", got)
		}
	}

	baseline, err := snapshotState(t.Context(), s)
	if err != nil {
		t.Fatal(err)
	}

	processConfirmationReport(t, s, "repeat-many-activities", "\n"+rows.String())

	if got := tableCount(t, s, "instrument_claims"); got != 1 {
		t.Fatal("repeat retained instrument support", got)
	}

	if changes := snapshotChanges(t, s, baseline); len(changes.Items) != 0 {
		t.Fatal("corroboration published changes", changes.Items)
	}

	records, err := s.Records(t.Context(), "repeat-many-activities", ibkrflex.Version)
	if err != nil || len(records) != count+1 {
		t.Fatal("report occurrences were lost", len(records), err)
	}

	query, _ := application.NewListQuery(
		application.ListOptions{PageSize: 2, Sort: "activity"},
		application.ObservationScope,
	)

	appearances, err := s.ObservationList(t.Context(), "artifact", "repeat-many-activities", query)
	if err != nil || appearances.Total != int64(count+1) || len(appearances.Items) != 2 {
		t.Fatal("report history was reduced to retained support", appearances, err)
	}
}

func TestAnonymousUnderlierContributionsUseIndependentChildHistory(t *testing.T) {
	s := newStoreWithSyntheticAccount(t)
	base := underlierTemplate(t)
	retainUnderlierTrades(t, s, "first-anonymous", underlierTrade(t, base, "first", "", false))

	view, err := s.Dashboard(t.Context())
	if err != nil || len(view.Executions) != 1 {
		t.Fatal(view, err)
	}

	parent := view.Executions[0].Instrument()

	detail, err := s.InstrumentDetail(t.Context(), parent)
	if err != nil || detail.Underlier == "" {
		t.Fatal(detail, err)
	}

	child := detail.Underlier
	retainDecision(t, s, "instrument", child, "ticker", "OPERATOR", "", "set")

	for _, event := range []string{"enrichment", "repeat"} {
		retainUnderlierTrades(t, s, event, underlierTrade(t, base, event, "US0000000001", false))

		parents := observationRowsForTest(t, s, "instrument", parent)
		children := observationRowsForTest(t, s, "instrument", child)
		if len(parents) != 1 || len(children) != 2 {
			t.Fatal("child identifier inflated parent support", parents, children)
		}
	}

	detail, err = s.InstrumentDetail(t.Context(), parent)
	if err != nil || len(detail.Instrument.Reported().Option().Fields().Underlier.Identifiers()) != 1 {
		t.Fatal("parent lost independently retained child metadata", detail, err)
	}

	retainUnderlierTrades(t, s, "qualified-later", underlierTrade(t, base, "qualified", "", true))

	detail, err = s.InstrumentDetail(t.Context(), parent)
	if err != nil || detail.Underlier != child || detail.Instrument.Status().NeedsReview() {
		t.Fatal("later qualification lost automatic identity", detail, err)
	}

	if got := observationRowsForTest(t, s, "instrument", parent); len(got) != 2 {
		t.Fatal("new qualified relationship was not retained", got)
	}

	retained, err := s.InstrumentDetail(t.Context(), child)
	if err != nil || retained.Instrument.Ticker() != "OPERATOR" ||
		len(retained.Instrument.Reported().Identifiers()) != 1 {
		t.Fatal("qualification lost source identifiers or operator choice", retained, err)
	}

	retainDecision(t, s, "instrument", child, "ticker", "", "", "release")

	retained, err = s.InstrumentDetail(t.Context(), child)
	if err != nil || retained.Instrument.Ticker() != "SYN" {
		t.Fatal("release did not restore automatic child terms", retained, err)
	}
}

func TestAnonymousUnderlierConflictsDoNotAccumulateParentSupport(t *testing.T) {
	s := newStoreWithSyntheticAccount(t)
	base := underlierTemplate(t)
	retainUnderlierTrades(t, s, "first", underlierTrade(t, base, "first", "US0000000001", false))

	view, err := s.Dashboard(t.Context())
	if err != nil || len(view.Executions) != 1 {
		t.Fatal(view, err)
	}

	parent := view.Executions[0].Instrument()

	detail, err := s.InstrumentDetail(t.Context(), parent)
	if err != nil {
		t.Fatal(err)
	}

	child := detail.Underlier
	for i, isin := range []string{"US0000000002", "US0000000002", "US0000000003"} {
		event := fmt.Sprint("conflict-", i)
		retainUnderlierTrades(t, s, event, underlierTrade(t, base, event, isin, false))

		if got := observationRowsForTest(t, s, "instrument", parent); len(got) != 1 {
			t.Fatal("child conflict became parent support", got)
		}

		detail, err = s.InstrumentDetail(t.Context(), parent)
		if err != nil || detail.Underlier != child || !detail.Instrument.Status().NeedsReview() {
			t.Fatal("discarded parent link hid child conflict", detail, err)
		}
	}

	if got := observationRowsForTest(t, s, "instrument", child); len(got) != 3 {
		t.Fatal("distinct child conflicts or repeat classification lost", got)
	}
	// A later qualified request must still see the discarded anonymous dispute.
	retainUnderlierTrades(t, s, "qualified", underlierTrade(t, base, "qualified", "US0000000001", true))

	detail, err = s.InstrumentDetail(t.Context(), parent)
	if err != nil || detail.Underlier != child || !detail.Instrument.Status().NeedsReview() {
		t.Fatal("qualification ignored independent child conflicts", detail, err)
	}
}

func TestSubjectContributionsKeepConflictsAndEnrichmentIndependently(t *testing.T) {
	s := newStoreWithSyntheticAccount(t)
	row := strings.Replace(equityRow("U1", "same-event", "100"), ` netCash="-0.000000049324000000123"`, "", 1)
	processConfirmationReport(t, s, "first", row)

	view, err := s.Dashboard(t.Context())
	if err != nil || len(view.Executions) != 1 {
		t.Fatal(view, err)
	}

	id, instrument := view.Executions[0].ID(), view.Executions[0].Instrument()
	retainDecision(t, s, "activity", id, "price", "120", "USD", "set")

	for _, report := range []struct{ id, row string }{
		{"enrichment", strings.Replace(row, "/>", ` netCash="-100"/>`, 1)},
		{"conflict", strings.Replace(row, `price="100"`, `price="101"`, 1)},
		{"conflict-repeat", "\n" + strings.Replace(row, `price="100"`, `price="101"`, 1)},
		{"another-conflict", strings.Replace(row, `price="100"`, `price="102"`, 1)},
		{"source-repeat", "\n" + row},
	} {
		processConfirmationReport(t, s, report.id, report.row)
	}

	observations := observationRowsForTest(t, s, "activity", id)
	if len(observations) != 4 {
		t.Fatal("activity contribution history", observations)
	}

	if got := observationRowsForTest(
		t,
		s,
		"instrument",
		instrument,
	); len(got) != 1 ||
		got[0].Outcome != application.ObservationAccepted {
		t.Fatal("activity disagreements became instrument contributions", got)
	}

	detail, err := s.ExecutionDetail(t.Context(), id)
	if err != nil || detail.Execution.Terms().Fields().Price.Amount() != "120" ||
		detail.Execution.Terms().Fields().NetCash.Amount() != "-100" {
		t.Fatal("source retention affected operator choice or enrichment", detail, err)
	}

	retainDecision(t, s, "activity", id, "price", "", "", "release")
	recalculateFixture(t, s)

	if got := observationRowsForTest(t, s, "activity", id); len(got) != 4 {
		t.Fatal("recalculation changed retained support", got)
	}
}

func TestUnderlierSupportIsIndependentOfParentSupport(t *testing.T) {
	s := newStoreWithSyntheticAccount(t)

	var rows strings.Builder
	for i := range 20 {
		row := strings.NewReplacer(`conid="200"`, fmt.Sprintf(`conid="%d"`, 200+i),
			`execID="option-fill"`, fmt.Sprintf(`execID="fill-%d"`, i),
			`tradeID="option-trade"`, fmt.Sprintf(`tradeID="trade-%d"`, i),
			` underlyingSymbol="SYN"`, "").Replace(syntheticOption)
		rows.WriteString(row)
	}

	processConfirmationReport(t, s, "parents", rows.String())

	view, err := s.Dashboard(t.Context())
	if err != nil || len(view.Executions) != 20 {
		t.Fatal(view, err)
	}

	parent := view.Executions[0].Instrument()

	detail, err := s.InstrumentDetail(t.Context(), parent)
	if err != nil || detail.Underlier == "" {
		t.Fatal(detail, err)
	}

	underlier := detail.Underlier
	if got := observationRowsForTest(t, s, "instrument", underlier); len(got) != 1 {
		t.Fatal("shared underlier retained every parent appearance", got)
	}

	if got := tableCount(t, s, "instrument_underliers"); got != 20 {
		t.Fatal("subject relationships depend on redundant evidence", got)
	}

	processConfirmationReport(t, s, "child-enrichment", syntheticOption)

	if got := observationRowsForTest(t, s, "instrument", underlier); len(got) != 2 {
		t.Fatal("new child information was dropped with parent corroboration", got)
	}

	for _, execution := range view.Executions {
		if got := observationRowsForTest(t, s, "instrument", execution.Instrument()); len(got) != 1 {
			t.Fatal("child details inflated parent support", got)
		}
	}

	dependents, err := s.queries.IdentityDependents(t.Context(), underlier)
	if err != nil || len(dependents) != 21 {
		t.Fatal("underlier update no longer reaches every parent", dependents, err)
	}
}

func TestChargeFormatRefusesPreviousWarehouse(t *testing.T) {
	for _, version := range []string{"wake-observation-contributions-3", "wake-partial-activity-1", "wake-reported-positions-4", "wake-observation-contributions-1", "wake-observation-contributions-2"} {
		t.Run(version, func(t *testing.T) {
			s, path := testStore(t)
			if _, err := s.db.Exec(`UPDATE store_metadata SET version=?`, version); err != nil {
				t.Fatal(err)
			}

			if err := s.Close(); err != nil {
				t.Fatal(err)
			}

			//nolint:gosec // Only reads the disposable warehouse created by testStore.
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}

			store, err := Open(t.Context(), testProfile(path), nil)
			if store != nil || !errors.Is(err, ErrUnsupportedSchema) {
				t.Fatal("old format opened without explicit recreation", store, err)
			}

			//nolint:gosec // Only reads the same disposable warehouse after refused opening.
			after, err := os.ReadFile(path)
			if err != nil || string(before) != string(after) {
				t.Fatal("old warehouse modified by preflight", err)
			}
		})
	}
}

func TestContributionAdmissionRollsBackAndSurvivesRestart(t *testing.T) {
	for _, subject := range []string{"activity", "instrument"} {
		t.Run(subject, func(t *testing.T) {
			s := newStoreWithSyntheticAccount(t)
			row := equityRow("U1", "first", "100")
			processConfirmationReport(t, s, "first", row)

			baseline, err := snapshotState(t.Context(), s)
			if err != nil {
				t.Fatal(err)
			}

			candidate, wantActivities := row+row, 1
			trigger := `CREATE TRIGGER reject_contribution BEFORE DELETE ON execution_observations BEGIN SELECT RAISE(ABORT,'synthetic failure'); END`
			if subject == "instrument" {
				candidate, wantActivities = equityRow("U1", "second", "101"), 2
				trigger = `CREATE TRIGGER reject_contribution BEFORE DELETE ON instrument_claims BEGIN SELECT RAISE(ABORT,'synthetic failure'); END`
			}

			normalizeConfirmationReport(t, s, "candidate", candidate)

			if _, err = s.db.Exec(trigger); err != nil {
				t.Fatal(err)
			}

			if err = s.Process(t.Context(), "candidate", ibkrflex.Version); err == nil {
				t.Fatal("failed contribution write committed")
			}

			complete, err := s.queries.ProcessingComplete(
				t.Context(),
				sqlc.ProcessingCompleteParams{ArtifactID: "candidate", Version: ibkrflex.Version},
			)
			if err != nil || complete || tableCount(t, s, "executions") != 1 ||
				tableCount(t, s, "instrument_claims") != 1 {
				t.Fatal("partial contribution state survived rollback", complete, err)
			}

			if changes := snapshotChanges(t, s, baseline); len(changes.Items) != 0 {
				t.Fatal("rolled-back admission published", changes.Items)
			}

			if _, err = s.db.Exec("DROP TRIGGER reject_contribution"); err != nil {
				t.Fatal(err)
			}

			if err = s.Process(t.Context(), "candidate", ibkrflex.Version); err != nil {
				t.Fatal(err)
			}

			profile := testProfile(s.owners[0].Name())
			if err = s.Close(); err != nil {
				t.Fatal(err)
			}

			s, err = Open(t.Context(), profile, nil)
			if err != nil {
				t.Fatal(err)
			}

			t.Cleanup(func() { _ = s.Close() })

			if err = s.Process(t.Context(), "candidate", ibkrflex.Version); err != nil {
				t.Fatal(err)
			}

			view, err := s.Dashboard(t.Context())
			if err != nil || len(view.Executions) != wantActivities || tableCount(t, s, "instrument_claims") != 1 {
				t.Fatal("retry/restart changed subject support", len(view.Executions), err)
			}
		})
	}
}

func TestInstrumentMergeDoesNotRetainDuplicateSupport(t *testing.T) {
	s := newStoreWithSyntheticAccount(t)
	for _, event := range []string{"first", "second"} {
		processConfirmationReport(
			t,
			s,
			event,
			strings.Replace(equityRow("U1", event, "100"), ` conid="100"`, "", 1),
		)
	}

	view, err := s.Dashboard(t.Context())
	if err != nil || len(view.Instruments) != 2 {
		t.Fatal("anonymous source subjects were combined without evidence", view, err)
	}

	for _, instrument := range view.Instruments {
		applyEquityIdentity(t, s, instrument.ID())
	}

	view, err = s.Dashboard(t.Context())
	if err != nil || len(view.Instruments) != 1 || tableCount(t, s, "instrument_claims") != 1 {
		t.Fatal("identity merge retained duplicate subject support", view, err)
	}

	if got := observationRowsForTest(t, s, "instrument", view.Instruments[0].ID()); len(got) != 1 {
		t.Fatal("merged instrument expanded supporting appearances", got)
	}
}

func TestMergedAnonymousUnderlierRetainsBothParentDependencies(t *testing.T) {
	s := newStoreWithSyntheticAccount(t)
	base := underlierTemplate(t)
	first := underlierTrade(t, base, "first", "US0000000001", false)
	f := first.Fields()
	f.Event, _ = source.NewReference("synthetic", "execution", "U1", "second")
	f.Location = "second"
	i := f.Instrument.Fields()
	i.Reference, _ = source.NewReference("synthetic", "instrument", "", "other-option")
	f.Instrument, _ = source.NewInstrument(i)

	second, err := source.NewTrade(f)
	if err != nil {
		t.Fatal(err)
	}

	retainUnderlierTrades(t, s, "parents", first, second)

	view, err := s.Dashboard(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	parents := []string{}
	children := []string{}
	for _, instrument := range view.Instruments {
		if instrument.Reported().Kind() == source.Option {
			parents = append(parents, instrument.ID())
		} else {
			children = append(children, instrument.ID())
		}
	}

	if len(parents) != 2 || len(children) != 2 {
		t.Fatal("anonymous roots were prematurely combined", parents, children)
	}

	for _, child := range children {
		applyEquityIdentity(t, s, child)
	}

	detail, err := s.InstrumentDetail(t.Context(), parents[0])
	if err != nil {
		t.Fatal(err)
	}

	child := detail.Underlier
	if got := observationRowsForTest(t, s, "instrument", child); len(got) != 1 {
		t.Fatal("merged child retained duplicate support", got)
	}

	retainUnderlierTrades(t, s, "child-dispute", underlierTrade(t, base, "dispute", "US0000000002", false))

	for _, parent := range parents {
		detail, err = s.InstrumentDetail(t.Context(), parent)
		if err != nil || detail.Underlier != child || !detail.Instrument.Status().NeedsReview() ||
			len(observationRowsForTest(t, s, "instrument", parent)) != 1 {
			t.Fatal("merged dependency lost source conflict or inflated parent support", detail, err)
		}
	}
}

func TestCompatibleEnrichmentAndCompetingOptionalClaims(t *testing.T) {
	ctx := context.Background()
	s := newStoreWithSyntheticAccount(t)
	processConfirmationReport(t, s, "baseline", syntheticOption)

	v, err := s.Dashboard(ctx)
	if err != nil {
		t.Fatal(err)
	}

	id := v.Executions[0].ID()
	revision := v.Executions[0].Revision()
	enriched := strings.Replace(
		syntheticOption,
		`multiplier="10"`,
		`multiplier="10" netCash="-2.0000000001"`,
		1,
	)
	processConfirmationReport(t, s, "enrichment", enriched)

	d, err := s.ExecutionDetail(ctx, id)
	if err != nil || d.Execution.Terms().Fields().NetCash.Amount() != "-2.0000000001" ||
		d.Execution.Revision() <= revision {
		t.Fatal("optional enrichment", d, err)
	}

	revision = d.Execution.Revision()

	processConfirmationReport(t, s, "enrichment-repeat", enriched)

	d, err = s.ExecutionDetail(ctx, id)
	if err != nil || d.Execution.Revision() != revision {
		t.Fatal("unchanged replay revised entity", err)
	}

	competing := strings.Replace(enriched, `netCash="-2.0000000001"`, `netCash="-3"`, 1)
	processConfirmationReport(t, s, "competing-enrichment", competing)

	d, err = s.ExecutionDetail(ctx, id)
	if err != nil || !d.Execution.State().NeedsReview() || d.Execution.Terms().Fields().NetCash.Amount() != "" {
		t.Fatal("arrival order selected optional claim", d, err)
	}
}

func TestIdentifierMismatchCannotEnrichAcceptedTrade(t *testing.T) {
	ctx := context.Background()
	s := newStoreWithSyntheticAccount(t)
	processConfirmationReport(t, s, "baseline", equityRow("U1", "same", "100"))
	changed := strings.Replace(equityRow("U1", "same", "100"), `US0000000001`, `US0000000002`, 1)
	processConfirmationReport(t, s, "mismatch", changed)

	v, err := s.Dashboard(ctx)
	if err != nil || len(v.Executions) != 1 || !v.Executions[0].State().NeedsReview() ||
		v.Executions[0].Terms().Fields().Instrument.Identifiers()[0].Value() != "US0000000001" {
		t.Fatal(v, err)
	}
}
