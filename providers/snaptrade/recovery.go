// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package snaptrade

import (
	"time"

	"go.trippwill.dev/wake/financial"
	"go.trippwill.dev/wake/provider"
)

// The provider's launch guidance gives account activity a daily polling bound.
const recoveryActivitySpacing = 24 * time.Hour

func (a *adapter) PlanRestart(request provider.RestartRequest) (provider.RestartPlan, error) {
	if request.RecoveredAt.IsZero() {
		return provider.RestartPlan{}, provider.ErrInvalid
	}

	if _, err := a.PrepareSettings(request.Settings); err != nil {
		return provider.RestartPlan{}, err
	}

	scope, err := sourceScope(request.SourceAccount)
	if err != nil {
		return provider.RestartPlan{}, provider.ErrUnavailable
	}

	end, err := financial.ParseDate(request.RecoveredAt.UTC().Format(time.DateOnly))
	if err != nil {
		return provider.RestartPlan{}, err
	}

	cursor := scan{End: end.String(), Recovery: true}
	earliest := request.RecoveredAt.Add(recoveryActivitySpacing)
	switch request.Stream {
	case positionsID:
		if scope.Broker != fidelity {
			return provider.RestartPlan{}, provider.ErrUnavailable
		}

		state, err := cursor.state()
		if err != nil {
			return provider.RestartPlan{}, err
		}

		return provider.NewRestartPlan(
			state, financial.Date{}, financial.Date{}, earliest,
			"Verify the confirmed Fidelity account again and retrieve a current holdings snapshot.",
		)
	case streamID:
		start, err := recoveryActivityStart(request)
		if err != nil {
			return provider.RestartPlan{}, err
		}

		cursor.Start = start.String()

		state, err := cursor.state()
		if err != nil {
			return provider.RestartPlan{}, err
		}

		return provider.NewRestartPlan(
			state,
			start,
			end,
			earliest,
			"Verify the confirmed account again. Scan the fixed date range from offset zero and deduplicate overlapping activity. Older or unreported history can remain uncovered.",
		)
	default:
		return provider.RestartPlan{}, provider.ErrUnavailable
	}
}

func recoveryActivityStart(request provider.RestartRequest) (financial.Date, error) {
	today := request.RecoveredAt.UTC()
	start := today.AddDate(0, 0, -364)
	if !request.RetainedThrough.IsZero() {
		last := request.RetainedThrough
		if last.String() > today.Format(time.DateOnly) {
			return financial.Date{}, provider.ErrUnavailable
		}

		overlap := time.Date(last.Year(), last.Month(), last.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -6)
		if overlap.After(start) {
			start = overlap
		}
	}

	return financial.ParseDate(start.Format(time.DateOnly))
}
