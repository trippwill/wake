// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"go.trippwill.dev/wake/financial"
	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/persistence/sqlite/sqlc"
	"go.trippwill.dev/wake/provider"
	"go.trippwill.dev/wake/source"
)

const partialVersion = "synthetic-partial/1"

func TestPartialUnknownReportingAmountsPreserveTheirDates(t *testing.T) {
	s := coverageStore(t)
	full := partialTrade(t).Observation()
	processPartial(t, s, "baseline", full)

	gainDate, _ := financial.ParseDate("2026-10-24")
	costDate, _ := financial.ParseDate("2026-10-23")
	unknown := partialObservation(
		t,
		full.Fields(),
		partialField(
			t,
			source.ActivityFieldInput{Name: source.FieldRealizedPnL, Unknown: true, ReportedOn: gainDate},
		),
		partialField(
			t,
			source.ActivityFieldInput{Name: source.FieldReportedCostBasis, Unknown: true, ReportedOn: costDate},
		),
	)
	processPartial(t, s, "dated-unknown", unknown)

	rows, err := s.queries.Executions(t.Context())
	if err != nil || len(rows) != 1 {
		t.Fatal(rows, err)
	}

	id := rows[0].ID

	value := realizedActivity(t, s, id).Activity.ReportedRealizedPnL
	if value == nil || value.Amount != nil || value.CostBasis != nil || value.ReportDate == nil ||
		*value.ReportDate != gainDate.String() || value.CostBasisReportDate == nil || *value.CostBasisReportDate != costDate.String() {
		t.Errorf("unknown monetary values erased reporting dates: %#v", value)
	}

	resource := realizedActivity(t, s, id)

	explanation, err := s.Explanation(t.Context(), "activity", id, resource.Revision)
	if err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{"/reported_realized_pnl/amount", "/reported_realized_pnl/cost_basis"} {
		if !slices.ContainsFunc(explanation.Fields, func(detail application.PublishedFieldDetail) bool {
			return detail.Path == path && detail.Reason == application.FieldReportedUnavailable
		}) {
			t.Errorf("explicit unavailable source assertion missing from explanation: %s", path)
		}
	}

	form, err := s.CorrectionForm(t.Context(), "activity", id)
	if err != nil {
		t.Fatal(err)
	}

	found := false
	for _, candidate := range form.Candidates {
		if strings.HasPrefix(candidate.Label, "Partial activity evidence") {
			found = true

			if candidate.Fields["realized_report_date"].Value != gainDate.String() ||
				candidate.Fields["cost_basis_report_date"].Value != costDate.String() {
				t.Error("unknown monetary values erased candidate dates", candidate.Fields)
			}
		}
	}

	if !found {
		t.Fatal("dated unknown source candidate missing")
	}
}

func TestFieldExplanationChangesRevisionWithoutChangingAcceptedValue(t *testing.T) {
	s := coverageStore(t)
	ctx := t.Context()
	full := partialTrade(t).Observation()
	processPartial(t, s, "baseline", full)

	before, err := s.Snapshot(ctx, application.SnapshotCondition{})
	if err != nil {
		t.Fatal(err)
	}

	rows, err := s.queries.Executions(ctx)
	if err != nil || len(rows) != 1 {
		t.Fatal(rows, err)
	}

	id := rows[0].ID
	original := realizedActivity(t, s, id)
	explicit := partialObservation(t, full.Fields(), partialField(t,
		source.ActivityFieldInput{Name: source.FieldSettlementDate, Unknown: true}))
	processPartial(t, s, "explicitly-unavailable", explicit)

	updated := realizedActivity(t, s, id)
	why, err := s.Explanation(ctx, "activity", id, updated.Revision)
	if err != nil || updated.Revision <= original.Revision || updated.Activity.Trade.SettlementDate != nil ||
		!slices.ContainsFunc(why.Fields, func(detail application.PublishedFieldDetail) bool {
			return detail.Path == "/settlement_date" && detail.Reason == application.FieldReportedUnavailable
		}) {
		t.Fatalf("field explanation did not advance with explicit unavailability: %+v %+v %v", updated, why, err)
	}

	after, err := s.Snapshot(ctx, application.MatchingSnapshotVersions(before.Snapshot.Version))
	if err != nil || after.Unchanged || after.Snapshot.Version == before.Snapshot.Version {
		t.Fatalf("explanation-only change did not publish: %+v %v", after, err)
	}
}

