// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/application/calculation"
	"go.trippwill.dev/wake/internal/persistence/sqlite/sqlc"
	"go.trippwill.dev/wake/internal/reference"
	"go.trippwill.dev/wake/source"
)

func TestReferenceRefreshWritesDirectListingCorroborationInEncounterOrder(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		multiple   bool
	}{
		{name: "singleton", want: "venue-a,venue-a,venue-b"},
		{name: "ambiguous", multiple: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			s, result := referenceRefreshFixture(t)
			ctx := t.Context()
			if tc.multiple {
				if err := s.queries.PutListing(ctx, sqlc.PutListingParams{
					ID: "another-listing", InstrumentID: "root", Venue: "OTHER", Ticker: "SYN", Currency: "USD",
				}); err != nil {
					t.Fatal(err)
				}
			}

			if _, err := s.db.ExecContext(ctx, `
CREATE TABLE test_listing_updates(figi TEXT NOT NULL);
CREATE TRIGGER test_record_listing_update AFTER UPDATE OF figi ON listings
BEGIN INSERT INTO test_listing_updates SELECT NEW.figi; END;`); err != nil {
				t.Fatal(err)
			}

			if err := s.ApplyReference(ctx, "root", result); err != nil {
				t.Fatal(err)
			}

			root, err := s.queries.Instrument(ctx, "root")
			if err != nil || root.ReferenceStatus != "resolved" || root.ReferenceFigi != "share" {
				t.Fatal("reference did not resolve the root", root, err)
			}

			var got string
			if err := s.db.QueryRowContext(ctx, `
SELECT coalesce(group_concat(figi), '') FROM (SELECT figi FROM test_listing_updates ORDER BY rowid)`).Scan(&got); err != nil {
				t.Fatal(err)
			}

			if got != tc.want {
				t.Fatalf("listing updates = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestReferenceRefreshDiscardsStaleRevisionAndAlias(t *testing.T) {
	t.Parallel()

	s, result := referenceRefreshFixture(t)
	ctx := t.Context()
	insertReferenceInstrument(t, s, "retired-root", "synthetic-isin")

	if _, err := s.db.ExecContext(ctx, `
UPDATE instruments SET reference_input_revision=2 WHERE id='root';
INSERT INTO resource_aliases(kind,id,target_id) VALUES ('instruments','retired-root','root');`); err != nil {
		t.Fatal(err)
	}

	before := readReferenceRefreshState(t, s)
	for _, tc := range []struct {
		id       string
		revision int64
	}{
		{"root", 1},
		{"retired-root", 2},
	} {
		result.InputRevision = tc.revision

		if err := s.ApplyReference(ctx, tc.id, result); err != nil {
			t.Fatal(err)
		}

		if after := readReferenceRefreshState(t, s); !reflect.DeepEqual(after, before) {
			t.Fatalf("stale lookup %s at revision %d wrote state", tc.id, tc.revision)
		}
	}
}

func TestReferenceRefreshRollsBackWhenPublicationFails(t *testing.T) {
	t.Parallel()

	s, result := referenceRefreshFixture(t)
	ctx := t.Context()
	before := readReferenceRefreshState(t, s)
	if _, err := s.db.ExecContext(ctx, `
CREATE TRIGGER test_reject_reference_publication BEFORE UPDATE ON publishing
BEGIN SELECT RAISE(ABORT,'injected publication failure'); END;`); err != nil {
		t.Fatal(err)
	}

	if err := s.ApplyReference(
		ctx,
		"root",
		result,
	); err == nil ||
		!strings.Contains(err.Error(), "injected publication failure") {
		t.Fatal("wanted injected publication failure, got", err)
	}

	if after := readReferenceRefreshState(t, s); !reflect.DeepEqual(after, before) {
		t.Fatal("failed publication changed the reference refresh state")
	}
}

func referenceRefreshFixture(t *testing.T) (*Store, reference.Result) {
	t.Helper()

	s, _ := testStore(t)
	ctx := t.Context()

	var identifiers []source.Identifier
	for _, item := range []struct {
		kind  source.IdentifierKind
		value string
	}{
		{source.VenueFIGI, "venue-a"},
		{source.VenueFIGI, "composite"},
		{source.ShareClassFIGI, "share"},
		{source.UnclassifiedFIGI, "venue-b"},
	} {
		id, err := source.NewIdentifier(item.kind, item.value, "figi")
		if err != nil {
			t.Fatal(err)
		}

		identifiers = append(identifiers, id)
	}

	instrument, err := source.NewInstrument(source.InstrumentInput{
		Kind: source.Equity, Name: "Reported issuer", Ticker: "SYN", Venue: "TEST", Identifiers: identifiers,
	})
	if err != nil {
		t.Fatal(err)
	}

	wire, err := json.Marshal(instrumentWire(instrument))
	if err != nil {
		t.Fatal(err)
	}

	if err := s.queries.PutInstrumentRoot(ctx, sqlc.PutInstrumentRootParams{
		ID:        "root",
		SourceKey: "root",
		Name:      instrument.Name(),
		Ticker:    instrument.Ticker(),
		Kind:      string(instrument.Kind()),
		Reported:  wire,
		Rule:      calculation.InstrumentRule,
	}); err != nil {
		t.Fatal(err)
	}

	if err := s.queries.PutListing(ctx, sqlc.PutListingParams{
		ID: "listing", InstrumentID: "root", Venue: "TEST", Ticker: "SYN", Currency: "USD",
	}); err != nil {
		t.Fatal(err)
	}

	next := time.Date(2026, 10, 22, 8, 0, 0, 0, time.UTC)
	if err := s.ApplyReference(ctx, "root", reference.Result{Status: "provisional", Next: next}); err != nil {
		t.Fatal(err)
	}

	if err := s.queries.PutReferenceEvidence(ctx, sqlc.PutReferenceEvidenceParams{
		ID:         "reference-evidence",
		Retrieved:  next.Format(time.RFC3339),
		Request:    []byte("[]"),
		Response:   []byte("[]"),
		HttpStatus: 200,
		Headers:    []byte("{}"),
	}); err != nil {
		t.Fatal(err)
	}

	return s, reference.Result{
		Status: "resolved", Evidence: "reference-evidence", Next: next.Add(time.Hour),
		Records: []reference.Record{
			{FIGI: "venue-b", ShareClass: "share", Market: "Equity", Name: "Verified issuer"},
			{FIGI: "composite", Composite: "composite", ShareClass: "share", Market: "Equity"},
			{FIGI: "venue-a", Composite: "composite", ShareClass: "share", Market: "Equity"},
			{FIGI: "share", ShareClass: "share", Market: "Equity"},
			{FIGI: "venue-a", Composite: "composite", ShareClass: "share", Market: "Equity"},
		},
	}
}

type referenceRefreshState struct {
	instrument       sqlc.Instrument
	selection        sqlc.InstrumentSelection
	listing          sqlc.Listing
	snapshot         application.FinancialSnapshot
	deadline         string
	deadlineRevision int64
	attempts         int
	syncToken        string
	grantToken       string
}

func readReferenceRefreshState(t *testing.T, s *Store) referenceRefreshState {
	t.Helper()

	ctx := t.Context()

	var (
		out referenceRefreshState
		err error
	)
	if out.instrument, err = s.queries.Instrument(ctx, "root"); err != nil {
		t.Fatal(err)
	}

	if out.selection, err = s.queries.InstrumentSelection(ctx, "root"); err != nil {
		t.Fatal(err)
	}

	if out.listing, err = s.queries.Listing(ctx, "listing"); err != nil {
		t.Fatal(err)
	}

	if out.snapshot, err = snapshotState(ctx, s); err != nil {
		t.Fatal(err)
	}

	if err := s.db.QueryRowContext(ctx, `
SELECT input_revision,next,(SELECT count(*) FROM instrument_reference_attempts),
       (SELECT token FROM sync.sync_association WHERE singleton=1),
       (SELECT token FROM access.upstream_grant WHERE singleton=1)
FROM sync.reference_deadlines WHERE instrument_id='root'`).Scan(
		&out.deadlineRevision, &out.deadline, &out.attempts, &out.syncToken, &out.grantToken,
	); err != nil {
		t.Fatal(err)
	}

	return out
}
