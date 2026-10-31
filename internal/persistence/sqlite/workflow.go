// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"time"

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/host"
	"go.trippwill.dev/wake/internal/persistence/sqlite/sqlc"
	"go.trippwill.dev/wake/provider"
	"go.trippwill.dev/wake/source"
)

func (s *Store) Schedule(ctx context.Context, key host.WorkKey, enabled bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	q := s.queries.WithTx(tx)
	if err = q.EnsureWork(
		ctx,
		sqlc.EnsureWorkParams{ConnectionID: key.Connection, Stream: key.Stream, Operation: key.Operation},
	); err != nil {
		return err
	}

	var flag int64
	if enabled {
		flag = 1
	}

	if err = q.SetScheduled(
		ctx,
		sqlc.SetScheduledParams{
			ConnectionID: key.Connection,
			Stream:       key.Stream,
			Operation:    key.Operation,
			Enabled:      flag,
		},
	); err != nil {
		return err
	}

	return s.commit(ctx, tx)
}

func (s *Store) Retry(ctx context.Context, key host.WorkKey) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	q := s.queries.WithTx(tx)
	if err = q.EnsureWork(
		ctx,
		sqlc.EnsureWorkParams{ConnectionID: key.Connection, Stream: key.Stream, Operation: key.Operation},
	); err != nil {
		return err
	}

	if key.Operation == "discover" {
		err = q.RestartDiscovery(
			ctx,
			sqlc.RestartDiscoveryParams{ConnectionID: key.Connection, Stream: key.Stream},
		)
	} else {
		err = q.RetryWork(
			ctx,
			sqlc.RetryWorkParams{ConnectionID: key.Connection, Stream: key.Stream, Operation: key.Operation},
		)
	}

	if err != nil {
		return err
	}

	if err = q.SetScheduled(
		ctx,
		sqlc.SetScheduledParams{
			ConnectionID: key.Connection,
			Stream:       key.Stream,
			Operation:    key.Operation,
			Enabled:      1,
		},
	); err != nil {
		return err
	}

	return s.commit(ctx, tx)
}

func accountValue(row sqlc.Account) (application.Account, error) {
	details, err := application.NewAccountDetails(row.Name, row.Currency)
	if err != nil {
		return application.Account{}, ErrCorrupt
	}

	ref, err := source.NewReference(row.Authority, row.Kind, row.Scope, row.Value)
	if err != nil {
		return application.Account{}, ErrCorrupt
	}

	return application.NewAccount(row.ID, row.ConnectionID, row.Provider, row.Stream, ref, details, row.Rule)
}

func (s *Store) Confirm(
	ctx context.Context,
	id, artifact string,
	ref source.Reference,
	details application.AccountDetails,
	acquire bool,
) (application.Account, error) {
	if details.Name() == "" || ref.IsZero() {
		return application.Account{}, application.ErrInvalid
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return application.Account{}, err
	}

	defer func() { _ = tx.Rollback() }()

	q := s.queries.WithTx(tx)

	existing, err := q.Account(ctx)
	if err == nil {
		if existing.ConnectionID == id && existing.Authority == ref.Authority() && existing.Kind == ref.Kind() &&
			existing.Scope == ref.Scope() &&
			existing.Value == ref.Value() {
			return accountValue(existing)
		}

		return application.Account{}, application.ErrAccountLimit
	}

	if !errors.Is(err, sql.ErrNoRows) {
		return application.Account{}, err
	}

	count, err := q.CandidateExists(
		ctx,
		sqlc.CandidateExistsParams{
			ConnectionID: id,
			ArtifactID:   artifact,
			Authority:    ref.Authority(),
			Kind:         ref.Kind(),
			Scope:        ref.Scope(),
			Value:        ref.Value(),
		},
	)
	if err != nil {
		return application.Account{}, err
	}

	if count == 0 {
		return application.Account{}, application.ErrCandidate
	}

	c, err := q.Connection(ctx, id)
	if err != nil {
		return application.Account{}, err
	}

	evidence, err := q.Retained(ctx, artifact)
	if err != nil {
		return application.Account{}, err
	}

	a, err := application.NewAccount(
		rand.Text(),
		id,
		c.Provider,
		evidence.Stream,
		ref,
		details,
		application.AuthorityRule,
	)
	if err != nil {
		return application.Account{}, err
	}

	if err = q.PutAccount(
		ctx,
		sqlc.PutAccountParams{
			Rule:         application.AuthorityRule,
			ID:           a.ID(),
			Name:         details.Name(),
			Currency:     details.Currency(),
			ConnectionID: id,
			Provider:     c.Provider,
			Stream:       evidence.Stream,
			Authority:    ref.Authority(),
			Kind:         ref.Kind(),
			Scope:        ref.Scope(),
			Value:        ref.Value(),
			ArtifactID:   artifact,
		},
	); err != nil {
		return application.Account{}, err
	}

	if acquire {
		if err = q.EnsureWork(
			ctx,
			sqlc.EnsureWorkParams{ConnectionID: id, Stream: a.Stream(), Operation: "acquire"},
		); err != nil {
			return application.Account{}, err
		}

		if err = q.SetScheduled(
			ctx,
			sqlc.SetScheduledParams{ConnectionID: id, Stream: a.Stream(), Operation: "acquire", Enabled: 1},
		); err != nil {
			return application.Account{}, err
		}
	}

	if err = q.DisableDiscovery(ctx, id); err != nil {
		return application.Account{}, err
	}

	if err = s.commit(ctx, tx); err != nil {
		return application.Account{}, err
	}

	return a, nil
}