func TestFieldExplanationFollowsActiveScalarClears(t *testing.T) {
	s := coverageStore(t)
	f := partialTrade(t).Fields()
	f.At, _ = financial.ParseTimestamp("2026-10-20T12:00:00Z")
	f.SettlementDate, _ = financial.ParseDate("2026-10-21")
	f.NetCash, _ = financial.ParseMoney("-200.0000", f.Price.Currency())

	trade, err := source.NewTrade(f)
	if err != nil {
		t.Fatal(err)
	}

	processPartial(t, s, "baseline", trade.Observation())

	rows, err := s.queries.Executions(t.Context())
	if err != nil || len(rows) != 1 {
		t.Fatal(rows, err)
	}

	id := rows[0].ID
	for _, scope := range []string{"at", "settlement_date", "net_cash"} {
		t.Run(scope, func(t *testing.T) {
			retainDecision(t, s, "activity", id, scope, "", "", "set")
			assertFieldReason(t, s, id, "/"+scope, application.FieldOperatorWithheld)
			retainDecision(t, s, "activity", id, scope, "", "", "release")
			assertFieldReason(t, s, id, "/"+scope, "")
		})
	}
}

func TestFieldExplanationForIncompleteConversionRate(t *testing.T) {
	s := coverageStore(t)
	trade := partialTrade(t).Fields()
	usd := trade.Price.Currency()
	eur, _ := financial.ParseCurrency("EUR")
	sent, _ := source.NewCurrencyAmount(eur, "100")
	received, _ := source.NewCurrencyAmount(usd, "120")

	conversion, err := source.NewConversion(source.ConversionInput{
		Location: trade.Location, Account: trade.Account, Event: trade.Event, EventDistinct: true,
		Date: trade.Date, Sent: sent, Received: received,
	})
	if err != nil {
		t.Fatal(err)
	}

	full := conversion.Observation()
	processPartial(t, s, "baseline", full)

	rows, err := s.queries.Executions(t.Context())
	if err != nil || len(rows) != 1 {
		t.Fatal(rows, err)
	}

	id := rows[0].ID

	assertFieldReason(t, s, id, "/rate", application.FieldNotReported)
	assertFieldReason(t, s, id, "/rate_base", application.FieldNotReported)

	rate, _ := financial.ParsePrice("1.20", usd)
	processPartial(t, s, "incomplete-rate", partialObservation(t, full.Fields(),
		partialField(t, source.ActivityFieldInput{Name: source.FieldRate, Price: rate}),
		partialField(t, source.ActivityFieldInput{Name: source.FieldRateBase, Unknown: true}),
	))
	assertFieldReason(t, s, id, "/rate", application.FieldUnresolved)
	assertFieldReason(t, s, id, "/rate_base", application.FieldReportedUnavailable)

	processPartial(t, s, "rate-base", partialObservation(t, full.Fields(),
		partialField(t, source.ActivityFieldInput{Name: source.FieldRateBase, Currency: eur}),
	))
	assertFieldReason(t, s, id, "/rate", "")
	assertFieldReason(t, s, id, "/rate_base", "")
}

