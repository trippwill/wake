// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package ibkrflex

import (
	"context"
	"sort"
	"time"

	"go.trippwill.dev/wake/financial"
	"go.trippwill.dev/wake/provider"
	"go.trippwill.dev/wake/source"
)

func (s *Source) Discover(
	ctx context.Context,
	request provider.Request,
	access provider.Access,
) (provider.Discovery, error) {
	// One protocol attempt only; discovery never expands unavailable ranges.
	step, err := s.acquire(ctx, request, access, true)
	if err != nil {
		return provider.Discovery{}, err
	}

	if step.Artifact().IsZero() {
		return provider.NewDiscovery(step, nil)
	}

	type profile struct {
		name, currency                 string
		nameConflict, currencyConflict bool
	}

	profiles := map[string]profile{}

	err = walk(ctx, step.Artifact().Content(), func(path []node) error {
		n := path[len(path)-1]

		id := n.attributes["accountId"]
		if id == "" {
			return nil
		}

		if !safeText(id, 1024) {
			return provider.ErrMalformed
		}

		p := profiles[id]
		// Trade currency is never account profile currency.
		if n.name == "AccountInformation" || n.name == "FlexStatement" {
			name := n.attributes["acctAlias"]
			if name == "" {
				name = n.attributes["accountName"]
			}

			currency := n.attributes["baseCurrency"]

			if name != "" {
				if p.name != "" && p.name != name {
					p.nameConflict = true
				}

				p.name = name
			}

			if currency != "" {
				if p.currency != "" && p.currency != currency {
					p.currencyConflict = true
				}

				p.currency = currency
			}
		}

		profiles[id] = p
		if len(profiles) > 1024 {
			return provider.ErrLimit
		}

		return nil
	})
	if err != nil {
		return provider.Discovery{}, err
	}

	ids := make([]string, 0, len(profiles))
	for id := range profiles {
		ids = append(ids, id)
	}

	sort.Strings(ids)

	candidates := make([]provider.Candidate, 0, len(ids))
	for _, id := range ids {
		p := profiles[id]
		if p.nameConflict {
			p.name = ""
		}

		if p.currencyConflict {
			p.currency = ""
		}

		ref, e := source.NewReference("ibkr", "account", "", id)
		if e != nil {
			return provider.Discovery{}, e
		}

		currency := financial.Currency{}
		if p.currency != "" {
			currency, _ = financial.ParseCurrency(p.currency)
		}

		candidate, e := provider.NewCandidate(ref, p.name, currency)
		if e != nil {
			return provider.Discovery{}, e
		}

		candidates = append(candidates, candidate)
	}

	done, err := provider.NewStep(provider.State{}, step.Artifact(), request.Now.Add(24*time.Hour))
	if err != nil {
		return provider.Discovery{}, err
	}

	done, err = done.WithCoverage(step.Coverage())
	if err != nil {
		return provider.Discovery{}, err
	}

	return provider.NewDiscovery(done, candidates)
}
