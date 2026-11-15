// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"time"

	"go.trippwill.dev/wake/financial"
	"go.trippwill.dev/wake/source"
)

// RestartPlanner is optional on an Acquirer. It creates a fresh continuation
// without contacting the provider or consulting an old execution checkpoint.
type RestartPlanner interface {
	PlanRestart(RestartRequest) (RestartPlan, error)
}

type RestartRequest struct {
	Stream          string
	Settings        Settings
	SourceAccount   source.Reference
	RetainedThrough financial.Date
	RecoveredAt     time.Time
}

type RestartPlan struct {
	state       State
	from, to    financial.Date
	notBefore   time.Time
	description string
}

func NewRestartPlan(
	state State, from, to financial.Date, notBefore time.Time, description string,
) (RestartPlan, error) {
	if state.IsZero() || from.IsZero() != to.IsZero() || from.String() > to.String() ||
		notBefore.IsZero() || !validText(description, 1024) {
		return RestartPlan{}, ErrInvalid
	}

	return RestartPlan{state, from, to, notBefore.Round(0).UTC(), description}, nil
}

func (p RestartPlan) State() State         { return p.state }
func (p RestartPlan) From() financial.Date { return p.from }
func (p RestartPlan) To() financial.Date   { return p.to }
func (p RestartPlan) NotBefore() time.Time { return p.notBefore }
func (p RestartPlan) Description() string  { return p.description }
