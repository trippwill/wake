// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package providerproof_test

import (
	"context"
	"testing"

	fixture "example.org/wakefixture"
	"go.trippwill.dev/wake/provider"
	"go.trippwill.dev/wake/source"
)

func TestExternalMovementClaimRoundTrips(t *testing.T) {
	base := registration(t)
	n := normalizeFunc(func(ctx context.Context, _ provider.Artifact, c provider.Claims) (provider.Result, error) {
		transfer, err := fixture.SecurityTransfer()
		if err != nil {
			return provider.Result{}, err
		}
		cash, err := fixture.CashActivity()
		if err != nil {
			return provider.Result{}, err
		}
		if err = c.PutActivity(ctx, transfer.Observation()); err != nil {
			return provider.Result{}, err
		}
		if err = c.PutActivity(ctx, cash.Observation()); err != nil {
			return provider.Result{}, err
		}
		return provider.NewResult(provider.Counts{Rows: 2, Activities: 2})
	})
	r, err := provider.NewRegistration(base.Descriptor(), "movements-1", fixture.Provider{}, n, fixture.Provider{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	f := newFixture(t, r)
	id, err := f.host.Acquire(t.Context(), f.id, "statements")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.host.Normalize(t.Context(), id, "movements-1"); err != nil {
		t.Fatal(err)
	}
	records, err := f.store.Records(t.Context(), id, "movements-1")
	if err != nil || len(records) != 2 {
		t.Fatal(records, err)
	}
	seen := map[string]bool{}
	for _, record := range records {
		family, _ := record.Observation.Field(source.FieldFamily)
		switch family.Fields().Text {
		case "security-transfer":
			v, e := record.Observation.SecurityTransfer()
			if e != nil || v.Fields().Direction != "in" {
				t.Fatal(v, e)
			}
			seen["transfer"] = true
		case "cash":
			v, e := record.Observation.CashActivity()
			if e != nil || v.Fields().Amount.Amount() != "-0.000000000000000012300" {
				t.Fatal(v, e)
			}
			seen["cash"] = true
		}
	}
	if len(seen) != 2 {
		t.Fatal("missing independent movement claims", seen)
	}
}
