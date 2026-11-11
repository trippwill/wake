// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"testing"

	"go.trippwill.dev/wake/financial"
	"go.trippwill.dev/wake/internal/persistence/sqlite/sqlc"
	"go.trippwill.dev/wake/source"
)

func TestPositionBindingSurvivesRedundantInstrumentSupport(t *testing.T) {
	s := coverageStore(t)
	coverageReport(t, s, "activity-first", equityRow("U1", "fill", "100"))
	acquireRealized(
		t,
		s,
		reportedPositionsXML(
			"2026-11-04",
			"<OpenPositions>"+holdingXML("2026-11-04", "10", "1")+"</OpenPositions>",
		),
	)

	bindings, err := s.queries.PositionBindings(t.Context(), "account")
	if err != nil {
		t.Fatal(err)
	}

	var durable int
	if err = s.db.QueryRow(`SELECT count(*) FROM position_bindings`).Scan(&durable); err != nil {
		t.Fatal(err)
	}

	value := readPositions(t, s)
	reason := ""
	if value.Reason != nil {
		reason = *value.Reason
	}

	if value.Status != "available" {
		t.Fatalf(
			"durable bindings=%d; bindings returned=%d; positions=%s/%s",
			durable,
			len(bindings),
			value.Status,
			reason,
		)
	}

	if durable != 1 || len(bindings) != 1 || tableCount(t, s, "instrument_claims") != 1 {
		t.Fatal("position association added redundant instrument support", durable, bindings)
	}

	baseline, err := snapshotState(t.Context(), s)
	if err != nil {
		t.Fatal(err)
	}

	acquireRealized(
		t,
		s,
		reportedPositionsXML(
			"2026-11-05",
			"<OpenPositions>"+holdingXML("2026-11-05", "20", "2")+"</OpenPositions>",
		),
	)

	after := readPositions(t, s)
	if after.Status != "available" || after.Version == value.Version || len(after.Positions) != 1 ||
		after.Positions[0].Quantity.Value != "20" ||
		after.Positions[0].InstrumentID != value.Positions[0].InstrumentID {
		t.Fatal("holding change required new instrument evidence", after)
	}

	if tableCount(t, s, "instrument_claims") != 1 || len(snapshotChanges(t, s, baseline).Items) != 0 {
		t.Fatal("position change added instrument support or activity publication")
	}
}

func TestPositionReinterpretationRetainsFirstInstrumentAssertion(t *testing.T) {
	s := coverageStore(t)
	account, _ := source.NewReference("ibkr", "account", "", "U1")
	ref, _ := source.NewReference("ibkr", "contract-id", "", "100")
	instrument, _ := source.NewInstrument(
		source.InstrumentInput{Kind: source.Equity, Reference: ref, Name: "Position-only security"},
	)
	quantity, _ := financial.ParseQuantity("1")
	position, _ := source.NewPosition(instrument, quantity, financial.Price{})
	date, _ := financial.ParseDate("2026-11-04")
	f := source.PositionSetInput{
		Location:  "set",
		Account:   account,
		Date:      date,
		Complete:  true,
		Positions: []source.Position{position},
	}
	coverageClaims(t, s, "report", positionSetNormalizer(f))

	first := readPositions(t, s)
	if first.Status != "available" {
		t.Fatal(first)
	}

	for _, version := range []string{"same-interpretation-2", "same-interpretation-3"} {
		if _, err := s.Normalize(t.Context(), "report", version, positionSetNormalizer(f)); err != nil {
			t.Fatal(err)
		}

		if err := s.Process(t.Context(), "report", version); err != nil {
			t.Fatal(err)
		}

		if current := readPositions(t, s); current.Status != "available" || current.Version != first.Version {
			t.Fatal("equivalent interpretation changed the holding", current)
		}
	}

	supports := observationRowsForTest(t, s, "instrument", first.Positions[0].InstrumentID)
	if len(supports) != 1 {
		t.Fatalf("identical instrument assertion kept %d supporting links after reinterpretation", len(supports))
	}

	profile := testProfile(s.owners[0].Name())
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(t.Context(), profile, nil)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = s.Close() })

	if err = s.Process(t.Context(), "report", "same-interpretation-3"); err != nil {
		t.Fatal(err)
	}

	if current := readPositions(
		t,
		s,
	); current.Status != "available" || current.Version != first.Version ||
		tableCount(t, s, "instrument_claims") != 1 {
		t.Fatal("restart or replay lost an independently retained position binding", current)
	}
}

