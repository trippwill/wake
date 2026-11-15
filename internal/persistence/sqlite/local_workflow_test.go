// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/host"
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

	if _, err := s.db.Exec(
		`INSERT INTO artifacts(id,connection_id,stream,operation,format,media_type,retrieved,hash,content) VALUES('local','synthetic','confirmations','acquire','ibkr-flex-confirmation-xml','application/xml','2026-10-20T00:00:00Z',?,?)`,
		artifact.Hash(),
		body,
	); err != nil {
		t.Fatal(err)
	}

	recordTestReceipt(t, s, "local")

	registration, err := ibkrflex.Registration(ibkrflex.Options{})
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	versions := map[string]string{registration.Descriptor().ID(): registration.Version()}

	if err = s.ProcessingFailure(t.Context(), "local", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	if pending, e := s.PendingArtifacts(t.Context(), now, versions); e != nil || len(pending) != 0 {
		t.Fatal("deferred artifact was eligible", pending, e)
	}

	if err = s.RetryProcessing(t.Context(), "local"); err != nil {
		t.Fatal(err)
	}

	if pending, e := s.PendingArtifacts(
		t.Context(),
		now,
		versions,
	); e != nil || len(pending) != 1 ||
		pending[0].ID != "local" {
		t.Fatal("retry did not make retained artifact eligible", pending, e)
	}

	w := application.New(localOnlyStore{s, t}, []provider.Registration{registration}, nil, nil, nil, nil)
	for range 2 {
		if err = w.RunOnce(t.Context(), time.Now()); err != nil {
			t.Fatal(err)
		}
	}

	view, err := s.Dashboard(t.Context())
	if err != nil || len(view.Executions) != 1 {
		t.Fatal(view, err)
	}

	var normalized, processed int
	if err = s.db.QueryRow(`SELECT (SELECT count(*) FROM normalization_receipts WHERE artifact_id='local'),(SELECT count(*) FROM processing_receipts WHERE artifact_id='local')`).
		Scan(&normalized, &processed); err != nil ||
		normalized != 1 ||
		processed != 1 {
		t.Fatal(normalized, processed, err)
	}

	if pending, e := s.PendingArtifacts(t.Context(), now, versions); e != nil || len(pending) != 0 {
		t.Fatal("completed receipt was queued again", pending, e)
	}

	versions[registration.Descriptor().ID()] = "new-interpretation"
	if pending, e := s.PendingArtifacts(
		t.Context(),
		now,
		versions,
	); e != nil || len(pending) != 1 ||
		pending[0].ID != "local" {
		t.Fatal("changed interpretation did not rediscover retained evidence", pending, e)
	}

	if err = w.Retry(t.Context(), "synthetic"); !errors.Is(err, provider.ErrUnavailable) {
		t.Fatal(err)
	}

	if err = w.SetReferenceKey(t.Context(), nil); !errors.Is(err, provider.ErrUnavailable) {
		t.Fatal(err)
	}
}