func TestFieldExplanationDistinguishesInstrumentTermConflictsClearsAndOmissions(t *testing.T) {
	for _, family := range []source.InstrumentKind{source.Option, source.Debt} {
		t.Run(string(family), func(t *testing.T) {
			s := coverageStore(t)
			f := partialTrade(t).Fields()
			instrument := f.Instrument.Fields()
			instrument.Kind = family

			term, omitted, values := "multiplier", "expiry", []string{"100", "50"}
			if family == source.Debt {
				term, omitted, values = "coupon_rate", "maturity", []string{"4.125", "4.250"}
			}

			withTerm := func(value string) source.ActivityObservation {
				t.Helper()

				var err error
				if family == source.Option {
					multiplier, _ := financial.ParseQuantity(value)
					instrument.Option, err = source.NewOptionTerms(source.OptionInput{Multiplier: multiplier})
				} else {
					rate, _ := financial.ParseQuantity(value)
					instrument.Debt, err = source.NewDebtTerms(source.DebtInput{CouponRate: rate})
				}

				if err != nil {
					t.Fatal(err)
				}

				f.Instrument, err = source.NewInstrument(instrument)
				if err != nil {
					t.Fatal(err)
				}

				trade, err := source.NewTrade(f)
				if err != nil {
					t.Fatal(err)
				}

				return trade.Observation()
			}

			processPartial(t, s, "baseline", withTerm(""))
			processPartial(t, s, "first-term", withTerm(values[0]))
			processPartial(t, s, "competing-term", withTerm(values[1]))

			rows, err := s.queries.Executions(t.Context())
			if err != nil || len(rows) != 1 {
				t.Fatal(rows, err)
			}

			id := rows[0].ID
			path := "/instrument_terms/" + term
			scope := string(family) + "." + term

			assertFieldReason(t, s, id, path, application.FieldUnresolved)
			assertFieldReason(t, s, id, "/instrument_terms/"+omitted, application.FieldNotReported)

			baseline := observationKey(
				sqlc.RecordValue{ArtifactID: "baseline", Version: partialVersion, Location: f.Location},
			)
			retainDecision(t, s, "activity", id, "evidence/instrument", baseline, "", "set")
			assertFieldReason(t, s, id, path, application.FieldOperatorWithheld)
			retainDecision(t, s, "activity", id, "evidence/instrument", "", "", "release")
			assertFieldReason(t, s, id, path, application.FieldUnresolved)
			retainDecision(t, s, "activity", id, scope, values[0], "", "set")
			assertFieldReason(t, s, id, path, "")
			retainDecision(t, s, "activity", id, scope, "", "", "set")
			assertFieldReason(t, s, id, path, application.FieldOperatorWithheld)
			retainDecision(t, s, "activity", id, scope, "", "", "release")
			assertFieldReason(t, s, id, path, application.FieldUnresolved)
		})
	}
}

func assertFieldReason(t *testing.T, s *Store, id, path string, want application.FieldAbsenceReason) {
	t.Helper()
	resource := realizedActivity(t, s, id)

	why, err := s.Explanation(t.Context(), "activity", id, resource.Revision)
	if err != nil {
		t.Fatal(err)
	}

	var got application.FieldAbsenceReason
	for _, field := range why.Fields {
		if field.Path == path {
			got = field.Reason
			break
		}
	}

	if got != want {
		t.Fatalf("%s absence reason = %q, want %q", path, got, want)
	}
}