func positionInput(t *testing.T, instrument source.Instrument) source.PositionSetInput {
	t.Helper()

	account, _ := source.NewReference("ibkr", "account", "", "U1")
	quantity, _ := financial.ParseQuantity("1")

	position, err := source.NewPosition(instrument, quantity, financial.Price{})
	if err != nil {
		t.Fatal(err)
	}

	date, _ := financial.ParseDate("2026-11-04")

	return source.PositionSetInput{
		Location:  "set",
		Account:   account,
		Date:      date,
		Complete:  true,
		Positions: []source.Position{position},
	}
}

func reinterpretPosition(t *testing.T, s *Store, report, version string, input source.PositionSetInput) {
	t.Helper()

	if _, err := s.Normalize(t.Context(), report, version, positionSetNormalizer(input)); err != nil {
		t.Fatal(err)
	}

	if err := s.Process(t.Context(), report, version); err != nil {
		t.Fatal(err)
	}
}

func TestPositionAndActivityInstrumentSupportIsIndependent(t *testing.T) {
	base := underlierTemplate(t)
	for _, first := range []string{"activity", "position"} {
		for _, qualified := range []bool{false, true} {
			name := first + "/anonymous"
			if qualified {
				name = first + "/qualified"
			}

			t.Run(name, func(t *testing.T) {
				s := coverageStore(t)
				trade := underlierTrade(t, base, "fill", "", qualified)

				position := func() {
					coverageClaims(t, s, "holding", positionSetNormalizer(positionInput(t, trade.Fields().Instrument)))
				}
				activity := func() { retainUnderlierTrades(t, s, "trade", trade) }
				if first == "activity" {
					activity()
					position()
				} else {
					position()
					activity()
				}

				view := readPositions(t, s)
				if view.Status != "available" || len(view.Positions) != 1 {
					t.Fatal(view)
				}

				detail, err := s.InstrumentDetail(t.Context(), view.Positions[0].InstrumentID)
				if err != nil || detail.Underlier == "" {
					t.Fatal(detail, err)
				}

				if len(observationRowsForTest(t, s, "instrument", detail.Instrument.ID())) != 1 ||
					len(observationRowsForTest(t, s, "instrument", detail.Underlier)) != 1 {
					t.Fatal("source kind changed instrument contribution counts")
				}
			})
		}
	}
}

func TestPositionChildAssertionsRemainCurrentWithoutHistoricalSupportLinks(t *testing.T) {
	s := coverageStore(t)
	base := underlierTemplate(t)
	input := positionInput(t, underlierTrade(t, base, "first", "", false).Fields().Instrument)
	coverageClaims(t, s, "holding", positionSetNormalizer(input))

	first := readPositions(t, s)
	if first.Status != "available" || len(first.Positions) != 1 {
		t.Fatal(first)
	}

	parent := first.Positions[0].InstrumentID

	detail, err := s.InstrumentDetail(t.Context(), parent)
	if err != nil || detail.Underlier == "" {
		t.Fatal(detail, err)
	}

	child := detail.Underlier
	input = positionInput(t, underlierTrade(t, base, "enriched", "US0000000001", false).Fields().Instrument)
	reinterpretPosition(t, s, "holding", "enriched-position", input)
	retainDecision(t, s, "instrument", child, "ticker", "OPERATOR", "", "set")

	baseline, err := snapshotState(t.Context(), s)
	if err != nil {
		t.Fatal(err)
	}

	reinterpretPosition(t, s, "holding", "repeated-enrichment", input)

	if len(observationRowsForTest(t, s, "instrument", parent)) != 1 ||
		len(observationRowsForTest(t, s, "instrument", child)) != 2 ||
		len(snapshotChanges(t, s, baseline).Items) != 0 {
		t.Fatal("reinterpreted child enrichment added support or publication")
	}

	detail, err = s.InstrumentDetail(t.Context(), parent)
	if err != nil || len(detail.Instrument.Reported().Option().Fields().Underlier.Identifiers()) != 1 {
		t.Fatal("child assertions disappeared with their historical representative", detail, err)
	}

	conflict := positionInput(t, underlierTrade(t, base, "conflict", "US0000000002", false).Fields().Instrument)
	conflict.Date, _ = financial.ParseDate("2026-11-03")
	coverageClaims(t, s, "disagreement", positionSetNormalizer(conflict))
	reinterpretPosition(t, s, "disagreement", "repeated-disagreement", conflict)

	if len(observationRowsForTest(t, s, "instrument", parent)) != 1 ||
		len(observationRowsForTest(t, s, "instrument", child)) != 3 {
		t.Fatal("repeated child conflict inflated support")
	}

	if current := readPositions(t, s); current.Status != "unavailable" {
		t.Fatal("identity disagreement was lost", current)
	}

	conflict.Positions = nil
	reinterpretPosition(t, s, "disagreement", "retired-disagreement", conflict)

	if current := readPositions(t, s); current.Status != "available" || len(current.Positions) != 1 {
		t.Fatal("retired child conflict still withheld holding", current)
	}

	if len(observationRowsForTest(t, s, "instrument", child)) != 3 {
		t.Fatal("retirement deleted source history")
	}

	qualified := positionInput(
		t,
		underlierTrade(t, base, "qualified", "US0000000001", true).Fields().Instrument,
	)
	reinterpretPosition(t, s, "holding", "qualified-child", qualified)

	detail, err = s.InstrumentDetail(t.Context(), parent)
	if err != nil || detail.Underlier != child ||
		len(observationRowsForTest(t, s, "instrument", parent)) != 2 ||
		len(observationRowsForTest(t, s, "instrument", child)) != 4 {
		t.Fatal("qualification replaced the source child or lost independent history", detail, err)
	}

	coverageClaims(t, s, "anonymous-still-current", positionSetNormalizer(input))

	detail, err = s.InstrumentDetail(t.Context(), parent)
	if err != nil || detail.Underlier != child ||
		len(observationRowsForTest(t, s, "instrument", parent)) != 2 ||
		len(observationRowsForTest(t, s, "instrument", child)) != 4 ||
		readPositions(t, s).Status != "available" {
		t.Fatal("current anonymous assertion lost its qualified source relationship", detail, err)
	}

	retainDecision(t, s, "instrument", child, "ticker", "", "", "release")

	detail, err = s.InstrumentDetail(t.Context(), child)
	if err != nil || detail.Instrument.Ticker() != "SYN" ||
		len(detail.Instrument.Reported().Identifiers()) != 1 {
		t.Fatal("release did not restore the current source child", detail, err)
	}
}

