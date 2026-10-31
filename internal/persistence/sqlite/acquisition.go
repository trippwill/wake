// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"time"

	"go.trippwill.dev/wake/financial"
	"go.trippwill.dev/wake/internal/host"
	"go.trippwill.dev/wake/internal/persistence/sqlite/sqlc"
	"go.trippwill.dev/wake/provider"
	"go.trippwill.dev/wake/source"
)

func (s *Store) Configure(ctx context.Context, c host.Connection, secrets map[string][]byte) error {
	data, err := json.Marshal(c.Settings.Values())
	if err != nil {
		return err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}

	defer func() { _ = tx.Rollback() }()

	q := s.queries.WithTx(tx)
	if err = q.PutDescriptor(
		ctx,
		sqlc.PutDescriptorParams{ID: c.ID, Provider: c.Provider, Name: c.Name},
	); err != nil {
		return err
	}

	if err = q.PutConnection(
		ctx,
		sqlc.PutConnectionParams{ID: c.ID, Provider: c.Provider, Name: c.Name, Settings: data},
	); err != nil {
		return err
	}

	for key, payload := range secrets {
		if err = q.PutSecret(
			ctx,
			sqlc.PutSecretParams{ConnectionID: c.ID, Key: key, Encrypted: payload},
		); err != nil {
			return err
		}
	}

	return s.commit(ctx, tx)
}

func (s *Store) Connection(ctx context.Context, id string) (host.Connection, error) {
	row, err := s.queries.Connection(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		descriptor, e := s.queries.Descriptor(ctx, id)
		if e != nil {
			return host.Connection{}, e
		}

		settings, _ := provider.NewSettings(map[string]string{})

		return host.Connection{
			ID:       id,
			Provider: descriptor.Provider,
			Name:     descriptor.Name,
			Settings: settings,
		}, nil
	}

	if err != nil {
		return host.Connection{}, err
	}

	values := map[string]string{}
	if err = json.Unmarshal(row.Settings, &values); err != nil {
		return host.Connection{}, ErrCorrupt
	}

	settings, err := provider.NewSettings(values)
	if err != nil {
		return host.Connection{}, ErrCorrupt
	}

	return host.Connection{ID: row.ID, Provider: row.Provider, Name: row.Name, Settings: settings}, nil
}

func (s *Store) Reconfigure(ctx context.Context, c host.Connection) error {
	data, err := json.Marshal(c.Settings.Values())
	if err != nil {
		return err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}

	defer func() { _ = tx.Rollback() }()

	q := s.queries.WithTx(tx)

	previous, err := q.Connection(ctx, c.ID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}

	descriptor, e := q.Descriptor(ctx, c.ID)
	if e != nil || descriptor.Provider != c.Provider {
		return ErrAssociation
	}

	if err = q.UpdateConnection(
		ctx,
		sqlc.UpdateConnectionParams{ID: c.ID, Provider: c.Provider, Name: c.Name, Settings: data},
	); err != nil {
		return err
	}

	if err = q.UpdateDescriptorName(ctx, sqlc.UpdateDescriptorNameParams{Name: c.Name, ID: c.ID}); err != nil {
		return err
	}

	if previous.ID == "" || !bytes.Equal(previous.Settings, data) {
		if err = q.ResetConnectionWork(ctx, c.ID); err != nil {
			return err
		}
	}

	return s.accessUpdated(ctx, "connection_settings", s.commit(ctx, tx))
}

func (s *Store) Secret(ctx context.Context, id, key string) ([]byte, error) {
	return s.queries.Secret(ctx, sqlc.SecretParams{ConnectionID: id, Key: key})
}

func (s *Store) ReplaceSecret(ctx context.Context, id, key string, payload []byte) error {
	err := s.queries.ReplaceSecret(
		ctx,
		sqlc.ReplaceSecretParams{ConnectionID: id, Key: key, Encrypted: payload},
	)

	return s.accessUpdated(ctx, "connection_credential", err)
}

