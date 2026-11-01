// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0
package persistence

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"go.trippwill.dev/wake/internal/persistence/sqlite/sqlc"
	"go.trippwill.dev/wake/provider"
	"go.trippwill.dev/wake/source"
)

func TestNestedUnderlierClaimsEnrichSharedRootWithoutReplacingOperatorChoice(t *testing.T) {
	s := coverageStore(t)
	ctx := t.Context()
	missingTicker := strings.Replace(syntheticOption, ` underlyingSymbol="SYN"`, "", 1)
	coverageReport(t, s, "parent-a", missingTicker)

	view, err := s.Dashboard(ctx)
	if err != nil {
		t.Fatal(err)
	}

	parent := view.Executions[0].Instrument()

	detail, err := s.InstrumentDetail(ctx, parent)
	if err != nil || detail.Underlier == "" {
		t.Fatal("initial underlier not linked", detail, err)
	}

	underlier := detail.Underlier
	retainDecision(t, s, "instrument", underlier, "ticker", "OPERATOR", "", "set")

	second := strings.NewReplacer(`conid="200"`, `conid="201"`, `execID="option-fill"`, `execID="second-fill"`, `tradeID="option-trade"`, `tradeID="second-trade"`).
		Replace(syntheticOption)
	coverageReport(t, s, "parent-b", second)

	child, err := s.InstrumentDetail(ctx, underlier)
	if err != nil || child.Instrument.Reported().Ticker() != "SYN" || child.Instrument.Ticker() != "OPERATOR" {
		t.Fatal("nested claim did not enrich retained underlier independently of operator label", child, err)
	}

	view, err = s.Dashboard(ctx)
	if err != nil {
		t.Fatal(err)
	}

	for _, execution := range view.Executions {
		parent, e := s.InstrumentDetail(ctx, execution.Instrument())
		if e != nil || parent.Underlier != underlier {
			t.Fatal("parents lost shared underlier", parent, e)
		}
	}

	retainDecision(t, s, "instrument", underlier, "ticker", "", "", "release")

	child, err = s.InstrumentDetail(ctx, underlier)
	if err != nil || child.Instrument.Ticker() != "SYN" {
		t.Fatal("release did not restore enriched automatic ticker", child, err)
	}
}