func TestPositionReappearingAssertionUsesCurrentOccurrences(t *testing.T) {
	s := coverageStore(t)
	ref, _ := source.NewReference("ibkr", "contract-id", "", "100")
	isin, _ := source.NewIdentifier(source.ISIN, "US0000000001", "synthetic")
	rich, _ := source.NewInstrument(
		source.InstrumentInput{Kind: source.Equity, Reference: ref, Identifiers: []source.Identifier{isin}},
	)
	partial, _ := source.NewInstrument(source.InstrumentInput{Kind: source.Equity, Reference: ref})
	input := positionInput(t, rich)
	coverageClaims(t, s, "holding", positionSetNormalizer(input))

	first := readPositions(t, s)
	if first.Status != "available" || len(first.Positions) != 1 {
		t.Fatal(first)
	}

	id := first.Positions[0].InstrumentID

	reinterpretPosition(t, s, "holding", "partial-position", positionInput(t, partial))

	detail, err := s.InstrumentDetail(t.Context(), id)
	if err != nil || len(detail.Instrument.Reported().Identifiers()) != 0 {
		t.Fatal("retired enrichment remained current", detail, err)
	}

	coverageClaims(t, s, "reappearing", positionSetNormalizer(input))

	detail, err = s.InstrumentDetail(t.Context(), id)
	if err != nil || len(detail.Instrument.Reported().Identifiers()) != 1 ||
		len(observationRowsForTest(t, s, "instrument", id)) != 1 {
		t.Fatal("known assertion did not become current independently of support", detail, err)
	}

	bindings, err := s.queries.PositionBindings(t.Context(), "account")
	if err != nil || len(bindings) != 2 {
		t.Fatal(bindings, err)
	}
}

func TestPositionContributionRollbackKeepsPriorInterpretation(t *testing.T) {
	s := coverageStore(t)
	ref, _ := source.NewReference("ibkr", "contract-id", "", "100")
	instrument, _ := source.NewInstrument(source.InstrumentInput{Kind: source.Equity, Reference: ref})
	input := positionInput(t, instrument)
	coverageClaims(t, s, "holding", positionSetNormalizer(input))

	before := readPositions(t, s)
	if _, err := s.Normalize(
		t.Context(),
		"holding",
		"failed-position",
		positionSetNormalizer(input),
	); err != nil {
		t.Fatal(err)
	}

	if _, err := s.db.Exec(
		`CREATE TRIGGER reject_position_contribution BEFORE DELETE ON instrument_claims BEGIN SELECT RAISE(ABORT,'synthetic failure'); END`,
	); err != nil {
		t.Fatal(err)
	}

	if err := s.Process(t.Context(), "holding", "failed-position"); err == nil {
		t.Fatal("failed retention committed")
	}

	complete, err := s.queries.ProcessingComplete(
		t.Context(),
		sqlc.ProcessingCompleteParams{ArtifactID: "holding", Version: "failed-position"},
	)
	if err != nil || complete || tableCount(t, s, "position_bindings") != 1 ||
		tableCount(t, s, "instrument_claims") != 1 {
		t.Fatal("partial bindings or interpretation survived rollback", complete, err)
	}

	if after := readPositions(t, s); after.Version != before.Version || after.Status != "available" {
		t.Fatal(after)
	}

	if _, err = s.db.Exec(`DROP TRIGGER reject_position_contribution`); err != nil {
		t.Fatal(err)
	}

	if err = s.Process(t.Context(), "holding", "failed-position"); err != nil {
		t.Fatal(err)
	}

	if after := readPositions(
		t,
		s,
	); after.Version != before.Version || after.Status != "available" ||
		tableCount(t, s, "instrument_claims") != 1 {
		t.Fatal(after)
	}
}

