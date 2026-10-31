// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0
package persistence

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"go.trippwill.dev/wake/internal/persistence/sqlite/sqlc"
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