func (s *Store) PendingWork(ctx context.Context, now time.Time) ([]host.WorkKey, error) {
	rows, err := s.queries.PendingWork(ctx, now.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return nil, err
	}

	out := []host.WorkKey{}
	for _, r := range rows {
		out = append(out, host.WorkKey{Connection: r.ConnectionID, Stream: r.Stream, Operation: r.Operation})
	}

	return out, nil
}

func (s *Store) PendingArtifacts(
	ctx context.Context,
	now time.Time,
	providers []string,
) ([]application.PendingArtifact, error) {
	if len(providers) == 0 {
		return nil, nil
	}

	rows, err := s.queries.PendingArtifacts(
		ctx,
		sqlc.PendingArtifactsParams{Now: now.UTC().Format(time.RFC3339Nano), Providers: providers},
	)
	if err != nil {
		return nil, err
	}

	out := make([]application.PendingArtifact, 0, len(rows))
	for _, row := range rows {
		out = append(out, application.PendingArtifact{ID: row.ID, Provider: row.Provider})
	}

	return out, nil
}

func (s *Store) ProcessingFailure(ctx context.Context, id string, next time.Time) error {
	return s.queries.ArtifactFailure(
		ctx,
		sqlc.ArtifactFailureParams{
			ID:                id,
			ProcessingFailure: "processing failed; retained evidence is safe",
			ProcessingNext:    next.UTC().Format(time.RFC3339Nano),
		},
	)
}

func (s *Store) ResumeStorage(ctx context.Context, revision int64, actor string, now time.Time) error {
	if actor == "" {
		return application.ErrInvalid
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	q := s.queries.WithTx(tx)

	profile, err := q.Profile(ctx)
	if err != nil {
		return err
	}

	if profile.RecoveryRevision != revision {
		return application.ErrStale
	}

	if profile.Paused == 0 {
		return nil
	}

	if profile.ReferenceReview != 0 {
		return application.ErrAccessReview
	}

	rows, err := q.ResumeStorage(
		ctx,
		sqlc.ResumeStorageParams{
			ResumedBy:        actor,
			ResumedAt:        now.UTC().Format(time.RFC3339Nano),
			RecoveryRevision: revision,
		},
	)
	if err != nil {
		s.logger.ErrorContext(
			ctx,
			"storage resume failed",
			"event",
			"storage_resume_failed",
			"code",
			storageCode(err),
			"outcome",
			"failed",
		)

		return err
	}

	if rows != 1 {
		return application.ErrAccessReview
	}

	if err = s.commit(ctx, tx); err != nil {
		return err
	}

	s.logger.InfoContext(
		ctx,
		"upstream work resumed after operator review",
		"event",
		"storage_resumed",
		"outcome",
		"succeeded",
		"revision",
		revision,
	)

	return nil
}

func (s *Store) UpstreamAllowed(ctx context.Context) error {
	p, err := s.queries.Profile(ctx)
	if err != nil {
		return err
	}

	if p.Paused != 0 {
		return provider.ErrUnavailable
	}

	return nil
}

func (s *Store) RetryProcessing(ctx context.Context, id string) error {
	n, err := s.queries.RetryProcessing(ctx, id)
	if err == nil && n != 1 {
		return application.ErrInvalid
	}

	return err
}
