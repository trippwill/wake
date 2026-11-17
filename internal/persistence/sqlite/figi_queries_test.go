// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"go.trippwill.dev/wake/internal/persistence/sqlite/sqlc"
)

func TestFIGILookupsIgnoreDisjointHistory(t *testing.T) {
	for _, extra := range []int{0, 250} {
		t.Run(fmt.Sprintf("disjoint-aliases=%d", extra), func(t *testing.T) {
			t.Parallel()
			s, _ := testStore(t)
			ctx := t.Context()

			if _, err := s.db.ExecContext(ctx, `
INSERT INTO instruments(id,source_key,name,ticker,kind,reported,reference_status,reference_figi)
VALUES ('z','z','','','equity',X'00','resolved','match'),
       ('b','b','','','equity',X'00','resolved','match'),
       ('a','a','','','equity',X'00','resolved','override'),
       ('alias','alias','','','equity',X'00','resolved','match'),
       ('empty','empty','','','equity',X'00','provisional','');
INSERT INTO instrument_selections(instrument_id,name,ticker,figi,evidence,status)
VALUES ('b','','','match','','resolved'),
       ('a','','','match','','resolved'),
       ('alias','','','match','','resolved'),
       ('empty','','','','','provisional');
INSERT INTO resource_aliases(kind,id,target_id)
VALUES ('instruments','alias','z'), ('listings','a','z');`); err != nil {
				t.Fatal(err)
			}

			if _, err := s.db.ExecContext(ctx, `
WITH RECURSIVE extra(n) AS (SELECT 1 WHERE ?1>0 UNION ALL SELECT n+1 FROM extra WHERE n<?1)
INSERT INTO instruments(id,source_key,name,ticker,kind,reported,reference_status,reference_figi)
SELECT 'other-'||n,'other-'||n,'','','equity',X'00','resolved','other-figi-'||n FROM extra;
INSERT INTO instrument_selections(instrument_id,name,ticker,figi,evidence,status)
SELECT id,'','',reference_figi,'','resolved' FROM instruments WHERE id LIKE 'other-%';
INSERT INTO resource_aliases(kind,id,target_id)
SELECT 'instruments',id,'other-1' FROM instruments WHERE id LIKE 'other-%' AND id<>'other-1';`, extra); err != nil {
				t.Fatal(err)
			}

			recorded := &rootPlanDB{DB: s.db}
			q := sqlc.New(recorded)
			roots, err := q.IdentityRoots(ctx, "match")
			assertFIGIRoots(t, roots, err, []string{"z", "b", "a"})
			assertFIGIQueryPlan(t, recorded, "i")

			competitors, err := q.CompetingFIGI(ctx, sqlc.CompetingFIGIParams{Figi: "match", CurrentID: "b"})
			assertFIGIRoots(t, competitors, err, []string{"a", "z"})
			assertFIGIQueryPlan(t, recorded, "instruments")

			roots, err = q.IdentityRoots(ctx, "")
			assertFIGIRoots(t, roots, err, nil)

			competitors, err = q.CompetingFIGI(ctx, sqlc.CompetingFIGIParams{CurrentID: "b"})
			assertFIGIRoots(t, competitors, err, nil)
		})
	}
}

func assertFIGIRoots(t *testing.T, rows []sqlc.Instrument, err error, want []string) {
	t.Helper()

	if err != nil {
		t.Fatal(err)
	}

	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}

	if !slices.Equal(ids, want) {
		t.Fatalf("got roots %v, want %v", ids, want)
	}
}

func assertFIGIQueryPlan(t *testing.T, db *rootPlanDB, instrumentTable string) {
	t.Helper()

	rows, err := db.DB.QueryContext(t.Context(), "EXPLAIN QUERY PLAN "+db.query, db.args...)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()

	var steps strings.Builder
	for rows.Next() {
		var (
			id, parent, unused int
			detail             string
		)
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}

		fmt.Fprintln(&steps, detail)
	}

	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	plan := steps.String()
	for _, lookup := range []string{
		"SEARCH " + instrumentTable + " USING INDEX sqlite_autoindex_instruments_1 (id=?)",
		"SEARCH projected USING INDEX instrument_figi (reference_figi=?)",
		"SEARCH retained USING INDEX instrument_selections_figi (figi=?)",
		"SEARCH a USING COVERING INDEX sqlite_autoindex_resource_aliases_1 (kind=? AND id=?)",
	} {
		if !strings.Contains(plan, lookup) {
			t.Fatalf("missing keyed lookup %q:\n%s", lookup, plan)
		}
	}
}
