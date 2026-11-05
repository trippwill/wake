// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package providerproof_test

import (
	"context"
	fixture "example.org/wakefixture"
	"go.trippwill.dev/wake/provider"
	"testing"
)

func TestExternalPositionSetsRoundTripThroughPublicClaims(t *testing.T) {
	base := registration(t)
	n := normalizeFunc(func(ctx context.Context, _ provider.Artifact, c provider.Claims) (provider.Result, error) {
		set, err := fixture.Positions()
		if err != nil {
			return provider.Result{}, err
		}
		if err = c.PutPositionSet(ctx, set); err != nil {
			return provider.Result{}, err
		}
		return provider.NewResult(provider.Counts{Rows: 2, PositionSets: 1, Instruments: 1})
	})
	r, err := provider.NewRegistration(base.Descriptor(), "positions-1", fixture.Provider{}, n, fixture.Provider{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	f := newFixture(t, r)
	id, err := f.host.Acquire(t.Context(), f.id, "statements")
	if err != nil {
		t.Fatal(err)
	}
	result, err := f.host.Normalize(t.Context(), id, "positions-1")
	if err != nil || result.Counts().PositionSets != 1 {
		t.Fatal(result, err)
	}
	records, err := f.store.Records(t.Context(), id, "positions-1")
	if err != nil || len(records) != 2 {
		t.Fatal(records, err)
	}
	found := false
	for _, r := range records {
		if r.Kind == "positions" {
			found = true
			v := r.PositionSet.Fields()
			if v.At.String() != "2026-10-20T12:34:56.1200Z" || v.Positions[0].Quantity().Value() != "-2.00000000000000100" || v.Positions[0].CostBasis().Amount() != "0.0000" {
				t.Fatal(v)
			}
		}
	}
	if !found {
		t.Fatal("missing typed positions claim")
	}
}