func TestTickerOnlyUnderlierGainsReferenceBeneathOperatorOverride(t *testing.T) {
	for name, existing := range map[string]bool{"new-reference": false, "existing-reference": true} {
		t.Run(name, func(t *testing.T) {
			seed := coverageStore(t)
			ctx := t.Context()
			override := strings.Replace(equityRow("U1", "override", "100"), `conid="100"`, `conid="999"`, 1)
			coverageReport(t, seed, "templates", syntheticOption+override)

			template, err := seed.Dashboard(ctx)
			if err != nil {
				t.Fatal(err)
			}

			s := coverageStore(t)
			qualifiedID := ""
			if existing {
				coverageReport(t, s, "existing-qualified", equityRow("U1", "existing-fill", "100"))

				existingView, e := s.Dashboard(ctx)
				if e != nil {
					t.Fatal(e)
				}

				qualifiedID = existingView.Executions[0].Instrument()
			}

			normalizer := func(id string, anonymous bool) provider.Normalizer {
				return claimNormalizer(
					func(ctx context.Context, _ provider.Artifact, claims provider.Claims) (provider.Result, error) {
						for _, execution := range template.Executions {
							fields := execution.Terms().Fields()
							if id == "later-anonymous" {
								var e error

								fields.Event, e = source.NewReference("synthetic", "execution", "U1", id+execution.ID())
								if e != nil {
									return provider.Result{}, e
								}

								fields.EventAliases = nil
							}

							if fields.Instrument.Kind() == source.Option && anonymous {
								instrument := fields.Instrument.Fields()
								option := instrument.Option.Fields()
								underlier := option.Underlier.Fields()
								underlier.Reference = source.Reference{}

								var e error

								option.Underlier, e = source.NewInstrument(underlier)
								if e != nil {
									return provider.Result{}, e
								}

								instrument.Option, e = source.NewOptionTerms(option)
								if e != nil {
									return provider.Result{}, e
								}

								fields.Instrument, e = source.NewInstrument(instrument)
								if e != nil {
									return provider.Result{}, e
								}
							}

							trade, e := source.NewTrade(fields)
							if e != nil {
								return provider.Result{}, e
							}

							if e = claims.PutTrade(ctx, trade); e != nil {
								return provider.Result{}, e
							}
						}

						return provider.NewResult(provider.Counts{Rows: 2, Trades: 2})
					},
				)
			}
			admit := func(id string, anonymous bool) { t.Helper(); coverageClaims(t, s, id, normalizer(id, anonymous)) }
			admit("anonymous-underlier", true)

			view, err := s.Dashboard(ctx)
			if err != nil {
				t.Fatal(err)
			}

			var parent, operator string
			for _, execution := range view.Executions {
				if execution.Terms().Fields().Instrument.Kind() == "option" {
					parent = execution.Instrument()
				} else if execution.Terms().Fields().Instrument.Reference().Value() == "999" {
					operator = execution.Instrument()
				}
			}

			detail, err := s.InstrumentDetail(ctx, parent)
			if err != nil || detail.Underlier == "" {
				t.Fatal(detail, err)
			}

			original := detail.Underlier

			retainDecision(t, s, "instrument", parent, "underlier", operator, "", "set")

			if !existing {
				coverageReport(t, s, "failed-qualification", "")

				if _, err = s.Normalize(
					ctx,
					"failed-qualification",
					"synthetic-claims/1",
					normalizer("failed-qualification", false),
				); err != nil {
					t.Fatal(err)
				}

				baseline, e := snapshotState(ctx, s)
				if e != nil {
					t.Fatal(e)
				}

				bindings := tableCount(t, s, "instrument_claims")
				if _, err = s.db.Exec(
					`CREATE TRIGGER fail_qualification BEFORE UPDATE OF source_key ON instruments BEGIN SELECT RAISE(ABORT,'synthetic qualification failure'); END;`,
				); err != nil {
					t.Fatal(err)
				}

				if err = s.Process(
					ctx,
					"failed-qualification",
					"synthetic-claims/1",
				); err == nil ||
					!strings.Contains(err.Error(), "synthetic qualification failure") {
					t.Fatal("failed identity write was acknowledged", err)
				}

				if _, err = s.db.Exec(`DROP TRIGGER fail_qualification;`); err != nil {
					t.Fatal(err)
				}

				// Fail after qualification has succeeded: the source key must roll back
				// together with the claim bindings and accepted publication.
				if _, err = s.db.Exec(
					`CREATE TRIGGER fail_nested_binding BEFORE INSERT ON instrument_claims WHEN NEW.role='underlier' BEGIN SELECT RAISE(ABORT,'synthetic binding failure'); END;`,
				); err != nil {
					t.Fatal(err)
				}

				if err = s.Process(
					ctx,
					"failed-qualification",
					"synthetic-claims/1",
				); err == nil ||
					!strings.Contains(err.Error(), "synthetic binding failure") {
					t.Fatal("failed nested binding was acknowledged", err)
				}

				if _, err = s.db.Exec(`DROP TRIGGER fail_nested_binding;`); err != nil {
					t.Fatal(err)
				}

				retained, e := s.queries.Instrument(ctx, original)
				if e != nil || retained.SourceKey != anonymousInstrumentKey("retained", parent+"/underlier") {
					t.Fatal("failed binding retained qualified source key", retained.SourceKey, e)
				}
				// Invalid retained input must fail closed rather than be skipped during
				// complete-root calculation or a later processing attempt.
				var (
					claimID   string
					claimData []byte
				)
				if err = s.db.QueryRow(`SELECT c.id,c.data FROM source_claims c JOIN instrument_claims i ON i.claim_id=c.id WHERE i.instrument_id=? AND i.role='underlier' LIMIT 1`, original).
					Scan(&claimID, &claimData); err != nil {
					t.Fatal(err)
				}

				if _, err = s.db.Exec(`UPDATE source_claims SET data=X'00' WHERE id=?`, claimID); err != nil {
					t.Fatal(err)
				}

				if _, err = calculateRoot(ctx, s.queries, parent); err == nil {
					t.Fatal("invalid retained claim was ignored")
				}

				if err = s.Process(ctx, "failed-qualification", "synthetic-claims/1"); err == nil {
					t.Fatal("processing accepted invalid retained input")
				}

				if _, err = s.db.Exec(`UPDATE source_claims SET data=? WHERE id=?`, claimData, claimID); err != nil {
					t.Fatal(err)
				}

				if tableCount(t, s, "instrument_claims") != bindings {
					t.Fatal("failed qualification retained new claim bindings")
				}

				if changes := snapshotChanges(t, s, baseline); len(changes.Items) != 0 {
					t.Fatal("failed qualification published", changes)
				}
			}

			admit("qualified-underlier", false)

			detail, err = s.InstrumentDetail(ctx, parent)
			if err != nil || detail.Underlier != operator {
				t.Fatal("source replaced operator link", detail, err)
			}

			expected := original
			if existing {
				expected = qualifiedID
			}

			child, err := s.InstrumentDetail(ctx, expected)
			if err != nil || child.Instrument.Reported().Reference().Value() != "100" {
				t.Fatal("linked underlier missed qualified reference", child, err)
			}

			if existing {
				anonymous, e := s.InstrumentDetail(ctx, original)
				if e != nil || !anonymous.Instrument.Reported().Reference().IsZero() {
					t.Fatal("distinct existing roots were merged", anonymous, e)
				}
			}

			retainDecision(t, s, "instrument", parent, "underlier", "", "", "release")

			detail, err = s.InstrumentDetail(ctx, parent)
			if err != nil || detail.Underlier != expected {
				t.Fatal("release lost original enriched underlier", detail, err)
			}

			coverageReport(t, s, "direct-underlier", equityRow("U1", "direct-underlier", "100"))

			view, err = s.Dashboard(ctx)
			if err != nil {
				t.Fatal(err)
			}

			for _, execution := range view.Executions {
				if execution.Terms().Fields().Instrument.Reference().Value() == "100" &&
					execution.Instrument() != expected {
					t.Fatal("qualified reference created a second root", execution.Instrument(), original)
				}
			}
			// Later incomplete observations must reuse the enriched binding.
			admit("later-anonymous", true)

			detail, err = s.InstrumentDetail(ctx, parent)
			if err != nil || detail.Underlier != expected {
				t.Fatal("incomplete claim replaced enriched link", detail, err)
			}

			competing := strings.NewReplacer(`underlyingConid="100"`, `underlyingConid="777"`, `execID="option-fill"`, `execID="disputed-fill"`, `tradeID="option-trade"`, `tradeID="disputed-trade"`).
				Replace(syntheticOption)
			coverageReport(t, s, "disputed-reference", competing)

			detail, err = s.InstrumentDetail(ctx, parent)
			if err != nil || !detail.Instrument.Status().NeedsReview() || detail.Underlier != expected {
				t.Fatal("conflicting reference replaced established source link", detail, err)
			}

			child, err = s.InstrumentDetail(ctx, expected)
			if err != nil || child.Instrument.Reported().Reference().Value() != "100" {
				t.Fatal("dispute changed established identity", child, err)
			}
		})
	}
}

