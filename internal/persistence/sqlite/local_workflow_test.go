// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/application/calculation"
	"go.trippwill.dev/wake/internal/host"
	"go.trippwill.dev/wake/internal/transport/financialapi"
	"go.trippwill.dev/wake/provider"
	"go.trippwill.dev/wake/providers/ibkrflex"
)

type localOnlyStore struct {
	*Store
	t *testing.T
}

func (s localOnlyStore) PendingWork(context.Context, time.Time) ([]host.WorkKey, error) {
	s.t.Fatal("local runner queried upstream scheduling")
	return nil, nil
}

func TestLocalWorkflowProcessesRetainedEvidenceWithoutUpstream(t *testing.T) {
	s := newStoreWithSyntheticAccount(t)
	body := []byte(confirmationReport(syntheticOption))

	artifact, err := provider.NewArtifact(ibkrflex.ConfirmationFormat, "application/xml", time.Now(), body)
	if err != nil {
		t.Fatal(err)
	}

	profile := testProfile(s.owners[0].Name())
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = OpenFinancial(t.Context(), profile, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	id, err := s.RetainSupplied(t.Context(), "synthetic", "confirmations", artifact)
	if err != nil {
		t.Fatal(err)
	}

	registration, err := ibkrflex.Registration(ibkrflex.Options{})
	if err != nil {
		t.Fatal(err)
	}

	s.registrations = []provider.Registration{registration}

	now := time.Now()
	versions := map[string]string{registration.Descriptor().ID(): registration.Version()}

	canceled, cancel := context.WithCancel(t.Context())
	cancel()

	if err = s.ProcessingFailure(canceled, id, now.Add(time.Hour)); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled failure changed the processing retry", err)
	}

	if _, err = s.PendingArtifacts(canceled, now, versions); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled read returned processing work", err)
	}

	if err = s.RetryProcessing(canceled, id); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled retry cleared the processing failure", err)
	}

	if err = s.ProcessingFailure(t.Context(), id, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	if pending, e := s.PendingArtifacts(t.Context(), now, versions); e != nil || len(pending) != 0 {
		t.Fatal("deferred artifact was eligible", pending, e)
	}

	if err = s.RetryProcessing(t.Context(), id); err != nil {
		t.Fatal(err)
	}

	if pending, e := s.PendingArtifacts(
		t.Context(),
		now,
		versions,
	); e != nil || len(pending) != 1 ||
		pending[0].ID != id {
		t.Fatal("retry did not make retained artifact eligible", pending, e)
	}

	w := application.New(localOnlyStore{s, t}, []provider.Registration{registration}, nil, nil, nil, nil)
	for range 2 {
		if err = w.RunOnce(t.Context(), time.Now()); err != nil {
			t.Fatal(err)
		}
	}

	executions, err := s.queries.Executions(t.Context())
	if err != nil || len(executions) != 1 {
		t.Fatal(executions, err)
	}

	retainDecision(t, s, "activity", executions[0].ID, "price", "2.340000000001", "USD", "set")

	var normalized, processed int
	if err = s.db.QueryRow(`SELECT (SELECT count(*) FROM normalization_receipts WHERE artifact_id=?),(SELECT count(*) FROM processing_receipts WHERE artifact_id=?)`, id, id).
		Scan(&normalized, &processed); err != nil ||
		normalized != 1 ||
		processed != 1 {
		t.Fatal(normalized, processed, err)
	}

	if pending, e := s.PendingArtifacts(t.Context(), now, versions); e != nil || len(pending) != 0 {
		t.Fatal("completed receipt was queued again", pending, e)
	}

	status := func() application.ProcessingStatus {
		t.Helper()

		view, e := s.Overview(t.Context())
		if e != nil || len(view.Status.Connections) != 1 || len(view.Status.Connections[0].Processing) != 1 {
			t.Fatal("missing report status", view.Status.Connections, e)
		}

		return view.Status.Connections[0].Processing[0]
	}
	if old := status(); !old.Complete || old.ActivityClaims == 0 {
		t.Fatal("installed interpretation was not complete", old)
	}

	const next = "new-interpretation"

	newer, err := provider.NewRegistration(
		registration.Descriptor(), next, registration.Acquirer(),
		claimNormalizer(func(context.Context, provider.Artifact, provider.Claims) (provider.Result, error) {
			return provider.Result{}, provider.ErrMalformed
		}), registration.Discoverer(), registration,
	)
	if err != nil {
		t.Fatal(err)
	}

	s.registrations = []provider.Registration{newer}

	versions[registration.Descriptor().ID()] = next
	if pending, e := s.PendingArtifacts(
		t.Context(),
		now,
		versions,
	); e != nil || len(pending) != 1 ||
		pending[0].ID != id {
		t.Fatal("changed interpretation did not rediscover retained evidence", pending, e)
	}

	if err = application.New(localOnlyStore{s, t}, s.registrations, nil, nil, nil, nil).
		RunOnce(t.Context(), now); !errors.Is(
		err,
		provider.ErrMalformed,
	) {
		t.Fatal(err)
	}

	if failed := status(); failed.Complete || failed.Failure == "" || failed.ActivityClaims != 0 {
		t.Fatal("failed interpretation inherited an older completion", failed)
	}

	if err = w.Retry(t.Context(), "synthetic"); !errors.Is(err, provider.ErrUnavailable) {
		t.Fatal(err)
	}

	if err = w.SetReferenceKey(t.Context(), nil); !errors.Is(err, provider.ErrUnavailable) {
		t.Fatal(err)
	}

	// Simulate the next compiled rule with the same financial-only connection.
	s.historyRule = "retained-claims-and-decisions/2"
	if err = s.initializeFinancial(t.Context(), false); err != nil || !s.HistoryUpdating() {
		t.Fatal("financial-only rebuild did not start", err)
	}

	response := httptest.NewRecorder()
	financialapi.NewHandler(s, nil, nil).
		ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/snapshot", nil))

	if response.Code != http.StatusServiceUnavailable || response.Header().Get("ETag") != "" {
		t.Fatal("partially rebuilt financial state was readable", response.Code)
	}

	for range 5 {
		if !s.HistoryUpdating() {
			break
		}

		if err = w.RunOnce(t.Context(), time.Now()); err != nil {
			t.Fatal(err)
		}
	}

	after, err := snapshotState(t.Context(), s)
	if err != nil || s.HistoryUpdating() {
		t.Fatal("financial-only rebuild did not publish", err)
	}

	for _, item := range after.Resources {
		if item.ID == executions[0].ID && item.Activity != nil && item.Activity.Trade != nil &&
			item.Activity.Trade.Price != nil && item.Activity.Trade.Price.Amount == "2.340000000001" {
			return
		}
	}

	t.Fatal("financial-only rebuild lost accepted correction")
}

