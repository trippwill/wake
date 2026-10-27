// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"slices"

	"go.trippwill.dev/wake/financial"
	"go.trippwill.dev/wake/source"
)

// Discoverer returns bounded source candidates; it grants no financial authority.
type (
	Discoverer interface {
		Discover(context.Context, Request, Access) (Discovery, error)
	}
	Candidate struct {
		reference source.Reference
		name      string
		currency  financial.Currency
	}
)

func NewCandidate(ref source.Reference, name string, currency financial.Currency) (Candidate, error) {
	if ref.IsZero() || (name != "" && !validText(name, 1024)) {
		return Candidate{}, ErrInvalid
	}

	return Candidate{ref, name, currency}, nil
}
func (c Candidate) Reference() source.Reference  { return c.reference }
func (c Candidate) Name() string                 { return c.name }
func (c Candidate) Currency() financial.Currency { return c.currency }

type Discovery struct {
	step       Step
	candidates []Candidate
}

func NewDiscovery(step Step, candidates []Candidate) (Discovery, error) {
	if step.NextEligibleAt().IsZero() || len(candidates) > 1024 {
		return Discovery{}, ErrLimit
	}

	if len(candidates) > 0 && step.Artifact().IsZero() {
		return Discovery{}, ErrInvalid
	}

	seen := map[source.Reference]bool{}
	for _, c := range candidates {
		if c.reference.IsZero() || seen[c.reference] {
			return Discovery{}, ErrInvalid
		}

		seen[c.reference] = true
	}

	return Discovery{step, slices.Clone(candidates)}, nil
}
func (d Discovery) Step() Step              { return d.step }
func (d Discovery) Candidates() []Candidate { return slices.Clone(d.candidates) }