func TestFirstBatchParentTermConflictKeepsUndisputedUnderlier(t *testing.T) {
	s := coverageStore(t)
	ctx := t.Context()
	competing := strings.NewReplacer(`execID="option-fill"`, `execID="second-fill"`, `tradeID="option-trade"`, `tradeID="second-trade"`, `multiplier="10"`, `multiplier="100"`).
		Replace(syntheticOption)
	coverageReport(t, s, "first-dispute", syntheticOption+competing)

	view, err := s.Dashboard(ctx)
	if err != nil || len(view.Executions) != 2 {
		t.Fatal(view, err)
	}

	detail, err := s.InstrumentDetail(ctx, view.Executions[0].Instrument())
	if err != nil || detail.Instrument.Status().NeedsReview() || detail.Underlier == "" {
		t.Fatal("parent dispute dropped an undisputed underlier", detail, err)
	}

	child, err := s.InstrumentDetail(ctx, detail.Underlier)
	if err != nil || child.Instrument.Reported().Reference().Value() != "100" {
		t.Fatal("wrong underlier retained", child, err)
	}
}

type rootPlanDB struct {
	*sql.DB
	query string
	args  []any
}

func (db *rootPlanDB) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	db.query = query
	db.args = args

	return db.DB.QueryContext(ctx, query, args...)
}

