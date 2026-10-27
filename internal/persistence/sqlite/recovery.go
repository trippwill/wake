// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0
package persistence

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"sort"
	"time"

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/logging"
	"go.trippwill.dev/wake/internal/persistence/sqlite/sqlc"
)

const maxRecoveryGroup = 128

func (s *Store) RetryProcessing(ctx context.Context, id string) error {
	n, err := s.queries.RetryProcessing(ctx, id)
	if err == nil && n != 1 {
		return application.ErrInvalid
	}

	return err
}

func (s *Store) RetryRecovery(ctx context.Context, id string) error {
	n, err := s.queries.RetryRecovery(ctx, id)
	if err == nil && n != 1 {
		return application.ErrInvalid
	}

	return err
}

func (s *Store) StartRebuild(ctx context.Context, submission, actor string, at time.Time) (string, error) {
	if len(submission) < 16 || len(submission) > 128 || actor == "" || at.IsZero() {
		return "", application.ErrInvalid
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}

	defer func() { _ = tx.Rollback() }()

	q := s.queries.WithTx(tx)
	if job, e := q.RecoveryJob(ctx, submission); e == nil {
		return job.ID, nil
	} else if !errors.Is(e, sql.ErrNoRows) {
		return "", e
	}

	if job, e := q.ActiveRecovery(ctx); e == nil {
		return job.ID, nil
	} else if !errors.Is(e, sql.ErrNoRows) {
		return "", e
	}

	rows, err := q.Executions(ctx)
	if err != nil {
		return "", err
	}

	instruments, err := q.Instruments(ctx)
	if err != nil {
		return "", err
	}
	// Explicit relationships and correction endpoints define atomic activity groups.
	adj := map[string][]string{}
	for _, r := range rows {
		adj[r.ID] = nil
	}

	links, err := q.AllRelationships(ctx)
	if err != nil {
		return "", err
	}

	for _, r := range links {
		if r.TargetID != "" {
			adj[r.ActivityID] = append(adj[r.ActivityID], r.TargetID)
			adj[r.TargetID] = append(adj[r.TargetID], r.ActivityID)
		}
	}

	corrections, err := q.SourceCorrections(ctx)
	if err != nil {
		return "", err
	}

	for _, c := range corrections {
		a, e := q.EventLinks(ctx, sqlc.EventLinksParams{AccountID: c.AccountID, Reference: c.Target})
		if e != nil {
			return "", e
		}

		b, e := q.EventLinks(ctx, sqlc.EventLinksParams{AccountID: c.AccountID, Reference: c.Replacement})
		if e != nil {
			return "", e
		}

		ids := append(append([]string{}, a...), b...)
		for _, id := range ids {
			adj[id] = append(adj[id], ids...)
		}
	}

	groups := map[string][]string{}
	seen := map[string]bool{}

	ids := make([]string, 0, len(adj))
	for id := range adj {
		ids = append(ids, id)
	}

	sort.Strings(ids)

	for _, root := range ids {
		if seen[root] {
			continue
		}

		queue := []string{root}
		members := []string{}
		for len(queue) > 0 {
			id := queue[0]
			queue = queue[1:]

			if seen[id] {
				continue
			}

			seen[id] = true

			members = append(members, id)
			if len(members) > maxRecoveryGroup {
				return "", application.ErrRecoveryGroup
			}

			queue = append(queue, adj[id]...)
		}

		sort.Strings(members)
		groups[members[0]] = members
	}

	if err = q.PutRecovery(
		ctx,
		sqlc.PutRecoveryParams{
			ID:      submission,
			Total:   int64(len(rows) + len(instruments)),
			Actor:   actor,
			Created: at.UTC().Format(time.RFC3339Nano),
		},
	); err != nil {
		return "", err
	}

	for group, members := range groups {
		for _, id := range members {
			if err = q.PutRecoveryItem(
				ctx,
				sqlc.PutRecoveryItemParams{
					JobID:    submission,
					GroupID:  "activity/" + group,
					Kind:     "activity",
					EntityID: id,
				},
			); err != nil {
				return "", err
			}
		}
	}

	for _, i := range instruments {
		if err = q.PutRecoveryItem(
			ctx,
			sqlc.PutRecoveryItemParams{
				JobID:    submission,
				GroupID:  "instrument/" + i.ID,
				Kind:     "instrument",
				EntityID: i.ID,
			},
		); err != nil {
			return "", err
		}
	}

	if len(rows)+len(instruments) == 0 {
		if err = q.FinishRecovery(ctx, submission); err != nil {
			return "", err
		}
	}

	if err = tx.Commit(); err != nil {
		return "", err
	}

	return submission, nil
}