func (s *Store) Checkpoint(ctx context.Context, k host.WorkKey) (host.Checkpoint, error) {
	if err := s.queries.EnsureWork(
		ctx,
		sqlc.EnsureWorkParams{ConnectionID: k.Connection, Stream: k.Stream, Operation: k.Operation},
	); err != nil {
		return host.Checkpoint{}, err
	}

	row, err := s.queries.Checkpoint(
		ctx,
		sqlc.CheckpointParams{ConnectionID: k.Connection, Stream: k.Stream, Operation: k.Operation},
	)
	if err != nil {
		return host.Checkpoint{}, err
	}

	state := provider.State{}
	if row.StateVersion != "" {
		state, err = provider.NewState(row.StateVersion, row.State)
		if err != nil {
			return host.Checkpoint{}, ErrCorrupt
		}
	}

	next, err := parseOptionalTime(row.Next)
	if err != nil {
		return host.Checkpoint{}, err
	}

	observed, err := parseProgress(row.Observed, row.ObservedCompleted, row.ObservedTotal)
	if err != nil {
		return host.Checkpoint{}, err
	}

	ack, err := parseProgress(row.Acknowledged, row.AcknowledgedCompleted, row.AcknowledgedTotal)
	if err != nil {
		return host.Checkpoint{}, err
	}

	return host.Checkpoint{
		Revision:     row.Revision,
		State:        state,
		Next:         next,
		Observed:     observed,
		Acknowledged: ack,
		Failure:      row.Failure,
		FailureCode:  host.FailureCode(row.FailureCode),
	}, nil
}

func parseOptionalTime(text string) (time.Time, error) {
	if text == "" {
		return time.Time{}, nil
	}

	at, err := time.Parse(time.RFC3339Nano, text)
	if err != nil {
		return time.Time{}, ErrCorrupt
	}

	return at, nil
}

func parseProgress(code string, n, total int64) (provider.Progress, error) {
	if code == "" {
		return provider.Progress{}, nil
	}

	if n < 0 || total < 0 {
		return provider.Progress{}, ErrCorrupt
	}

	return provider.NewProgress(provider.ProgressPhase(code), uint64(n), uint64(total))
}

func (s *Store) Observe(ctx context.Context, k host.WorkKey, p provider.Progress) error {
	completed, total := p.Completed(), p.Total()
	if completed > math.MaxInt64 || total > math.MaxInt64 {
		return provider.ErrLimit
	}

	return s.queries.ObserveWork(
		ctx,
		sqlc.ObserveWorkParams{
			Observed:          string(p.Phase()),
			ObservedCompleted: int64(completed),
			ObservedTotal:     int64(total),
			ConnectionID:      k.Connection,
			Stream:            k.Stream,
			Operation:         k.Operation,
		},
	)
}

func (s *Store) Fail(ctx context.Context, k host.WorkKey, code host.FailureCode, message string) error {
	return s.queries.FailWork(
		ctx,
		sqlc.FailWorkParams{
			Failure:      message,
			FailureCode:  string(code),
			ConnectionID: k.Connection,
			Stream:       k.Stream,
			Operation:    k.Operation,
		},
	)
}

func (s *Store) Accept(
	ctx context.Context,
	k host.WorkKey,
	revision int64,
	step provider.Step,
	candidates []provider.Candidate,
	p provider.Progress,
) (string, error) {
	completed, total := p.Completed(), p.Total()
	if completed > math.MaxInt64 || total > math.MaxInt64 {
		return "", provider.ErrLimit
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}

	defer func() { _ = tx.Rollback() }()

	q := s.queries.WithTx(tx)

	rows, err := q.AdvanceWork(
		ctx,
		sqlc.AdvanceWorkParams{
			StateVersion:          step.State().Version(),
			State:                 nonNil(step.State().Payload()),
			Next:                  step.NextEligibleAt().Format(time.RFC3339Nano),
			Acknowledged:          string(p.Phase()),
			AcknowledgedCompleted: int64(completed),
			AcknowledgedTotal:     int64(total),
			Failure:               step.FailureCode(),
			FailureCode:           string(workFailureCode(step.RetryReason())),
			ConnectionID:          k.Connection,
			Stream:                k.Stream,
			Operation:             k.Operation,
			Revision:              revision,
		},
	)
	if err != nil {
		return "", err
	}

	if rows != 1 {
		return "", provider.ErrConflict
	}

	id := ""
	a := step.Artifact()
	if !a.IsZero() {
		id = rand.Text()
		if err = q.PutArtifact(
			ctx,
			sqlc.PutArtifactParams{
				ID:           id,
				ConnectionID: k.Connection,
				Stream:       k.Stream,
				Operation:    k.Operation,
				Format:       a.Format(),
				MediaType:    a.MediaType(),
				Retrieved:    a.RetrievedAt().Format(time.RFC3339Nano),
				Hash:         a.Hash(),
				Content:      a.Content(),
				RequestedFrom: step.Coverage().
					RequestedFrom(),
				RequestedTo:  step.Coverage().RequestedTo(),
				ReportedFrom: step.Coverage().ReportedFrom(),
				ReportedTo:   step.Coverage().ReportedTo(),
			},
		); err != nil {
			return "", err
		}
	}

	if k.Operation == "discover" && id != "" && step.State().IsZero() {
		if err = setScheduled(q,
			ctx,
			sqlc.SetScheduledParams{
				ConnectionID: k.Connection,
				Stream:       k.Stream,
				Operation:    k.Operation,
				Enabled:      0,
			},
		); err != nil {
			return "", err
		}
	}

	for _, c := range candidates {
		ref := c.Reference()
		currency := ""
		if c.Currency() != (financial.Currency{}) {
			currency = c.Currency().String()
		}

		if err = q.PutCandidate(
			ctx,
			sqlc.PutCandidateParams{
				ArtifactID: id,
				Authority:  ref.Authority(),
				Kind:       ref.Kind(),
				Scope:      ref.Scope(),
				Value:      ref.Value(),
				Name:       c.Name(),
				Currency:   currency,
			},
		); err != nil {
			return "", err
		}
	}

	if err = s.commit(ctx, tx); err != nil {
		return "", err
	}

	return id, nil
}

