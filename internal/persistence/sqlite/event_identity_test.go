// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"testing"

	"go.trippwill.dev/wake/internal/persistence/sqlite/sqlc"
	"go.trippwill.dev/wake/source"
)

func TestEventMatchingUsesExplicitQualificationWithOpaqueNativeKinds(t *testing.T) {
	store, _ := testStore(t)

	_, err := store.db.Exec(`INSERT INTO connections(id,provider) VALUES('source','synthetic');
 INSERT INTO artifacts(id,connection_id,stream,operation,format,media_type,retrieved,hash,content) VALUES('report','source','statements','discover','synthetic','text/plain','2026-10-25T12:00:00Z','synthetic',X'00');
 INSERT INTO accounts(id,singleton,name,currency,connection_id,provider,stream,authority,kind,scope,value,artifact_id) VALUES('account',1,'Synthetic','','source','synthetic','statements','external','portfolio','','portfolio','report');
 INSERT INTO executions(id,account_id,accepted,status,rule) VALUES('first','account',X'00','accepted','synthetic'),('second','account',X'00','accepted','synthetic');`)
	if err != nil {
		t.Fatal(err)
	}

	first, _ := source.NewReference("external", "opaque-ticket", "portfolio", "one")
	second, _ := source.NewReference("external", "opaque-ticket", "portfolio", "two")
	third, _ := source.NewReference("external", "opaque-ticket", "portfolio", "three")
	shared, _ := source.NewReference("external", "opaque-allocation", "portfolio", "allocation")
	put := func(ref source.Reference, id string, distinct bool) {
		t.Helper()

		if err := store.queries.PutEventLink(
			t.Context(),
			sqlc.PutEventLinkParams{
				AccountID:     "account",
				Reference:     referenceKey(ref),
				ExecutionID:   id,
				DistinctEvent: boolInt(distinct),
			},
		); err != nil {
			t.Fatal(err)
		}
	}
	put(first, "first", true)
	put(shared, "first", false)

	id, ambiguous, err := matchReferences(
		t.Context(),
		store.queries,
		"account",
		first,
		[]source.Reference{shared},
		true,
	)
	if err != nil || ambiguous || id != "first" {
		t.Fatal("qualified primary match lost", id, ambiguous, err)
	}

	id, ambiguous, err = matchReferences(
		t.Context(),
		store.queries,
		"account",
		second,
		[]source.Reference{shared},
		true,
	)
	if err != nil || ambiguous || id != "" {
		t.Fatal("shared alias collapsed distinct fills", id, ambiguous, err)
	}

	put(second, "second", true)
	put(shared, "second", false)

	id, ambiguous, err = matchReferences(
		t.Context(),
		store.queries,
		"account",
		third,
		[]source.Reference{shared},
		false,
	)
	if err != nil || !ambiguous || id != "" {
		t.Fatal("unqualified reference selected arbitrary fill", id, ambiguous, err)
	}
}
