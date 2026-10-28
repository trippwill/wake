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
)

func TestPublishedExplanationsExcludeReleasedOverrides(t *testing.T) {
	for _, kind := range []string{"activity", "instrument"} {
		t.Run(kind, func(t *testing.T) {
			s := coverageStore(t)
			coverageReport(t, s, "released-authority", equityRow("U1", "fill", "100"))

			view, err := s.Dashboard(t.Context())
			if err != nil {
				t.Fatal(err)
			}

			id, readKind, first, second, value1, value2, currency := view.Executions[0].ID(), "activity", "price", "quantity", "101", "2", "USD"
			if kind == "instrument" {
				id, readKind, first, second, value1, value2, currency = view.Executions[0].Instrument(), "instruments", "name", "ticker", "Reviewed name", "REVIEWED", ""
			}

			before, err := s.Explanation(t.Context(), readKind, id)
			if err != nil {
				t.Fatal(err)
			}

			checkpoint, err := s.PublishCheckpoint(t.Context())
			if err != nil {
				t.Fatal(err)
			}

			retainDecision(t, s, kind, id, first, value1, currency, "set")
			retainDecision(t, s, kind, id, second, value2, "", "set")
			retainDecision(t, s, kind, id, first, "", "", "release")

			why, err := s.Explanation(t.Context(), readKind, id)
			if err != nil || len(why.Decisions) != 1 || why.Decisions[0].Scope != second ||
				why.Decisions[0].Action != "set" {
				t.Fatal("released scope remains active", why, err)
			}

			retainDecision(t, s, kind, id, second, "", "", "release")

			why, err = s.Explanation(t.Context(), readKind, id)
			resource, e := s.Resource(t.Context(), readKind, id)
			if err != nil || e != nil || len(why.Decisions) != 0 || why.Selection != before.Selection ||
				why.Revision != resource.Revision {
				t.Fatal("automatic authority not restored", why, resource, err, e)
			}

			history, err := s.CorrectionForm(t.Context(), kind, id)
			if err != nil || len(history.Decisions) != 4 {
				t.Fatal("release lost retained history", history, err)
			}

			transitions := publishedChanges(t, s, checkpoint)
			count := 0
			for _, change := range transitions.Items {
				if change.Resource.Kind == readKind && change.Resource.ID == id {
					count++
				}
			}

			if count != 4 {
				t.Fatal("authority changes did not remain separate committed transitions", count)
			}
		})
	}
}

type endpointCountingDB struct {
	sqlc.DBTX
	endpoints int
	related   string
}

func (db *endpointCountingDB) QueryContext(
	ctx context.Context,
	query string,
	args ...any,
) (*sql.Rows, error) {
	if strings.Contains(query, "-- name: EventLinks ") {
		db.endpoints++
	}

	if strings.Contains(query, "-- name: RelatedSourceCorrections ") {
		db.related = query
	}

	return db.DBTX.QueryContext(ctx, query, args...)
}

func TestPublishingEndpointLookupsIgnoreUnrelatedCorrections(t *testing.T) {
	s := coverageStore(t)
	coverageReport(t, s, "relevant-trade", equityRow("U1", "target", "100"))

	view, err := s.Dashboard(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	id := view.Executions[0].ID()

	var rows strings.Builder
	for i := range 40 {
		rows.WriteString(correctionRow(fmt.Sprintf("unrelated-%d", i), "Ca", fmt.Sprintf("Tmissing-%d", i)))
	}

	rows.WriteString(correctionRow("related", "Ca", "Ttarget"))
	coverageReport(t, s, "correction-history", rows.String())
	counter := &endpointCountingDB{DBTX: s.db}

	resource, why, err := canonicalSnapshot(t.Context(), sqlc.New(counter), "activity", id)
	if err != nil || resource.Activity.State != "cancelled" || len(why.Corrections) != 1 ||
		len(why.Corrections[0].TargetIDs) != 1 ||
		why.Corrections[0].TargetIDs[0] != id {
		t.Fatal("relevant correction meaning lost", resource, why, err)
	}

	if counter.endpoints != 2 {
		t.Fatal("unrelated corrections cause endpoint queries inside publishing", counter.endpoints)
	}
}

func TestPublishingReverseLookupsUseEndpointIndexes(t *testing.T) {
	s := coverageStore(t)

	var trigger string
	if err := s.db.QueryRow(`SELECT sql FROM sqlite_schema WHERE type='trigger' AND name='publish_link'`).
		Scan(&trigger); err != nil {
		t.Fatal(err)
	}

	_, body, ok := strings.Cut(trigger, " BEGIN")
	if !ok {
		t.Fatal("missing trigger body")
	}

	details := ""
	for _, statement := range strings.Split(body, ";") {
		statement = strings.TrimSpace(statement)
		if statement == "" || statement == "END" {
			continue
		}

		statement = strings.ReplaceAll(statement, "NEW.account_id", "'account'")
		statement = strings.ReplaceAll(statement, "NEW.reference", "'synthetic-reference'")

		rows, err := s.db.Query("EXPLAIN QUERY PLAN " + statement)
		if err != nil {
			t.Fatal(err)
		}

		for rows.Next() {
			var (
				id, parent, unused int
				detail             string
			)
			if err = rows.Scan(&id, &parent, &unused, &detail); err != nil {
				t.Fatal(err)
			}

			details += detail + "\n"
		}

		if err = rows.Err(); err != nil {
			t.Fatal(err)
		}

		_ = rows.Close()
	}

	for _, index := range []string{"relationships_target", "corrections_target", "corrections_replacement"} {
		if !strings.Contains(details, index) {
			t.Error("reverse trigger does not use endpoint index", index, details)
		}
	}

	counter := &endpointCountingDB{DBTX: s.db}
	if _, err := sqlc.New(counter).RelatedSourceCorrections(t.Context(), "activity"); err != nil {
		t.Fatal(err)
	}

	rows, err := s.db.Query("EXPLAIN QUERY PLAN "+counter.related, "activity")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()

	details = ""
	for rows.Next() {
		var (
			id, parent, unused int
			detail             string
		)
		if err = rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}

		details += detail + "\n"
	}

	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}

	for _, index := range []string{"event_links_execution", "corrections_target", "corrections_replacement"} {
		if !strings.Contains(details, index) {
			t.Error("activity endpoint lookup does not use endpoint index", index, details)
		}
	}
}
