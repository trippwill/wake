// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/host"
	"go.trippwill.dev/wake/internal/persistence/sqlite/sqlc"
	"go.trippwill.dev/wake/provider"
)

func activityState(row sqlc.Execution) (application.ActivityState, error) {
	if row.ReviewReasons < 0 || row.ReviewReasons > 3 {
		return application.ActivityState{}, ErrCorrupt
	}

	state, err := application.NewActivityState(
		application.LifecycleState(row.Status),
		application.ReviewReasons(row.ReviewReasons),
		application.SelectionAuthority(row.SelectionAuthority),
	)
	if err != nil {
		return application.ActivityState{}, ErrCorrupt
	}

	return state, nil
}

func workFailureCode(code provider.RetryReason) host.FailureCode {
	if code == "" {
		return ""
	}

	return host.FailureTemporary
}
