// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"context"
	"crypto/rand"
	"testing"
	"time"

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/reference"
)

func TestPublishingAtomicCommitAndLatestDisconnectedState(t *testing.T) {
	ctx := context.Background()
	s := coverageStore(t)

	baseline, err := snapshotState(ctx, s)
	if err != nil {
		t.Fatal(err)
	}

	coverageReport(
		t,
		s,
		"first",
		equityRow("U1", "original", "100.0000")+syntheticOption+syntheticDebt+syntheticFX,
	)

	initial := snapshotChanges(t, s, baseline)
	if len(initial.Items) < 8 {
		t.Fatal("missing published resources", len(initial.Items))
	}

	page, err := snapshotResources(ctx, s, "activity")
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

	if got := snapshotChanges(t, s, initial.Snapshot); len(got.Items) != 0 {
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

	current := snapshotChanges(t, s, initial.Snapshot)

	var latest application.CanonicalResource
	for _, item := range current.Items {
		if item.Resource.ID == id {
			latest = item.Resource
		}
	}

	if latest.Revision <= before.Revision || latest.Activity == nil || latest.Activity.State != "cancelled" ||
		!latest.Activity.Conflict {
		t.Fatal("latest disconnected state lost corrections/conflict", latest)
	}

	coverageReport(
		t,
		s,
		"cancel-overlap",
		`<TradeConfirm accountId="U1" assetCategory="STK" transactionType="ExchTrade" levelOfDetail="EXECUTION" origTradeID="Toriginal" code="Ca"/>`,
	)

	if len(snapshotChanges(t, s, current.Snapshot).Items) != 0 {
		t.Fatal("repeated source instruction published")
	}

	why, err := explainCurrent(ctx, s, "activity", id)
	if err != nil || why.Revision != latest.Revision || len(why.Corrections) != 1 {
		t.Fatal("explanation revision", why, err)
	}
	// A failed publisher must roll back both the accepted write and its publication version.
	if _, err = s.db.Exec(
		`CREATE TRIGGER reject_publication BEFORE UPDATE ON publishing BEGIN SELECT RAISE(ABORT,'injected'); END`,
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
		t.Fatal("publication fault acknowledged")
	}

	after, err := s.Resource(ctx, "activity", id)
	if err != nil || after.Revision != latest.Revision || after.Activity.Price.Amount == "999" {
		t.Fatal("partial accepted write", err)
	}

	if len(snapshotChanges(t, s, current.Snapshot).Items) != 0 {
		t.Fatal("rolled back publication visible")
	}
}

func TestPublishingReferenceRevisions(t *testing.T) {
	ctx := context.Background()
	s := coverageStore(t)
	coverageReport(t, s, "first", syntheticOption)

	page, err := snapshotResources(ctx, s, "instruments")
	if err != nil {
		t.Fatal(err)
	}

	baseline, err := snapshotState(ctx, s)
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

	changes := snapshotChanges(t, s, baseline)
	if len(changes.Items) != len(page.Items) {
		t.Fatal("reference changes unpublished", len(changes.Items))
	}
}

func TestPublishingFinalTransactionStateAndAccountListingRevisions(t *testing.T) {
	ctx := context.Background()
	s := coverageStore(t)
	coverageReport(t, s, "first", syntheticOption)

	baseline, err := snapshotState(ctx, s)
	if err != nil {
		t.Fatal(err)
	}

	accounts, err := snapshotResources(ctx, s, "accounts")
	if err != nil {
		t.Fatal(err)
	}

	listings, err := snapshotResources(ctx, s, "listings")
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

	changes := snapshotChanges(t, s, baseline)
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

	if len(snapshotChanges(t, s, changes.Snapshot).Items) != 0 {
		t.Fatal("rollback published")
	}
}