func retainPartial(t testing.TB, s *Store, id string, values ...source.ActivityObservation) {
	t.Helper()

	artifact, err := provider.NewArtifact("synthetic-activity", "text/plain", time.Now(), []byte(id))
	if err != nil {
		t.Fatal(err)
	}

	err = s.queries.PutArtifact(
		t.Context(),
		sqlc.PutArtifactParams{
			ID:           id,
			ConnectionID: "synthetic",
			Stream:       "partial",
			Operation:    "acquire",
			Format:       artifact.Format(),
			MediaType:    artifact.MediaType(),
			Retrieved:    artifact.RetrievedAt().Format(time.RFC3339Nano),
			Hash:         artifact.Hash(),
			Content:      artifact.Content(),
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	_, err = s.Normalize(
		t.Context(),
		id,
		partialVersion,
		claimNormalizer(
			func(ctx context.Context, _ provider.Artifact, sink provider.Claims) (provider.Result, error) {
				for _, value := range values {
					if e := sink.PutActivity(ctx, value); e != nil {
						return provider.Result{}, e
					}
				}

				return provider.NewResult(provider.Counts{Rows: uint64(len(values)), Activities: uint64(len(values))})
			},
		),
	)
	if err != nil {
		t.Fatal(err)
	}
}

func processPartial(t testing.TB, s *Store, id string, values ...source.ActivityObservation) {
	t.Helper()
	retainPartial(t, s, id, values...)

	if err := s.Process(t.Context(), id, partialVersion); err != nil {
		t.Fatal(err)
	}
}

func partialField(t testing.TB, f source.ActivityFieldInput) source.ActivityField {
	t.Helper()

	v, err := source.NewActivityField(f)
	if err != nil {
		t.Fatal(err)
	}

	return v
}

func partialObservation(
	t testing.TB,
	subject source.ActivityObservationInput,
	fields ...source.ActivityField,
) source.ActivityObservation {
	t.Helper()

	subject.Fields = fields

	v, err := source.NewActivityObservation(subject)
	if err != nil {
		t.Fatal(err)
	}

	return v
}

func partialTrade(t testing.TB) source.Trade {
	t.Helper()

	account, _ := source.NewReference("ibkr", "account", "", "U1")
	event, _ := source.NewReference("fixture", "execution", "U1", "partial-event")
	ref, _ := source.NewReference("fixture", "instrument", "", "partial-equity")
	instrument, _ := source.NewInstrument(
		source.InstrumentInput{Reference: ref, Kind: source.Equity, Name: "Partial equity", Ticker: "PAR"},
	)
	usd, _ := financial.ParseCurrency("USD")
	price, _ := financial.ParsePrice("100.000000000001", usd)
	q, _ := financial.ParseQuantity("2.00000000000001")
	date, _ := financial.ParseDate("2026-10-20")

	v, err := source.NewTrade(
		source.TradeInput{
			Location:      "row",
			Account:       account,
			Event:         event,
			EventDistinct: true,
			Instrument:    instrument,
			Side:          source.Buy,
			Quantity:      q,
			Price:         price,
			Date:          date,
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	return v
}

func TestPartialActivityCollectiveCompletionAndIndependentSelections(t *testing.T) {
	s := coverageStore(t)
	// The fixture seeds an account directly; publish it before observing activity changes.
	coverageReport(t, s, "account-baseline", "")

	changes, unsubscribe := s.SubscribePublications()
	defer unsubscribe()

	assertPublication := func(want bool) {
		t.Helper()

		got := len(changes) != 0
		if got != want {
			t.Fatalf("accepted publication signal: got %t, want %t", got, want)
		}

		if got {
			<-changes
		}
	}

	full := partialTrade(t).Observation()
	subject := full.Fields()
	eur, _ := financial.ParseCurrency("EUR")
	amount, _ := financial.ParseMoney("0.00000000000000001", eur)
	date, _ := financial.ParseDate("2026-10-24")
	gain := partialObservation(
		t,
		subject,
		partialField(
			t,
			source.ActivityFieldInput{Name: source.FieldRealizedPnL, Money: amount, ReportedOn: date},
		),
	)
	first := partialObservation(t, subject, subject.Fields[:3]...)
	second := partialObservation(t, subject, subject.Fields[3:]...)
	processPartial(t, s, "gain-first", gain)
	processPartial(t, s, "terms-first", first)
	assertPublication(false)

	if count := tableCount(t, s, "executions"); count != 0 {
		t.Fatal("partial evidence fabricated activity", count)
	}

	var pending int
	if err := s.db.QueryRow("SELECT count(*) FROM execution_observations WHERE outcome='incomplete'").
		Scan(&pending); err != nil ||
		pending != 2 {
		t.Fatal("pending claims unavailable", pending, err)
	}

	processPartial(t, s, "terms-second", second)
	assertPublication(true)

	rows, err := s.queries.Executions(t.Context())
	if err != nil || len(rows) != 1 {
		t.Fatal(rows, err)
	}

	id := rows[0].ID

	value := realizedActivity(t, s, id)
	if value.Activity.Trade.Price.Amount != "100.000000000001" ||
		value.Activity.Quantity().Value != "2.00000000000001" ||
		value.Activity.ReportedRealizedPnL.Amount.Amount != amount.Amount() ||
		value.Activity.ReportedRealizedPnL.Amount.Currency != "EUR" ||
		*value.Activity.ReportedRealizedPnL.ReportDate != "2026-10-24" {
		t.Fatal("collective completion lost source facts", value)
	}

	usd, _ := financial.ParseCurrency("USD")
	newPrice, _ := financial.ParsePrice("101.0000", usd)
	newGain, _ := financial.ParseMoney("13.340000000000001", eur)
	priceOnly := partialObservation(
		t,
		subject,
		partialField(
			t,
			source.ActivityFieldInput{Name: source.FieldPrice, Price: newPrice, QuotationBasis: source.PerShare},
		),
	)
	gainOnly := partialObservation(
		t,
		subject,
		partialField(
			t,
			source.ActivityFieldInput{Name: source.FieldRealizedPnL, Money: newGain, ReportedOn: date},
		),
	)
	processPartial(t, s, "price-change", priceOnly)
	assertPublication(true)
	processPartial(t, s, "gain-change", gainOnly)
	assertPublication(true)

	choose := func(field, value string) {
		form, e := s.CorrectionForm(t.Context(), "activity", id)
		if e != nil {
			t.Fatal(e)
		}

		key := ""
		for _, candidate := range form.Candidates {
			if candidate.Fields[field].Value == value {
				key = candidate.Value
			}
		}

		if key == "" {
			t.Fatal("source candidate missing", field)
		}

		retainDecision(t, s, "activity", id, "observation", key, "", "set")
	}
	choose("price", newPrice.Amount())
	assertPublication(true)
	choose("realized_pnl", newGain.Amount())
	assertPublication(true)

	value = realizedActivity(t, s, id)
	if value.Activity.Trade.Price.Amount != newPrice.Amount() ||
		value.Activity.ReportedRealizedPnL.Amount.Amount != newGain.Amount() {
		t.Fatal("independent selections replaced one another", value)
	}

	retainDecision(t, s, "activity", id, "evidence/price", "", "", "release")
	assertPublication(true)

	value = realizedActivity(t, s, id)
	if value.Activity.Trade.Price.Amount != "100.000000000001" ||
		value.Activity.ReportedRealizedPnL.Amount.Amount != newGain.Amount() {
		t.Fatal("field release erased another selection", value)
	}

	before, _ := snapshotState(t.Context(), s)
	processPartial(t, s, "repeat-gain", gainOnly)
	assertPublication(false)

	after, _ := snapshotState(t.Context(), s)
	if before.Version != after.Version || tableCount(t, s, "executions") != 1 {
		t.Fatal("known claim repeated financial publication")
	}
}

func TestPartialActivityCompletionRollsBackAndReplays(t *testing.T) {
	s := coverageStore(t)
	coverageReport(t, s, "account-baseline", "")

	changes, unsubscribe := s.SubscribePublications()
	defer unsubscribe()

	full := partialTrade(t).Observation()
	subject := full.Fields()
	a := partialObservation(t, subject, subject.Fields[:3]...)
	b := partialObservation(t, subject, subject.Fields[3:]...)
	processPartial(t, s, "first", a)
	retainPartial(t, s, "second", b)

	if _, err := s.db.Exec(
		"CREATE TRIGGER reject_partial BEFORE INSERT ON activity_selections BEGIN SELECT RAISE(ABORT,'synthetic failure'); END",
	); err != nil {
		t.Fatal(err)
	}

	if err := s.Process(t.Context(), "second", partialVersion); err == nil {
		t.Fatal("injected projection failure succeeded")
	}

	if tableCount(t, s, "executions") != 0 || tableCount(t, s, "activity_bases") != 0 {
		t.Fatal("failed completion leaked accepted state")
	}

	if len(changes) != 0 {
		t.Fatal("pending or rolled-back completion emitted a publication signal")
	}

	complete, err := s.queries.ProcessingComplete(
		t.Context(),
		sqlc.ProcessingCompleteParams{ArtifactID: "second", Version: partialVersion},
	)
	if err != nil || complete {
		t.Fatal("failed completion acknowledged", err)
	}

	if _, err = s.db.Exec("DROP TRIGGER reject_partial"); err != nil {
		t.Fatal(err)
	}

	if err = s.Process(t.Context(), "second", partialVersion); err != nil {
		t.Fatal(err)
	}

	if tableCount(t, s, "executions") != 1 {
		t.Fatal("replay did not complete exactly once")
	}

	if len(changes) != 1 {
		t.Fatal("committed completion did not notify subscribers")
	}
}

func TestPartialActivityRequiresIdentityAndRejectsWrongUnits(t *testing.T) {
	s := coverageStore(t)
	full := partialTrade(t).Observation()
	processPartial(t, s, "complete", full)
	subject := full.Fields()
	quantity, _ := financial.ParseQuantity("2.00000000000001")
	wrong := partialObservation(
		t,
		subject,
		partialField(
			t,
			source.ActivityFieldInput{Name: source.FieldQuantity, Quantity: quantity, QuantityUnit: "contracts"},
		),
	)
	processPartial(t, s, "wrong-unit", wrong)

	view, err := s.Dashboard(t.Context())
	if err != nil || len(view.Executions) != 1 || !view.Executions[0].State().NeedsReview() ||
		view.Executions[0].Terms().QuantityUnit() != "shares" {
		t.Fatal("unit contradiction was silently merged", view, err)
	}

	subject.Event = source.Reference{}
	subject.EventAliases = nil
	subject.EventDistinct = false
	anonymous := partialObservation(
		t,
		subject,
		partialField(
			t,
			source.ActivityFieldInput{Name: source.FieldQuantity, Quantity: quantity, QuantityUnit: "shares"},
		),
	)
	processPartial(t, s, "unlinked", anonymous)

	if tableCount(t, s, "executions") != 1 {
		t.Fatal("unidentified partial created activity")
	}

	form, err := s.CorrectionForm(t.Context(), "activity", view.Executions[0].ID())
	if err != nil {
		t.Fatal(err)
	}

	for _, candidate := range form.Candidates {
		if candidate.Scope != "observation" {
			t.Fatal("special financial selection path", candidate)
		}
	}
}

func TestPartialFeesAndSettlementUseTheSameReconciliation(t *testing.T) {
	s := coverageStore(t)
	full := partialTrade(t).Observation()
	processPartial(t, s, "execution", full)
	subject := full.Fields()
	usd, _ := financial.ParseCurrency("USD")
	fee, _ := financial.ParseMoney("-0.010000000000000001", usd)
	charge, _ := source.NewCharge(source.ChargeInput{Category: source.Fee, Field: "commission", Total: fee})
	date, _ := financial.ParseDate("2026-10-23")
	late := partialObservation(
		t,
		subject,
		partialField(t, source.ActivityFieldInput{Name: source.FieldCharge, Charge: charge}),
		partialField(t, source.ActivityFieldInput{Name: source.FieldSettlementDate, Date: date}),
	)
	processPartial(t, s, "settlement-and-fee", late)
	rows, _ := s.queries.Executions(t.Context())
	id := rows[0].ID

	value := realizedActivity(t, s, id)
	if value.Activity.Trade.SettlementDate == nil || *value.Activity.Trade.SettlementDate != "2026-10-23" ||
		len(value.Activity.Charges()) != 1 ||
		value.Activity.Charges()[0].Total.Amount != fee.Amount() {
		t.Fatal("late ordinary fields did not enrich", value)
	}

	unknown := partialObservation(
		t,
		subject,
		partialField(t, source.ActivityFieldInput{Name: source.FieldSettlementDate, Unknown: true}),
	)
	processPartial(t, s, "unknown-date", unknown)

	value = realizedActivity(t, s, id)
	if value.Activity.Trade.SettlementDate == nil || *value.Activity.Trade.SettlementDate != "2026-10-23" ||
		len(value.Activity.Charges()) != 1 {
		t.Fatal("unknown or omitted fields erased evidence", value)
	}

	form, err := s.CorrectionForm(t.Context(), "activity", id)
	if err != nil {
		t.Fatal(err)
	}

	choice := ""
	for _, candidate := range form.Candidates {
		if candidate.Fields["charge/commission"].Value == fee.Amount() {
			choice = candidate.Value
		}
	}

	if choice == "" {
		t.Fatal("ordinary partial evidence is not selectable")
	}

	retainDecision(t, s, "activity", id, "observation", choice, "", "set")

	value = realizedActivity(t, s, id)
	if value.Activity.Trade.Price.Amount != "100.000000000001" ||
		value.Activity.Quantity().Value != "2.00000000000001" {
		t.Fatal("choosing partial fees replaced complete trade terms", value)
	}
}

func TestPartialAmbiguousAliasPreservesEstablishedAssociation(t *testing.T) {
	s := coverageStore(t)
	coverageReport(t, s, "first", equityRow("U1", "fill", "100"))

	body := strings.ReplaceAll(realizedReport("fill", "0.0000", "-1"), `ibExecID="fill"`, ``)
	acquireRealized(t, s, body)
	rows, _ := s.queries.Executions(t.Context())
	id := rows[0].ID
	other := strings.ReplaceAll(equityRow("U1", "other", "100"), `tradeID="Tother"`, `tradeID="Tfill"`)
	coverageReport(t, s, "second", other)

	value := realizedActivity(t, s, id)
	if value.Activity.ReportedRealizedPnL == nil || value.Activity.ReportedRealizedPnL.Amount == nil ||
		value.Activity.ReportedRealizedPnL.Amount.Amount != "0.0000" ||
		!value.Activity.ReportedRealizedPnL.Conflict ||
		tableCount(t, s, "executions") != 2 {
		t.Fatal("ambiguity moved or erased established financial assertions", value)
	}
}

func TestPartialInterpretationVersionReusesEvidenceAndPreservesDecisions(t *testing.T) {
	s := coverageStore(t)
	full := partialTrade(t).Observation()
	processPartial(t, s, "retained", full)
	rows, _ := s.queries.Executions(t.Context())
	id := rows[0].ID
	retainDecision(t, s, "activity", id, "price", "99.0000000001", "USD", "set")
	before, _ := snapshotState(t.Context(), s)
	count := tableCount(t, s, "artifacts")
	usd, _ := financial.ParseCurrency("USD")
	money, _ := financial.ParseMoney("-2.0000000000001", usd)
	f := full.Fields()
	f.Fields = append(
		f.Fields,
		partialField(t, source.ActivityFieldInput{Name: source.FieldNetCash, Money: money}),
	)

	reinterpreted, err := source.NewActivityObservation(f)
	if err != nil {
		t.Fatal(err)
	}

	_, err = s.Normalize(
		t.Context(),
		"retained",
		"synthetic-partial/2",
		claimNormalizer(
			func(ctx context.Context, _ provider.Artifact, sink provider.Claims) (provider.Result, error) {
				if e := sink.PutActivity(ctx, reinterpreted); e != nil {
					return provider.Result{}, e
				}

				return provider.NewResult(provider.Counts{Rows: 1, Activities: 1})
			},
		),
	)
	if err != nil {
		t.Fatal(err)
	}

	if err = s.Process(t.Context(), "retained", "synthetic-partial/2"); err != nil {
		t.Fatal(err)
	}

	value := realizedActivity(t, s, id)
	after, _ := snapshotState(t.Context(), s)
	if value.Activity.Trade.Price.Amount != "99.0000000001" ||
		value.Activity.Trade.NetCash.Amount != "-2.0000000000001" ||
		after.Version == before.Version ||
		tableCount(t, s, "artifacts") != count {
		t.Fatalf(
			"interpretation result: price=%s net=%s before=%s after=%s artifacts=%d expected=%d",
			value.Activity.Trade.Price.Amount,
			value.Activity.Trade.NetCash.Amount,
			before.Version,
			after.Version,
			tableCount(t, s, "artifacts"),
			count,
		)
	}

	if err = s.Process(t.Context(), "retained", "synthetic-partial/2"); err != nil {
		t.Fatal(err)
	}

	again, _ := snapshotState(t.Context(), s)
	if again.Version != after.Version {
		t.Fatal("interpretation replay republished")
	}
}

func TestPartialSharedAliasCannotAssembleDifferentDistinctFills(t *testing.T) {
	s := coverageStore(t)
	full := partialTrade(t).Observation()
	a := full.Fields()
	alias, _ := source.NewReference("fixture", "group", "U1", "shared")
	a.EventAliases = []source.Reference{alias}
	a.Location = "a"
	b := a
	b.Event, _ = source.NewReference("fixture", "execution", "U1", "different")
	b.Location = "b"
	group := a
	group.Event = alias
	group.EventAliases = nil
	group.EventDistinct = false
	group.Location = "group"
	usd, _ := financial.ParseCurrency("USD")
	money, _ := financial.ParseMoney("1.00", usd)
	first := partialObservation(t, a, a.Fields[:3]...)
	second := partialObservation(t, b, b.Fields[3:]...)
	ambiguous := partialObservation(
		t,
		group,
		partialField(t, source.ActivityFieldInput{Name: source.FieldRealizedPnL, Money: money}),
	)
	processPartial(t, s, "ambiguous-parts", ambiguous, first, second)

	if tableCount(t, s, "executions") != 0 {
		t.Fatal("shared alias assembled terms from different distinct events")
	}

	finish := a
	finish.Location = "finish-a"
	processPartial(t, s, "finish-a", partialObservation(t, finish, finish.Fields[3:]...))

	rows, err := s.queries.Executions(t.Context())
	if err != nil || len(rows) != 1 {
		t.Fatal(rows, err)
	}

	value := realizedActivity(t, s, rows[0].ID)
	if value.Activity.ReportedRealizedPnL != nil {
		t.Fatal("ambiguous group result was assigned to one fill", value)
	}

	finish = b
	finish.Location = "finish-b"
	processPartial(t, s, "finish-b", partialObservation(t, finish, finish.Fields[:3]...))

	if tableCount(t, s, "executions") != 2 {
		t.Fatal("independent partial fills did not complete separately")
	}

	var outcome string
	if err = s.db.QueryRow("SELECT outcome FROM execution_observations WHERE artifact_id='ambiguous-parts' AND location='group'").
		Scan(&outcome); err != nil ||
		outcome != "native-review" {
		t.Fatal("ambiguous group was not retained for identity review", outcome, err)
	}
}