func TestLocalSupplyRollsBackWhenReceiptFails(t *testing.T) {
	ctx := t.Context()
	dir := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(dir, "wake.db")

	store, err := OpenFinancial(ctx, Profile{WarehousePath: path}, nil)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = store.Close() }()

	if _, err = store.db.ExecContext(ctx, `INSERT INTO connections(id,provider) VALUES('local','synthetic');
CREATE TRIGGER reject_supply_receipt BEFORE INSERT ON acquisition_receipts
BEGIN SELECT RAISE(ABORT,'injected receipt failure'); END;`); err != nil {
		t.Fatal(err)
	}

	artifact, err := provider.NewArtifact("synthetic", "application/json", time.Now(), []byte("{}"))
	if err != nil {
		t.Fatal(err)
	}

	if id, err := store.RetainSupplied(
		ctx,
		"local",
		"reports",
		provider.Artifact{},
	); id != "" ||
		!errors.Is(err, provider.ErrInvalid) {
		t.Fatal("empty evidence was accepted", id, err)
	}

	canceled, cancel := context.WithCancel(ctx)
	cancel()

	if id, err := store.RetainSupplied(
		canceled,
		"local",
		"reports",
		artifact,
	); id != "" ||
		!errors.Is(err, context.Canceled) {
		t.Fatal("canceled supply acknowledged evidence", id, err)
	}

	if id, err := store.RetainSupplied(ctx, "local", "reports", artifact); id != "" || err == nil {
		t.Fatal("failed receipt acknowledged a report", id, err)
	}

	var artifacts, receipts int
	if err = store.db.QueryRowContext(ctx,
		`SELECT (SELECT count(*) FROM artifacts),(SELECT count(*) FROM acquisition_receipts)`,
	).Scan(&artifacts, &receipts); err != nil || artifacts != 0 || receipts != 0 {
		t.Fatal("failed receipt left partial evidence", artifacts, receipts, err)
	}

	if _, err = store.db.ExecContext(ctx, "DROP TRIGGER reject_supply_receipt"); err != nil {
		t.Fatal(err)
	}

	id, err := store.RetainSupplied(ctx, "local", "reports", artifact)
	if id == "" || err != nil {
		t.Fatal("could not retain evidence after retry", id, err)
	}

	if err = store.RetryProcessing(ctx, id); !errors.Is(err, calculation.ErrInvalid) {
		t.Fatal("supplied evidence received account authority", err)
	}

	if err = store.RetryProcessing(ctx, "missing"); !errors.Is(err, calculation.ErrInvalid) {
		t.Fatal("unknown report was eligible for retry", err)
	}
}