func nonNil(data []byte) []byte {
	if data == nil {
		return []byte{}
	}

	return data
}

func (s *Store) Retained(ctx context.Context, id string) (host.Retained, error) {
	row, err := s.queries.Retained(ctx, id)
	if err != nil {
		return host.Retained{}, err
	}

	at, err := parseOptionalTime(row.Retrieved)
	if err != nil {
		return host.Retained{}, err
	}

	a, err := provider.NewArtifact(row.Format, row.MediaType, at, row.Content)
	if err != nil || a.Hash() != row.Hash {
		return host.Retained{}, ErrCorrupt
	}

	return host.Retained{
		ID:         row.ID,
		Connection: row.ConnectionID,
		Provider:   row.Provider,
		Stream:     row.Stream,
		Artifact:   a,
	}, nil
}

func (s *Store) Candidates(ctx context.Context, id string) ([]provider.Candidate, error) {
	rows, err := s.queries.Candidates(ctx, id)
	if err != nil {
		return nil, err
	}

	out := []provider.Candidate{}
	for _, r := range rows {
		ref, e := source.NewReference(r.Authority, r.Kind, r.Scope, r.Value)
		if e != nil {
			return nil, ErrCorrupt
		}

		currency := financial.Currency{}
		if r.Currency != "" {
			currency, e = financial.ParseCurrency(r.Currency)
			if e != nil {
				return nil, ErrCorrupt
			}
		}

		candidate, e := provider.NewCandidate(ref, r.Name, currency)
		if e != nil {
			return nil, ErrCorrupt
		}

		out = append(out, candidate)
	}

	return out, nil
}

func (s *Store) Reserve(
	ctx context.Context,
	p, policy, scope string,
	now time.Time,
	spacing time.Duration,
) (provider.Reservation, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return provider.Reservation{}, err
	}
	defer func() { _ = tx.Rollback() }()

	q := s.queries.WithTx(tx)

	text, err := q.Pacing(ctx, sqlc.PacingParams{Provider: p, Policy: policy, Scope: scope})
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return provider.Reservation{}, err
	}

	next, err := parseOptionalTime(text)
	if err != nil {
		return provider.Reservation{}, err
	}

	if next.After(now) {
		return provider.NewReservation(false, next)
	}

	next = now.Add(spacing).UTC()
	if err = q.SetPacing(
		ctx,
		sqlc.SetPacingParams{Provider: p, Policy: policy, Scope: scope, Next: next.Format(time.RFC3339Nano)},
	); err != nil {
		return provider.Reservation{}, err
	}

	if err = s.commit(ctx, tx); err != nil {
		return provider.Reservation{}, err
	}

	return provider.NewReservation(true, next)
}

func (s *Store) Delay(ctx context.Context, p, policy, scope string, until time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	q := s.queries.WithTx(tx)

	text, err := q.Pacing(ctx, sqlc.PacingParams{Provider: p, Policy: policy, Scope: scope})
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}

	next, err := parseOptionalTime(text)
	if err != nil {
		return err
	}

	if !until.After(next) {
		return nil
	}

	if err = q.SetPacing(
		ctx,
		sqlc.SetPacingParams{
			Provider: p,
			Policy:   policy,
			Scope:    scope,
			Next:     until.UTC().Format(time.RFC3339Nano),
		},
	); err != nil {
		return err
	}

	return s.commit(ctx, tx)
}

var _ host.Store = (*Store)(nil)

func (s *Store) accessUpdated(ctx context.Context, kind string, err error) error {
	if err != nil {
		s.logger.ErrorContext(
			ctx,
			"access repair failed",
			"event",
			"storage_access_failed",
			"operation",
			kind,
			"code",
			storageCode(err),
			"outcome",
			"failed",
		)

		return err
	}

	s.logger.InfoContext(
		ctx,
		"access configuration saved",
		"event",
		"storage_access_updated",
		"operation",
		kind,
		"outcome",
		"succeeded",
	)

	return nil
}
