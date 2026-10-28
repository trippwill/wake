// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"context"
	"crypto/rand"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/reference"
)

func publishedChanges(t *testing.T, s *Store, cursor string) application.ChangePage {
	t.Helper()

	out, err := s.Changes(context.Background(), cursor, "", 500)
	if err != nil {
		t.Fatal(err)
	}

	return out
}

func TestPublishingAtomicCommitReplayAndDisconnectedTransitions(t *testing.T) {
	ctx := context.Background()
	s := coverageStore(t)

	checkpoint, err := s.PublishCheckpoint(ctx)
	if err != nil {
		t.Fatal(err)
	}

	coverageReport(
		t,
		s,
		"first",
		equityRow("U1", "original", "100.0000")+syntheticOption+syntheticDebt+syntheticFX,
	)

	initial := publishedChanges(t, s, checkpoint)
	if len(initial.Items) < 8 {
		t.Fatal("missing published resources", len(initial.Items))
	}

	page, err := s.Resources(ctx, "activity", "", 500)
	if err != nil || len(page.Items) != 4 {
		t.Fatal(page, err)
	}

	var id string
	for _, r := range page.Items {
		if r.Activity.Price != nil && r.Activity.Price.Amount == "100.0000" {
			id = r.ID
			if r.Activity.At == nil || *r.Activity.At != "2026-10-20T13:30:00.120Z" ||
				r.Activity.Quantity.Value != "0.000000000123" {
				t.Fatal("precision lost", r)
			}
		}
	}

	if id == "" {
		t.Fatalf("exact price missing: %+v", page.Items)
	}

	coverageReport(
		t,
		s,
		"repeat",
		equityRow("U1", "original", "100.0000")+syntheticOption+syntheticDebt+syntheticFX,
	)
	coverageReport(t, s, "scale-only", equityRow("U1", "original", "100.00"))

	if got := publishedChanges(t, s, initial.Cursor); len(got.Items) != 0 {
		t.Fatal("unchanged overlap published", got.Items)
	}

	before, err := s.Resource(ctx, "activity", id)
	if err != nil {
		t.Fatal(err)
	}
	// A decision changing authority must publish even when its value already matches.
	d := retainDecision(t, s, "activity", id, "price", "100.0000", "USD", "set")
	if err = s.SaveDecision(ctx, d, "admin/synthetic-session", time.Now()); err != nil {
		t.Fatal(err)
	}

	retainDecision(t, s, "activity", id, "price", "101.000000000001", "USD", "set")
	retainDecision(t, s, "activity", id, "price", "100.0000", "USD", "release")
	coverageReport(t, s, "conflict", equityRow("U1", "original", "102"))
	coverageReport(
		t,
		s,
		"cancel",
		`<TradeConfirm accountId="U1" assetCategory="STK" transactionType="ExchTrade" levelOfDetail="EXECUTION" origTradeID="Toriginal" code="Ca"/>`,
	)

	transitions := publishedChanges(t, s, initial.Cursor)
	states := []application.CanonicalResource{}
	for _, c := range transitions.Items {
		if c.Resource.ID == id {
			states = append(states, c.Resource)
		}
	}

	if len(states) != 5 {
		t.Fatal("committed transitions collapsed or submission duplicated", len(states))
	}

	for i, r := range states {
		if r.Revision <= before.Revision {
			t.Fatal("revision did not advance")
		}

		if i > 0 && r.Revision <= states[i-1].Revision {
			t.Fatal("unordered revisions")
		}
	}

	if states[1].Activity.Price.Amount != "101.000000000001" || !states[3].Activity.Conflict ||
		states[4].Activity.State != "cancelled" {
		t.Fatal("lost correction/conflict states", states)
	}

	coverageReport(
		t,
		s,
		"cancel-overlap",
		`<TradeConfirm accountId="U1" assetCategory="STK" transactionType="ExchTrade" levelOfDetail="EXECUTION" origTradeID="Toriginal" code="Ca"/>`,
	)

	if len(publishedChanges(t, s, transitions.Cursor).Items) != 0 {
		t.Fatal("repeated source instruction published")
	}

	why, err := s.Explanation(ctx, "activity", id)
	if err != nil || why.Revision != states[4].Revision || len(why.Corrections) != 1 {
		t.Fatal("explanation revision", why, err)
	}
	// A failed publisher must roll back both the accepted write and its journal.
	if _, err = s.db.Exec(
		`CREATE TRIGGER reject_journal BEFORE INSERT ON published_changes BEGIN SELECT RAISE(ABORT,'injected'); END`,
	); err != nil {
		t.Fatal(err)
	}

	form, err := s.CorrectionForm(ctx, "activity", id)
	if err != nil {
		t.Fatal(err)
	}

	command, err := application.NewDecision(
		application.DecisionInput{
			TargetKind:       "activity",
			TargetID:         id,
			Scope:            "price",
			Text:             "999",
			Currency:         "USD",
			Action:           "set",
			Submission:       rand.Text(),
			ExpectedRevision: form.Revision,
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	if err = s.SaveDecision(ctx, command, "test", time.Now()); err == nil {
		t.Fatal("journal fault acknowledged")
	}

	after, err := s.Resource(ctx, "activity", id)
	if err != nil || after.Revision != states[4].Revision || after.Activity.Price.Amount == "999" {
		t.Fatal("partial accepted write", err)
	}

	if len(publishedChanges(t, s, transitions.Cursor).Items) != 0 {
		t.Fatal("rolled back journal visible")
	}
}

func TestPublishingCheckpointPaginationHighWaterAndRecovery(t *testing.T) {
	ctx := context.Background()
	s := coverageStore(t)
	coverageReport(t, s, "first", syntheticOption+syntheticDebt+syntheticFX)

	checkpoint, err := s.PublishCheckpoint(ctx)
	if err != nil {
		t.Fatal(err)
	}

	page, err := s.Resources(ctx, "activity", "", 1)
	if err != nil || page.Next == nil {
		t.Fatal(page, err)
	}
	// Change an already scanned resource while the consumer scans later pages.
	scanCursor := *page.Next

	first := page.Items[0]
	scope, text, currency := "price", "2.0000", "USD"
	if first.Activity.Family == "conversion" {
		scope, text, currency = "sent_amount", "42.0000", ""
	}
	// All selected activity families expose a quantity/amount correction.
	if first.Activity.Family == "conversion" {
		scope = "sent_amount"
	}

	form, err := s.CorrectionForm(ctx, "activity", first.ID)
	if err != nil {
		t.Fatal(err)
	}

	if first.Activity.Family == "conversion" {
		for _, field := range form.Fields {
			if strings.Contains(field, "sent") && !strings.Contains(field, "currency") {
				scope = field
				break
			}
		}
	}

	retainDecision(t, s, "activity", first.ID, scope, text, currency, "set")

	seen := map[string]bool{first.ID: true}
	for page.Next != nil {
		next, e := s.Resources(ctx, "activity", *page.Next, 1)
		if e != nil {
			t.Fatal(e)
		}

		for _, r := range next.Items {
			if seen[r.ID] {
				t.Fatal("duplicate page")
			}

			seen[r.ID] = true
		}

		page = next
	}

	if len(seen) != 3 {
		t.Fatal("scan lost resources")
	}

	replay := publishedChanges(t, s, checkpoint)
	if len(replay.Items) == 0 {
		t.Fatal("scan missed concurrent update")
	}
	// Each pass keeps a finite high-water mark despite subsequent writes.
	retainDecision(t, s, "activity", first.ID, scope, "43.0000", currency, "set")

	bounded, err := s.Changes(ctx, checkpoint, replay.Through, 1)
	if err != nil {
		t.Fatal(err)
	}

	if bounded.Cursor != replay.Cursor || bounded.More {
		t.Fatal("pass high water moved")
	}
	// Restart and both companion losses preserve warehouse cursor identity.
	profile := testProfile(s.owners[0].Name())
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{profile.ConnectionsPath, profile.OperationalPath} {
		if err = os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}

	reopened, err := Open(ctx, profile, nil)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = reopened.Close() }()

	if _, err = reopened.Changes(ctx, replay.Cursor, "", 500); err != nil {
		t.Fatal("companion recovery invalidated cursor", err)
	}

	old, err := reopened.PublishCheckpoint(ctx)
	if err != nil {
		t.Fatal(err)
	}

	job, err := reopened.StartRebuild(ctx, rand.Text(), "test", time.Now())
	if err != nil {
		t.Fatal(err)
	}

	if _, err = reopened.Changes(ctx, old, "", 5); !errors.Is(err, application.ErrRebuilding) {
		t.Fatal("read during rebuild", err)
	}

	if _, err = reopened.db.Exec(`UPDATE recovery_jobs SET status='failed' WHERE id=?`, job); err != nil {
		t.Fatal(err)
	}

	if _, err = reopened.PublishCheckpoint(ctx); !errors.Is(err, application.ErrRebuilding) {
		t.Fatal("failed rebuild exposed", err)
	}

	var generation string
	if err = reopened.db.QueryRow(`SELECT generation FROM publishing`).Scan(&generation); err != nil {
		t.Fatal(err)
	}

	if err = reopened.RetryRecovery(ctx, job); err != nil {
		t.Fatal(err)
	}

	for n := 0; n < 100; n++ {
		changed, e := reopened.RunRecovery(ctx)
		if e != nil {
			t.Fatal(e)
		}

		if !changed {
			break
		}

		if n == 99 {
			t.Fatal("rebuild stalled")
		}
	}

	var afterGeneration string
	if err = reopened.db.QueryRow(`SELECT generation FROM publishing`).
		Scan(&afterGeneration); err != nil ||
		generation != afterGeneration {
		t.Fatal("retry rotated generation", err)
	}

	if _, err = reopened.Changes(ctx, old, "", 10); !errors.Is(err, application.ErrGeneration) {
		t.Fatal("old cursor accepted", err)
	}

	if _, err = reopened.Resources(ctx, "activity", scanCursor, 1); !errors.Is(err, application.ErrGeneration) {
		t.Fatal("scan generation survived rebuild", err)
	}

	if _, err = reopened.PublishCheckpoint(ctx); err != nil {
		t.Fatal("fresh bootstrap unavailable", err)
	}
}

func TestPublishingReferenceRevisions(t *testing.T) {
	ctx := context.Background()
	s := coverageStore(t)
	coverageReport(t, s, "first", syntheticOption)

	page, err := s.Resources(ctx, "instruments", "", 500)
	if err != nil {
		t.Fatal(err)
	}

	checkpoint, err := s.PublishCheckpoint(ctx)
	if err != nil {
		t.Fatal(err)
	}

	for _, r := range page.Items {
		if err = s.ApplyReference(
			ctx,
			r.ID,
			reference.Result{Status: "no-match", Next: time.Now().Add(time.Hour)},
		); err != nil {
			t.Fatal(err)
		}
	}

	changes := publishedChanges(t, s, checkpoint)
	if len(changes.Items) != len(page.Items) {
		t.Fatal("reference changes unpublished", len(changes.Items))
	}
}

func TestPublishingCursorClassificationAndWarehouseRecreation(t *testing.T) {
	ctx := context.Background()
	s := coverageStore(t)
	coverageReport(t, s, "first", syntheticOption)

	checkpoint, err := s.PublishCheckpoint(ctx)
	if err != nil {
		t.Fatal(err)
	}

	for _, bad := range []string{"", "1", "%%", checkpoint + "=", strings.Repeat("A", 2049)} {
		if _, err = s.Changes(ctx, bad, "", 1); !errors.Is(err, application.ErrCursor) {
			t.Fatal(bad, err)
		}
	}

	page, err := s.Resources(ctx, "instruments", "", 1)
	if err != nil || page.Next == nil {
		t.Fatal(page, err)
	}

	if _, err = s.Changes(ctx, *page.Next, "", 1); !errors.Is(err, application.ErrCursor) {
		t.Fatal("page cursor accepted as change cursor", err)
	}

	c, err := parsePublishingCursor(checkpoint)
	if err != nil {
		t.Fatal(err)
	}

	c.Position++
	if _, err = s.Changes(ctx, c.encode(), "", 1); !errors.Is(err, application.ErrCursor) {
		t.Fatal("future cursor", err)
	}

	if _, err = s.Resource(ctx, "activity", "missing"); !errors.Is(err, application.ErrNotFound) {
		t.Fatal(err)
	}

	other, _ := testStore(t)
	if _, err = other.Changes(ctx, checkpoint, "", 1); !errors.Is(err, application.ErrGeneration) {
		t.Fatal("warehouse binding", err)
	}
}

func TestPublishingFinalTransactionStateAndAccountListingRevisions(t *testing.T) {
	ctx := context.Background()
	s := coverageStore(t)
	coverageReport(t, s, "first", syntheticOption)

	checkpoint, err := s.PublishCheckpoint(ctx)
	if err != nil {
		t.Fatal(err)
	}

	accounts, err := s.Resources(ctx, "accounts", "", 1)
	if err != nil {
		t.Fatal(err)
	}

	listings, err := s.Resources(ctx, "listings", "", 1)
	if err != nil || len(listings.Items) != 1 {
		t.Fatal(listings, err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = tx.Rollback() }()

	if _, err = tx.Exec(
		`UPDATE accounts SET name='intermediate'; UPDATE accounts SET name='Reviewed account'; UPDATE listings SET ticker='CHANGED'; UPDATE executions SET status='intermediate'; UPDATE executions SET status='accepted'`,
	); err != nil {
		t.Fatal(err)
	}

	if err = s.commit(ctx, tx); err != nil {
		t.Fatal(err)
	}

	changes := publishedChanges(t, s, checkpoint)
	if len(changes.Items) != 2 {
		t.Fatal("intermediate state published", changes)
	}

	a, err := s.Resource(ctx, "accounts", accounts.Items[0].ID)
	if err != nil || a.Account.Name != "Reviewed account" || a.Revision <= accounts.Items[0].Revision {
		t.Fatal("account revision", a, err)
	}

	l, err := s.Resource(ctx, "listings", listings.Items[0].ID)
	if err != nil || l.Listing.Ticker == nil || *l.Listing.Ticker != "CHANGED" ||
		l.Revision <= listings.Items[0].Revision {
		t.Fatal("listing revision", l, err)
	}

	tx2, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}

	if _, err = tx2.Exec(`UPDATE accounts SET name='rolled back'`); err != nil {
		t.Fatal(err)
	}

	if err = tx2.Rollback(); err != nil {
		t.Fatal(err)
	}

	if len(publishedChanges(t, s, changes.Cursor).Items) != 0 {
		t.Fatal("rollback published")
	}
}
