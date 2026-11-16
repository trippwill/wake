// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"go.trippwill.dev/wake/internal/application/calculation"
	"go.trippwill.dev/wake/internal/host"
	"go.trippwill.dev/wake/internal/persistence/sqlite/sqlc"
	"go.trippwill.dev/wake/provider"
)

func activityState(row sqlc.Execution) (calculation.ActivityState, error) {
	if row.ReviewReasons < 0 || row.ReviewReasons > 3 {
		return calculation.ActivityState{}, ErrCorrupt
	}

	state, err := calculation.NewActivityState(
		calculation.LifecycleState(row.Status),
		calculation.ReviewReasons(row.ReviewReasons),
		calculation.SelectionAuthority(row.SelectionAuthority),
	)
	if err != nil {
		return calculation.ActivityState{}, ErrCorrupt
	}

	return state, nil
}

func workFailureCode(code provider.RetryReason) host.FailureCode {
	if code == "" {
		return ""
	}

	return host.FailureTemporary
}