func (s *Store) RunRecovery(ctx context.Context) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()

	q := s.queries.WithTx(tx)

	job, err := q.ActiveRecovery(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}

	if err != nil {
		return false, err
	}

	s.recoveryMu.Lock()
	if s.recoveryJob != job.ID {
		s.recoveryJob, s.recoveryOperation, s.recoveryNext = job.ID, rand.Text(), time.Time{}
	}

	logger := s.logger.With("operation_id", s.recoveryOperation)
	s.recoveryMu.Unlock()

	report := func(completed int64, done bool) {
		s.recoveryMu.Lock()
		defer s.recoveryMu.Unlock()

		if !done && time.Now().Before(s.recoveryNext) {
			return
		}

		s.recoveryNext = time.Now().Add(30 * time.Second)

		event, outcome := "rebuild_progress", "running"
		if done {
			event, outcome = "rebuild_completed", "completed"
		}

		logger.InfoContext(ctx, "projection rebuild progress", "event", event, "outcome", outcome,
			"processed", completed, "total", job.Total)
	}

	stop := logging.Progress(ctx, logger, "rebuild_progress", 30*time.Second)
	defer stop()

	failed := func(cause error) (bool, error) {
		_ = tx.Rollback()

		if ctx.Err() != nil {
			return false, ctx.Err()
		}

		if e := s.queries.FailRecovery(ctx, job.ID); e != nil {
			return false, e
		}

		logger.WarnContext(
			ctx,
			"projection rebuild paused",
			"event",
			"rebuild_paused",
			"code",
			logging.Code(cause),
		)

		return true, cause
	}
	group, err := q.NextRecoveryGroup(ctx, job.ID)
	if errors.Is(err, sql.ErrNoRows) {
		if err = q.FinishRecovery(ctx, job.ID); err != nil {
			return failed(err)
		}

		if err = tx.Commit(); err != nil {
			return true, err
		}

		report(job.Total, true)

		return true, nil
	}

	if err != nil {
		return failed(err)
	}

	members, err := q.RecoveryGroup(ctx, sqlc.RecoveryGroupParams{JobID: job.ID, GroupID: group})
	if err != nil {
		return failed(err)
	}

	if len(members) > maxRecoveryGroup {
		return failed(application.ErrRecoveryGroup)
	}

	entities := map[string]bool{}
	for _, m := range members {
		if m.Kind == "activity" {
			entities[m.EntityID] = true
		}
	}

	states, err := correctionStatesFor(ctx, q, entities)
	if err != nil {
		return failed(err)
	}

	for _, m := range members {
		if m.Kind == "instrument" {
			err = projectInstrument(ctx, q, m.EntityID)
		} else {
			var row sqlc.Execution

			row, err = q.Execution(ctx, m.EntityID)
			if err == nil {
				err = projectActivity(ctx, q, row, states[row.ID])
			}
		}

		if err != nil {
			return failed(err)
		}
	}

	if err = q.CompleteRecoveryGroup(
		ctx,
		sqlc.CompleteRecoveryGroupParams{JobID: job.ID, GroupID: group},
	); err != nil {
		return failed(err)
	}

	if err = q.AdvanceRecovery(ctx, job.ID); err != nil {
		return failed(err)
	}

	complete := false
	if _, e := q.NextRecoveryGroup(ctx, job.ID); errors.Is(e, sql.ErrNoRows) {
		if err = q.FinishRecovery(ctx, job.ID); err != nil {
			return failed(err)
		}

		complete = true
	} else if e != nil {
		return failed(e)
	}

	if err = tx.Commit(); err != nil {
		return failed(err)
	}

	logger.DebugContext(
		ctx,
		"projection rebuild group committed",
		"event",
		"rebuild_group",
		"members",
		len(members),
	)
	report(job.Completed+int64(len(members)), complete)

	return true, nil
}
