// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"time"

	"go.trippwill.dev/wake/internal/upstream"
	"go.trippwill.dev/wake/provider"
)

type upstreamAssociation struct {
	revision, granted               string
	paused, epochMatches, restoring bool
}

type associationReader interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func readUpstreamAssociation(ctx context.Context, db associationReader) (upstreamAssociation, error) {
	var a upstreamAssociation

	err := db.QueryRowContext(ctx, profileStatement("VerifyUpstreamWitness", "")).Scan(
		&a.revision, &a.paused, &a.granted, &a.epochMatches, &a.restoring,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrAssociation
	}

	return a, err
}

func (a upstreamAssociation) admitted(ctx context.Context) error {
	if !a.epochMatches || a.restoring {
		return ErrAssociation
	}

	if a.paused || a.granted != a.revision {
		return provider.ErrUnavailable
	}

	if expected := upstream.Revision(ctx); expected != "" && expected != a.revision {
		return provider.ErrConflict
	}

	return nil
}

func (s *Store) UpstreamRevision(ctx context.Context) (string, error) {
	a, err := readUpstreamAssociation(ctx, s.db)
	if err != nil {
		return "", err
	}

	if s.credentialKeyUnavailable.Load() {
		return "", provider.ErrUnavailable
	}

	return a.revision, a.admitted(ctx)
}

func beginUpstream(ctx context.Context, db *sql.DB) (*sql.Tx, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}

	a, err := readUpstreamAssociation(ctx, tx)
	if err == nil {
		if !a.epochMatches || a.restoring {
			err = ErrAssociation
		}

		if upstream.Revision(ctx) != "" {
			err = a.admitted(ctx)
		}
	}

	if err != nil {
		_ = tx.Rollback()
		return nil, err
	}

	return tx, nil
}

type upstreamMutation uint8

const (
	upstreamProgress  upstreamMutation = 0
	upstreamAuthority upstreamMutation = 1
	upstreamFinancial upstreamMutation = 2
)

// commitUpstream updates both witnesses before committing the operational writes
// and any financial publication. Authority changes invalidate in-flight work;
// deadline and continuation changes rotate only the safety token.
func (s *Store) commitUpstream(ctx context.Context, tx *sql.Tx, mutation upstreamMutation) error {
	var revision any
	if mutation&upstreamAuthority != 0 {
		revision = rand.Text()

		if _, err := tx.ExecContext(ctx, profileStatement("InvalidateRecoveryPreview", "")); err != nil {
			return err
		}
	}

	if _, err := tx.ExecContext(ctx, profileStatement("RotateSync", ""), rand.Text(), revision); err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx, profileStatement("RotateWitness", "")); err != nil {
		return err
	}

	if mutation&upstreamFinancial != 0 {
		return s.commit(ctx, tx)
	}

	return tx.Commit()
}

func bindUpstream(ctx context.Context, tx *sql.Tx, metadata [2]storeMetadata, fresh, recovering bool) error {
	lineage := metadata[0].id
	if !fresh {
		var restoring bool
		if err := tx.QueryRowContext(ctx, profileStatement("Association", "")).
			Scan(&lineage, &restoring); err != nil {
			return err
		}

		if !recovering {
			if !restoring {
				return nil
			}

			if _, err := readUpstreamAssociation(ctx, tx); err != nil {
				return err
			}
		}
	}

	revision := rand.Text()
	if _, err := tx.ExecContext(
		ctx,
		profileStatement("BindSync", ""),
		lineage,
		metadata[0].id,
		rand.Text(),
		metadata[1].id,
		rand.Text(),
		revision,
		rand.Text(),
	); err != nil {
		return err
	}

	var actor, at, grant string
	if fresh && !recovering {
		actor, at, grant = "installation", time.Now().UTC().Format(time.RFC3339Nano), revision
	}

	_, err := tx.ExecContext(ctx, profileStatement("BindGrant", ""), grant, actor, at)

	return err
}