func TestPositionInitialAnonymousChildConflictIsIndependent(t *testing.T) {
	s := coverageStore(t)
	base := underlierTemplate(t)
	first := positionInput(t, underlierTrade(t, base, "first", "US0000000001", false).Fields().Instrument)
	second := positionInput(t, underlierTrade(t, base, "second", "US0000000002", false).Fields().Instrument)
	first.Positions = append(first.Positions, second.Positions...)
	coverageClaims(t, s, "initial-child-disagreement", positionSetNormalizer(first))

	view, err := s.Dashboard(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	var child string
	for _, instrument := range view.Instruments {
		if instrument.Kind() != string(source.Option) {
			continue
		}

		detail, err := s.InstrumentDetail(t.Context(), instrument.ID())
		if err != nil {
			t.Fatal(err)
		}

		child = detail.Underlier
	}

	if child == "" {
		t.Fatal("missing automatic child relationship")
	}

	detail, err := s.InstrumentDetail(t.Context(), child)
	if err != nil || detail.Instrument.Status() != "conflict" ||
		len(observationRowsForTest(t, s, "instrument", child)) != 2 {
		t.Fatal("first child conflict depended on a projected automatic selection", detail, err)
	}

	if tableCount(t, s, "instrument_claims") != 3 || readPositions(t, s).Status != "unavailable" {
		t.Fatal("initial child disagreement lost independent support")
	}
}

func TestPositionFamilyConflictDoesNotProjectAnEquityUnderlier(t *testing.T) {
	s := coverageStore(t)
	coverageReport(t, s, "equity", equityRow("U1", "fill", "100"))

	ref, _ := source.NewReference("ibkr", "contract-id", "", "100")
	childRef, _ := source.NewReference("ibkr", "contract-id", "", "999")
	child, _ := source.NewInstrument(source.InstrumentInput{Kind: source.Equity, Reference: childRef})
	terms, _ := source.NewOptionTerms(source.OptionInput{Underlier: child})
	option, _ := source.NewInstrument(
		source.InstrumentInput{Kind: source.Option, Reference: ref, Option: terms},
	)
	coverageClaims(t, s, "disputed-option", positionSetNormalizer(positionInput(t, option)))

	root, err := s.queries.InstrumentBySource(t.Context(), referenceKey(ref))
	if err != nil {
		t.Fatal(err)
	}

	detail, err := s.InstrumentDetail(t.Context(), root.ID)
	if err != nil || detail.Instrument.Kind() != string(source.Equity) ||
		detail.Instrument.Status() != "conflict" ||
		detail.Underlier != "" ||
		!detail.Instrument.Reported().Option().IsZero() {
		t.Fatal("family conflict projected an option relationship onto an equity", detail, err)
	}

	if readPositions(t, s).Status != "unavailable" {
		t.Fatal("disputed holding was published")
	}
}

func TestPositionIdentityMergeKeepsBindingsWithoutSupport(t *testing.T) {
	s := coverageStore(t)
	isin, _ := source.NewIdentifier(source.ISIN, "US0000000001", "synthetic")
	instrument, _ := source.NewInstrument(
		source.InstrumentInput{Kind: source.Equity, Ticker: "SYN", Identifiers: []source.Identifier{isin}},
	)
	input := positionInput(t, instrument)
	input.Positions = append(input.Positions, input.Positions...)
	coverageClaims(t, s, "separate-source-identities", positionSetNormalizer(input))

	if readPositions(t, s).Status != "unavailable" {
		t.Fatal("anonymous identities were combined without evidence")
	}

	roots, err := s.queries.Instruments(t.Context())
	if err != nil || len(roots) != 2 {
		t.Fatal(roots, err)
	}

	for _, root := range roots {
		applyEquityIdentity(t, s, root.ID)
	}

	view := readPositions(t, s)
	if view.Status != "available" || len(view.Positions) != 2 ||
		view.Positions[0].InstrumentID != view.Positions[1].InstrumentID ||
		tableCount(t, s, "instrument_claims") != 1 ||
		tableCount(t, s, "position_bindings") != 2 {
		t.Fatal("identity merge lost bindings or retained redundant support", view)
	}
}
