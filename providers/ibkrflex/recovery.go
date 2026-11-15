// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package ibkrflex

import (
	"time"

	"go.trippwill.dev/wake/financial"
	"go.trippwill.dev/wake/provider"
)

func (s *Source) PlanRestart(request provider.RestartRequest) (provider.RestartPlan, error) {
	if request.RecoveredAt.IsZero() || request.SourceAccount.Authority() != "ibkr" ||
		request.SourceAccount.Kind() != "account" {
		return provider.RestartPlan{}, provider.ErrUnavailable
	}

	queryKey, err := flexQueryKey(request.Stream)
	if err != nil {
		return provider.RestartPlan{}, err
	}

	query, ok := request.Settings.Get(queryKey)
	if !ok || !digits(query) || len(query) > 64 {
		return provider.RestartPlan{}, provider.ErrInvalid
	}

	end := civil(request.RecoveredAt)
	start := end.AddDate(0, 0, -364)
	if !request.RetainedThrough.IsZero() {
		retained := request.RetainedThrough

		overlap := time.Date(retained.Year(), retained.Month(), retained.Day(), 0, 0, 0, 0, time.UTC)
		if overlap.After(end) {
			return provider.RestartPlan{}, provider.ErrUnavailable
		}

		if candidate := overlap.AddDate(0, 0, -6); candidate.After(start) {
			start = candidate
		}
	}

	dates := window{start.Format(time.DateOnly), end.Format(time.DateOnly)}
	from, _ := financial.ParseDate(dates.From)
	to, _ := financial.ParseDate(dates.To)
	state := continuation{Query: query, Started: true, Current: &dates, Recovery: true}

	// Flex limits a token to ten requests within a minute.
	earliest := request.RecoveredAt.Add(time.Minute)

	step, err := state.step(provider.Artifact{}, earliest)
	if err != nil {
		return provider.RestartPlan{}, err
	}

	return provider.NewRestartPlan(
		step.State(),
		from,
		to,
		earliest,
		"Request a fresh Flex report. Overlap up to seven retained days and deduplicate retained activity. Older dates and missing report sections can remain uncovered.",
	)
}

func flexQueryKey(stream string) (string, error) {
	switch stream {
	case Confirmations:
		return "query_id", nil
	case Activity:
		return "activity_query_id", nil
	default:
		return "", provider.ErrUnavailable
	}
}
