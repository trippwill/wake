// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"context"
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

func (s *Store) RestartInputs(ctx context.Context) (application.RestartInputs, error) {
	var result application.RestartInputs
	if s.owners[2] == nil {
		return result, nil
	}

	tx, err := beginUpstream(ctx, s.db)
	if err != nil {
		return result, err
	}

	defer func() { _ = tx.Rollback() }()

	at, err := recoveryStart(ctx, tx)
	if err != nil || at.IsZero() {
		return result, err
	}

	profile, err := s.queries.WithTx(tx).Profile(ctx)
	if err != nil {
		return result, err
	}

	result.At, result.Revision = at, profile.RecoveryRevision

	result.Sources, err = restartSources(ctx, tx)
	if err != nil {
		return result, err
	}

	for i := range result.Sources {
		if err := restartSourceState(ctx, tx, &result.Sources[i]); err != nil {
			return result, err
		}
	}

	return result, nil
}

func recoveryStart(ctx context.Context, db associationReader) (time.Time, error) {
	var text string

	err := db.QueryRowContext(ctx, profileStatement("RecoveryStart", "")).Scan(&text)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, nil
	}

	if err != nil {
		return time.Time{}, err
	}

	return time.Parse(time.RFC3339Nano, text)
}

func restartSources(ctx context.Context, tx *sql.Tx) ([]application.RestartInput, error) {
	rows, err := tx.QueryContext(ctx, profileStatement("RecoverySourceInputs", ""))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var result []application.RestartInput
	for rows.Next() {
		var (
			input                         application.RestartInput
			settings                      []byte
			authority, kind, scope, value string
		)
		if err := rows.Scan(&input.ID, &input.Provider, &input.Name, &settings, &input.AccountID,
			&input.AccountRevision, &authority, &kind, &scope, &value); err != nil {
			return nil, err
		}

		if len(settings) != 0 {
			if decoded, _, err := decodeConnectionSettings(settings); err == nil {
				input.Settings, input.Binding = decoded, digest(settings)
			}
		}

		if input.AccountID != "" {
			input.Account, err = source.NewReference(authority, kind, scope, value)
			if err != nil {
				return nil, err
			}
		}

		result = append(result, input)
	}

	return result, rows.Err()
}

func restartSourceState(ctx context.Context, tx *sql.Tx, input *application.RestartInput) error {
	coverage, err := tx.QueryContext(ctx, profileStatement("RecoveryCoverage", ""), input.ID)
	if err != nil {
		return err
	}
	defer func() { _ = coverage.Close() }()

	input.Retained = map[string]financial.Date{}
	for coverage.Next() {
		var stream, bound string
		if err := coverage.Scan(&stream, &bound); err != nil {
			return err
		}

		date, err := financial.ParseDate(bound)
		if err != nil {
			return err
		}

		input.Retained[stream] = date
	}

	if err := coverage.Err(); err != nil {
		return err
	}

	pacing, err := tx.QueryContext(ctx, profileStatement("RecoveryPacingFloor", ""), input.Provider)
	if err != nil {
		return err
	}
	defer func() { _ = pacing.Close() }()

	for pacing.Next() {
		var deadline string
		if err := pacing.Scan(&deadline); err != nil {
			return err
		}

		at, err := parseOptionalTime(deadline)
		if err != nil {
			return err
		}

		if at.After(input.PacingFloor) {
			input.PacingFloor = at
		}
	}

	if err := pacing.Err(); err != nil {
		return err
	}

	approved, err := tx.QueryContext(ctx, profileStatement("ApprovedRestartStreams", ""), input.ID)
	if err != nil {
		return err
	}
	defer func() { _ = approved.Close() }()

	input.Approved = map[string]bool{}
	for approved.Next() {
		var stream string
		if err := approved.Scan(&stream); err != nil {
			return err
		}

		input.Approved[stream] = true
	}

	return approved.Err()
}

