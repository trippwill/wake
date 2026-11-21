// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"go.trippwill.dev/wake/internal/persistence/sqlite/sqlc"
)

type savedPacingDeadline struct {
	provider, policy, scope string
	at                      time.Time
}

type recoveryPacing struct {
	deadlines []savedPacingDeadline
	reference time.Time
}

func readRecoveryPacing(ctx context.Context, db *sql.DB) (recoveryPacing, error) {
	var result recoveryPacing

	rows, err := db.QueryContext(ctx, profileStatement("ReadPacingForRecovery", "main"))
	if err != nil {
		return result, err
	}

	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var (
			row  savedPacingDeadline
			next string
		)
		if err := rows.Scan(&row.provider, &row.policy, &row.scope, &next); err != nil {
			return result, err
		}

		row.at, err = parseOptionalTime(next)
		if err == nil && !row.at.IsZero() {
			result.deadlines = append(result.deadlines, row)
		}
	}

	if err := rows.Err(); err != nil {
		return result, err
	}

	var start, cooldown string

	err = db.QueryRowContext(ctx, profileStatement("ReadReferencePacingForRecovery", "main")).
		Scan(&start, &cooldown)
	if errors.Is(err, sql.ErrNoRows) {
		return result, nil
	}

	if err != nil {
		return result, err
	}

	window, _ := parseOptionalTime(start)
	result.reference, _ = parseOptionalTime(cooldown)
	if !window.IsZero() && window.Add(time.Minute).After(result.reference) {
		result.reference = window.Add(time.Minute)
	}

	return result, nil
}

func preserveRecoveryPacing(ctx context.Context, tx *sql.Tx, pacing recoveryPacing) error {
	for _, row := range pacing.deadlines {
		var next string

		err := tx.QueryRowContext(ctx, profileStatement("ExistingRecoveryPacing", ""), row.provider, row.policy, row.scope).
			Scan(&next)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}

		current, err := parseOptionalTime(next)
		if err != nil {
			return err
		}

		if !row.at.After(current) {
			continue
		}

		if _, err := tx.ExecContext(ctx, profileStatement("PreserveRecoveryPacing", ""),
			row.provider, row.policy, row.scope, row.at.UTC().Format(time.RFC3339Nano)); err != nil {
			return err
		}
	}

	if !pacing.reference.IsZero() {
		return reserveRecoveredReference(ctx, sqlc.New(tx), pacing.reference)
	}

	return nil
}