func TestRootClaimQueriesUseKeyedLookup(t *testing.T) {
	s := coverageStore(t)
	ctx := t.Context()
	coverageReport(t, s, "indexed-root", syntheticOption)

	view, err := s.Dashboard(ctx)
	if err != nil {
		t.Fatal(err)
	}

	recorded := &rootPlanDB{DB: s.db}

	queries := sqlc.New(recorded)
	root := view.Executions[0].Instrument()
	if _, err = queries.RootCalculationInputs(ctx, root); err != nil {
		t.Fatal(err)
	}

	rootQuery, rootArgs := recorded.query, recorded.args

	if _, err = queries.CalculationInputs(
		ctx,
		sql.NullString{String: view.Executions[0].ID(), Valid: true},
	); err != nil {
		t.Fatal(err)
	}

	activityQuery, activityArgs := recorded.query, recorded.args

	if _, err = queries.ArtifactRootIDs(ctx, "indexed-root"); err != nil {
		t.Fatal(err)
	}

	for name, read := range map[string]struct {
		query string
		args  []any
	}{"root": {rootQuery, rootArgs}, "activity": {activityQuery, activityArgs}, "dependents": {recorded.query, recorded.args}} {
		plan, err := s.db.QueryContext(ctx, "EXPLAIN QUERY PLAN "+read.query, read.args...)
		if err != nil {
			t.Fatal(err)
		}

		var steps []string
		for plan.Next() {
			var (
				id, parent, unused int
				detail             string
			)
			if err = plan.Scan(&id, &parent, &unused, &detail); err != nil {
				t.Fatal(err)
			}

			steps = append(steps, detail)
		}

		err = plan.Err()
		_ = plan.Close()

		if err != nil {
			t.Fatal(err)
		}

		text := strings.Join(steps, "\n")
		t.Log(name, text)

		if name == "dependents" &&
			!strings.Contains(
				text,
				"SEARCH parent USING INDEX instrument_claim_occurrence (claim_id=? AND ordinal=?)",
			) {
			t.Fatal("dependent parent lookup is not keyed by source occurrence", text)
		}

		for _, fullScan := range []string{"SCAN p", "SCAN i", "SCAN o", "SCAN source_claims", "SCAN records"} {
			if strings.Contains(text, fullScan) {
				t.Fatal("calculation scans unrelated history", name, text)
			}
		}

		if !strings.Contains(
			text,
			"SEARCH p USING INDEX sqlite_autoindex_claim_processing_1 (claim_id=? AND ordinal=?)",
		) {
			t.Fatal("representative appearance not reached through claim key", text)
		}
	}
}