func (s *Store) ApproveRestart(
	ctx context.Context,
	review application.RestartReview,
	actor string,
	now time.Time,
) error {
	if actor == "" || s.credentialKeyUnavailable.Load() {
		return application.ErrAccessReview
	}

	tx, err := beginUpstream(ctx, s.db)
	if err != nil {
		return err
	}

	defer func() { _ = tx.Rollback() }()

	profile, err := s.queries.WithTx(tx).Profile(ctx)
	if err != nil {
		return err
	}

	if profile.RecoveryRevision != review.Revision {
		return application.ErrStale
	}

	atRecovery, err := recoveryStart(ctx, tx)
	if err != nil {
		return err
	}

	if atRecovery.IsZero() {
		return application.ErrStale
	}

	q := s.queries.WithTx(tx)
	at := now.UTC().Format(time.RFC3339Nano)
	changed := false
	for _, scope := range review.Scopes {
		if scope.Approved || scope.Blocked != "" {
			continue
		}

		if err := approveRestartScope(ctx, tx, q, scope, actor, at); err != nil {
			return err
		}

		changed = true
	}

	var referenceApproved bool
	if err := tx.QueryRowContext(ctx, profileStatement("ReferenceRecoveryApproved", "")).
		Scan(&referenceApproved); err != nil {
		return err
	}

	if !changed && profile.Paused == 0 && referenceApproved == review.Reference {
		return nil
	}

	if _, err := tx.ExecContext(
		ctx,
		profileStatement("SetRecoveredReference", ""),
		review.Reference,
	); err != nil {
		return err
	}

	if review.Reference {
		if err := reserveRecoveredReference(ctx, q, review.ReferenceAt); err != nil {
			return err
		}
	}

	if _, err := tx.ExecContext(ctx, profileStatement("ResumeReviewedRecovery", ""), actor, at); err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx, profileStatement("IssueGrant", ""), actor, at); err != nil {
		return err
	}

	return s.commitUpstream(ctx, tx, upstreamAuthority)
}

func approveRestartScope(
	ctx context.Context, tx *sql.Tx, q *sqlc.Queries, scope application.RestartScope, actor, at string,
) error {
	var (
		settings []byte
		account  string
		revision int64
	)
	if err := tx.QueryRowContext(ctx, profileStatement("CurrentRestartScope", ""), scope.Connection).
		Scan(&settings, &account, &revision); err != nil {
		return err
	}

	if digest(settings) != scope.Binding || account != scope.AccountID || revision != scope.AccountRevision ||
		scope.Plan.State().IsZero() {
		return application.ErrStale
	}

	next := scope.NotBefore.UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(
		ctx,
		profileStatement("ApproveRestartScope", ""),
		scope.Connection,
		scope.Stream,
		settings,
		account,
		revision,
		next,
		scope.Plan.From().String(),
		scope.Plan.To().String(),
		actor,
		at,
	); err != nil {
		return err
	}

	state := scope.Plan.State()
	if _, err := tx.ExecContext(ctx, profileStatement("SeedRestartWork", ""),
		scope.Connection, scope.Stream, state.Version(), state.Payload(), next); err != nil {
		return err
	}

	return q.SetScheduled(ctx, sqlc.SetScheduledParams{
		ConnectionID: scope.Connection, Stream: scope.Stream, Operation: "acquire", Enabled: 1,
	})
}

func reserveRecoveredReference(ctx context.Context, q *sqlc.Queries, floor time.Time) error {
	budget, err := q.ReferenceBudget(ctx)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}

	current, err := parseOptionalTime(budget.Cooldown)
	if err != nil || current.After(floor) {
		return err
	}

	return q.SetReferenceBudget(ctx, sqlc.SetReferenceBudgetParams{
		WindowStart: budget.WindowStart, Used: budget.Used, Cooldown: floor.UTC().Format(time.RFC3339Nano),
	})
}

func checkRestartScope(ctx context.Context, db associationReader, key host.WorkKey) (time.Time, error) {
	var (
		required bool
		next     string
	)
	if err := db.QueryRowContext(ctx, profileStatement("CheckRestartScope", ""), key.Connection, key.Stream, key.Operation).
		Scan(&required, &next); err != nil {
		return time.Time{}, err
	}

	if required && next == "" {
		return time.Time{}, provider.ErrUnavailable
	}

	return parseOptionalTime(next)
}
