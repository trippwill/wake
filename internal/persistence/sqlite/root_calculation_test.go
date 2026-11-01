// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0
package persistence

import (
	"context"
	"database/sql"
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

			admit := func(id string, anonymous bool) {
				t.Helper()
				coverageClaims(
					t,
					s,
					id,
					claimNormalizer(
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
					),
				)
			}
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
	if err != nil || !detail.Instrument.Status().NeedsReview() || detail.Underlier == "" {
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

	for name, read := range map[string]struct {
		query string
		args  []any
	}{"root": {rootQuery, rootArgs}, "activity": {recorded.query, recorded.args}} {
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