func underlierTemplate(t *testing.T) source.Trade {
	t.Helper()
	seed := coverageStore(t)
	coverageReport(t, seed, "underlier-template", syntheticOption)

	view, err := seed.Dashboard(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	return view.Executions[0].Terms()
}

func underlierTrade(t *testing.T, base source.Trade, event, isin string, qualified bool) source.Trade {
	t.Helper()

	fields := base.Fields()
	fields.Location = event

	var err error

	fields.Event, err = source.NewReference("synthetic", "execution", "U1", event)
	if err != nil {
		t.Fatal(err)
	}

	fields.EventAliases = nil
	instrument := fields.Instrument.Fields()
	option := instrument.Option.Fields()

	underlier := option.Underlier.Fields()
	if !qualified {
		underlier.Reference = source.Reference{}
	}

	if isin != "" {
		identifier, e := source.NewIdentifier(source.ISIN, isin, "synthetic")
		if e != nil {
			t.Fatal(e)
		}

		underlier.Identifiers = []source.Identifier{identifier}
	}

	option.Underlier, err = source.NewInstrument(underlier)
	if err != nil {
		t.Fatal(err)
	}

	instrument.Option, err = source.NewOptionTerms(option)
	if err != nil {
		t.Fatal(err)
	}

	fields.Instrument, err = source.NewInstrument(instrument)
	if err != nil {
		t.Fatal(err)
	}

	trade, err := source.NewTrade(fields)
	if err != nil {
		t.Fatal(err)
	}

	return trade
}

func retainUnderlierTrades(t *testing.T, s *Store, id string, trades ...source.Trade) {
	t.Helper()
	coverageClaims(
		t,
		s,
		id,
		claimNormalizer(
			func(ctx context.Context, _ provider.Artifact, claims provider.Claims) (provider.Result, error) {
				var counts provider.Counts
				for _, trade := range trades {
					if err := claims.PutTrade(ctx, trade); err != nil {
						return provider.Result{}, err
					}

					counts.Rows++
					counts.Trades++
				}

				return provider.NewResult(counts)
			},
		),
	)
}

func TestConflictingQualifiedUnderlierKeepsEstablishedLink(t *testing.T) {
	for _, variant := range []string{"retained", "target-first", "parent-first"} {
		t.Run(variant, func(t *testing.T) {
			s := coverageStore(t)
			base := underlierTemplate(t)

			target := equityRow("U1", "known-root", "100")
			initial := target
			if variant != "retained" {
				initial = strings.Replace(target, ` isin="US0000000001"`, "", 1)
			}

			coverageReport(t, s, "qualified-with-isin", initial)
			retainUnderlierTrades(
				t,
				s,
				"anonymous-with-other-isin",
				underlierTrade(t, base, "anonymous", "US0000000002", false),
			)

			root, err := s.queries.InstrumentBySource(
				t.Context(),
				referenceKey(base.Fields().Instrument.Reference()),
			)
			if err != nil {
				t.Fatal(err)
			}

			before, err := s.InstrumentDetail(t.Context(), root.ID)
			if err != nil || before.Underlier == "" {
				t.Fatal(before, err)
			}

			batch := []source.Trade{underlierTrade(t, base, "qualified", "", true)}
			if variant != "retained" {
				seed := coverageStore(t)
				coverageReport(t, seed, "target-template", target)

				targetView, e := seed.Dashboard(t.Context())
				if e != nil {
					t.Fatal(e)
				}

				batch = append(batch, targetView.Executions[0].Terms())
				if variant == "target-first" {
					batch[0], batch[1] = batch[1], batch[0]
				}
			}

			retainUnderlierTrades(t, s, "new-qualified-reference", batch...)

			after, err := s.InstrumentDetail(t.Context(), root.ID)
			if err != nil || after.Underlier != before.Underlier || !after.Instrument.Status().NeedsReview() {
				t.Fatal("conflicting qualified identity silently replaced source link", before.Underlier, after, err)
			}
		})
	}
}

func TestMixedUnderlierBatchEnrichesSelectedQualifiedRoot(t *testing.T) {
	for name, variant := range map[string]string{"qualified-first": "", "anonymous-first": "reverse", "historical-identifier": "historical"} {
		t.Run(name, func(t *testing.T) {
			s := coverageStore(t)
			base := underlierTemplate(t)
			coverageReport(
				t,
				s,
				"existing-without-isin",
				strings.Replace(equityRow("U1", "known-root", "100"), ` isin="US0000000001"`, "", 1),
			)

			initialISIN := ""
			if variant == "historical" {
				initialISIN = "US0000000001"
			}

			retainUnderlierTrades(t, s, "initial-anonymous", underlierTrade(t, base, "initial", initialISIN, false))
			qualified := underlierTrade(t, base, "qualified", "", true)
			anonymous := underlierTrade(t, base, "new-isin", "US0000000001", false)

			batch := []source.Trade{qualified, anonymous}
			if variant == "reverse" {
				batch[0], batch[1] = batch[1], batch[0]
			}

			if variant == "historical" {
				batch = []source.Trade{qualified}
			}

			retainUnderlierTrades(t, s, "mixed-batch", batch...)

			root, err := s.queries.InstrumentBySource(
				t.Context(),
				referenceKey(base.Fields().Instrument.Reference()),
			)
			if err != nil {
				t.Fatal(err)
			}

			parent, err := s.InstrumentDetail(t.Context(), root.ID)
			if err != nil || parent.Underlier == "" {
				t.Fatal(parent, err)
			}

			selected, err := s.InstrumentDetail(t.Context(), parent.Underlier)
			if err != nil {
				t.Fatal(err)
			}

			ids := selected.Instrument.Reported().Identifiers()
			if len(ids) != 1 || ids[0].Value() != "US0000000001" {
				t.Fatal("mixed batch lost identifier on selected root", selected.Instrument.Reported())
			}
		})
	}
}

func TestDisputedNewUnderliersDoNotInventAnEmptyRoot(t *testing.T) {
	s := coverageStore(t)
	unknown := strings.NewReplacer(` underlyingConid="100"`, "", ` underlyingSymbol="SYN"`, "").
		Replace(syntheticOption)
	coverageReport(t, s, "unknown-underlier", unknown)

	competing := strings.NewReplacer(`underlyingConid="100"`, `underlyingConid="777"`, `execID="option-fill"`, `execID="other-fill"`, `tradeID="option-trade"`, `tradeID="other-trade"`).
		Replace(syntheticOption)
	coverageReport(t, s, "competing-underliers", syntheticOption+competing)

	view, err := s.Dashboard(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	for _, instrument := range view.Instruments {
		if instrument.Reported().Kind() == "" {
			t.Fatal("dispute invented empty root", instrument)
		}
	}

	detail, err := s.InstrumentDetail(t.Context(), view.Executions[0].Instrument())
	if err != nil || !detail.Instrument.Status().NeedsReview() || detail.Underlier != "" {
		t.Fatal("disputed new link was selected", detail, err)
	}
}

func TestProspectiveUnderlierClaimsAcrossParentsAgreeOnReview(t *testing.T) {
	for _, existing := range []bool{false, true} {
		for _, reverse := range []bool{false, true} {
			for range 2 {
				s := coverageStore(t)
				base := underlierTemplate(t)
				if existing {
					coverageReport(
						t,
						s,
						"existing-without-isin",
						strings.Replace(equityRow("U1", "known-root", "100"), ` isin="US0000000001"`, "", 1),
					)
				}

				retainUnderlierTrades(
					t,
					s,
					"parent-p-anonymous",
					underlierTrade(t, base, "p-initial", "US0000000002", false),
				)

				p, err := s.queries.InstrumentBySource(
					t.Context(),
					referenceKey(base.Fields().Instrument.Reference()),
				)
				if err != nil {
					t.Fatal(err)
				}

				before, err := s.InstrumentDetail(t.Context(), p.ID)
				if err != nil {
					t.Fatal(err)
				}

				other := base.Fields()
				instrument := other.Instrument.Fields()

				instrument.Reference, err = source.NewReference("synthetic", "option", "", "parent-q")
				if err != nil {
					t.Fatal(err)
				}

				other.Instrument, err = source.NewInstrument(instrument)
				if err != nil {
					t.Fatal(err)
				}

				q, err := source.NewTrade(other)
				if err != nil {
					t.Fatal(err)
				}

				batch := []source.Trade{
					underlierTrade(t, base, "p-qualified", "", true),
					underlierTrade(t, q, "q-qualified", "US0000000001", true),
				}
				if reverse {
					batch[0], batch[1] = batch[1], batch[0]
				}

				retainUnderlierTrades(t, s, "two-parents", batch...)

				after, err := s.InstrumentDetail(t.Context(), p.ID)
				if err != nil || after.Underlier != before.Underlier || !after.Instrument.Status().NeedsReview() {
					t.Fatal("prospective conflict moved parent P", after, err)
				}

				target, err := s.queries.InstrumentBySource(
					t.Context(),
					referenceKey(base.Fields().Instrument.Option().Fields().Underlier.Reference()),
				)
				if err != nil {
					t.Fatal(err)
				}

				detail, err := s.InstrumentDetail(t.Context(), target.ID)
				if err != nil || !detail.Instrument.Status().NeedsReview() ||
					len(detail.Instrument.Reported().Identifiers()) != 0 {
					t.Fatal("parent order selected disputed target identity", reverse, detail, err)
				}
			}
		}
	}
}

type rootHistoryReadDB struct {
	*sql.DB
	reads map[string]int
}

func (db *rootHistoryReadDB) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if strings.Contains(query, "-- name: RootCalculationInputs") && len(args) == 1 {
		if id, ok := args[0].(string); ok {
			db.reads[id]++
		}
	}

	return db.DB.QueryContext(ctx, query, args...)
}

func TestSharedUnderlierHistoryIsReadOncePerStablePhase(t *testing.T) {
	s := coverageStore(t)

	var rows strings.Builder
	for n := 0; n < 8; n++ {
		rows.WriteString(
			strings.NewReplacer(`conid="200"`, fmt.Sprintf(`conid="option-%d"`, n), `execID="option-fill"`, fmt.Sprintf(`execID="option-fill-%d"`, n), `tradeID="option-trade"`, fmt.Sprintf(`tradeID="option-trade-%d"`, n)).
				Replace(syntheticOption),
		)
	}

	for n := 0; n < 32; n++ {
		rows.WriteString(equityRow("U1", fmt.Sprintf("history-%d", n), "100"))
	}

	coverageReport(t, s, "shared-history", rows.String())

	view, err := s.Dashboard(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	var (
		parents   []string
		underlier string
	)
	for _, instrument := range view.Instruments {
		if instrument.Reported().Kind() == source.Option {
			parents = append(parents, instrument.ID())
		} else {
			underlier = instrument.ID()
		}
	}

	if len(parents) != 8 || underlier == "" {
		t.Fatal("incomplete history fixture", len(parents), underlier)
	}

	recorded := &rootHistoryReadDB{DB: s.db, reads: map[string]int{}}
	q := sqlc.New(recorded)

	phase := newRootPhase(q)
	if _, err = phase.targets(t.Context(), parents); err != nil {
		t.Fatal(err)
	}

	if recorded.reads[underlier] != 1 {
		t.Fatal("binding phase reread shared history", recorded.reads)
	}

	recorded.reads = map[string]int{}

	phase = newRootPhase(q)
	for _, instrument := range view.Instruments {
		if _, err = phase.calculate(t.Context(), instrument.ID()); err != nil {
			t.Fatal(err)
		}
	}

	if recorded.reads[underlier] != 1 {
		t.Fatal("projection phase reread shared history", recorded.reads)
	}

	for id, count := range recorded.reads {
		if count != 1 {
			t.Fatal("history decoded more than once", id, count)
		}
	}
}

func TestAnonymousIdentifierDisputeReachesItsQualifiedGroup(t *testing.T) {
	for _, existing := range []bool{false, true} {
		s := coverageStore(t)
		base := underlierTemplate(t)
		if existing {
			coverageReport(
				t,
				s,
				"empty-target",
				strings.Replace(equityRow("U1", "known-root", "100"), ` isin="US0000000001"`, "", 1),
			)
		}

		retainUnderlierTrades(t, s, "anonymous-a", underlierTrade(t, base, "anonymous-a", "US0000000002", false))

		root, err := s.queries.InstrumentBySource(t.Context(), referenceKey(base.Fields().Instrument.Reference()))
		if err != nil {
			t.Fatal(err)
		}

		before, err := s.InstrumentDetail(t.Context(), root.ID)
		if err != nil {
			t.Fatal(err)
		}

		retainUnderlierTrades(t, s, "qualified-b", underlierTrade(t, base, "qualified-b", "US0000000001", true))

		after, err := s.InstrumentDetail(t.Context(), root.ID)
		if err != nil || after.Underlier != before.Underlier || !after.Instrument.Status().NeedsReview() {
			t.Fatal("dispute replaced established link", after, err)
		}

		target, err := s.queries.InstrumentBySource(
			t.Context(),
			referenceKey(base.Fields().Instrument.Option().Fields().Underlier.Reference()),
		)
		if err != nil {
			t.Fatal(err)
		}

		detail, err := s.InstrumentDetail(t.Context(), target.ID)
		if err != nil || !detail.Instrument.Status().NeedsReview() ||
			len(detail.Instrument.Reported().Identifiers()) != 0 {
			t.Fatal("anonymous identifier omitted from target dispute", existing, detail, err)
		}
	}
}

func TestLaterUnderlierConflictReprojectsRetainedParents(t *testing.T) {
	s := coverageStore(t)
	base := underlierTemplate(t)
	retainUnderlierTrades(t, s, "parent-p", underlierTrade(t, base, "p-qualified", "US0000000002", true))

	p, err := s.queries.InstrumentBySource(t.Context(), referenceKey(base.Fields().Instrument.Reference()))
	if err != nil {
		t.Fatal(err)
	}

	before, err := s.InstrumentDetail(t.Context(), p.ID)
	if err != nil || before.Instrument.Status().NeedsReview() {
		t.Fatal(before, err)
	}

	fields := base.Fields()
	instrument := fields.Instrument.Fields()

	instrument.Reference, err = source.NewReference("synthetic", "option", "", "later-parent-q")
	if err != nil {
		t.Fatal(err)
	}

	fields.Instrument, err = source.NewInstrument(instrument)
	if err != nil {
		t.Fatal(err)
	}

	other, err := source.NewTrade(fields)
	if err != nil {
		t.Fatal(err)
	}

	retainUnderlierTrades(t, s, "parent-q", underlierTrade(t, other, "q-qualified", "US0000000001", true))

	after, err := s.InstrumentDetail(t.Context(), p.ID)
	if err != nil || !after.Instrument.Status().NeedsReview() || after.Underlier != before.Underlier {
		t.Fatal("retained dependent parent was not reprojected", after, err)
	}
}
