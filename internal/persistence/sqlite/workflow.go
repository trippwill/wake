// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"time"

	"go.trippwill.dev/wake/financial"
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

	return tx.Commit()
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

	return tx.Commit()
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

	if count != 1 {
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

	if err = q.DisableDiscovery(ctx, id); err != nil {
		return application.Account{}, err
	}

	if err = tx.Commit(); err != nil {
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

func (s *Store) PendingArtifacts(ctx context.Context, now time.Time) ([]string, error) {
	return s.queries.PendingArtifacts(ctx, now.UTC().Format(time.RFC3339Nano))
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

func (s *Store) Dashboard(ctx context.Context) (application.Dashboard, error) {
	out := application.Dashboard{}

	account, err := s.queries.Account(ctx)
	if err == nil {
		out.Account, err = accountValue(account)
	}

	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}

	ids, err := s.queries.Connections(ctx)
	if err != nil {
		return out, err
	}

	for _, id := range ids {
		c, e := s.Connection(ctx, id)
		if e != nil {
			return out, e
		}

		name, _ := c.Settings.Get("name")
		query, _ := c.Settings.Get("query_id")
		view := application.ConnectionView{ID: id, Name: name, Query: query}

		rows, e := s.queries.CandidateProfiles(ctx, id)
		if e != nil {
			return out, e
		}

		seen := map[source.Reference]bool{}
		for _, r := range rows {
			ref, e := source.NewReference(r.Authority, r.Kind, r.Scope, r.Value)
			if e != nil {
				return out, ErrCorrupt
			}

			if seen[ref] {
				continue
			}

			seen[ref] = true

			c := financial.Currency{}
			if r.Currency != "" {
				c, e = financial.ParseCurrency(r.Currency)
				if e != nil {
					return out, ErrCorrupt
				}
			}

			candidate, e := provider.NewCandidate(ref, r.Name, c)
			if e != nil {
				return out, ErrCorrupt
			}

			view.Candidates = append(
				view.Candidates,
				application.Candidate{Artifact: r.ArtifactID, Candidate: candidate},
			)
		}

		statuses, e := s.queries.WorkStatuses(ctx, id)
		if e != nil {
			return out, e
		}

		for _, r := range statuses {
			view.Work = append(
				view.Work,
				application.WorkStatus{
					Operation: r.Operation,
					Progress:  r.Observed,
					Failure:   r.Failure,
					Next:      r.Next,
				},
			)
		}

		processing, e := s.queries.ProcessingStatuses(ctx, id)
		if e != nil {
			return out, e
		}

		for _, r := range processing {
			view.Processing = append(
				view.Processing,
				application.ProcessingStatus{
					Artifact:      r.ID,
					Complete:      r.Processed == 1,
					RequestedFrom: r.RequestedFrom,
					RequestedTo:   r.RequestedTo,
					ReportedFrom:  r.ReportedFrom,
					ReportedTo:    r.ReportedTo,
					Failure:       r.ProcessingFailure,
					TradeClaims:   r.TradeClaims, ExcludedRows: r.ExcludedRows, NeedsReview: r.NeedsReview,
				},
			)
		}

		out.Connections = append(out.Connections, view)
	}

	executions, err := s.queries.Executions(ctx)
	if err != nil {
		return out, err
	}

	for _, r := range executions {
		e, err := executionValue(r)
		if err != nil {
			return out, err
		}

		out.Executions = append(out.Executions, e)
	}

	instruments, err := s.queries.Instruments(ctx)
	if err != nil {
		return out, err
	}

	for _, r := range instruments {
		i, err := instrumentValue(r)
		if err != nil {
			return out, err
		}

		out.Instruments = append(out.Instruments, i)
	}

	setting, err := s.queries.ReferenceSetting(ctx)
	if err == nil {
		out.ReferenceKey = len(setting.Encrypted) > 0
		out.ReferenceFailure = setting.Failure
	} else if !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}

	return out, nil
}
