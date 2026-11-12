// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package providerproof_test

import (
	"context"
	"testing"

	fixture "example.org/wakefixture"
	persistence "go.trippwill.dev/wake/internal/persistence/sqlite"
	"go.trippwill.dev/wake/provider"
	"go.trippwill.dev/wake/source"
)

func TestExternalFinancialClaimsRoundTripThroughNormalization(t *testing.T) {
	option, err := fixture.Option()
	if err != nil {
		t.Fatal(err)
	}
	lifecycle, err := fixture.Lifecycle()
	if err != nil {
		t.Fatal(err)
	}
	conversion, err := fixture.Conversion()
	if err != nil {
		t.Fatal(err)
	}
	correction, err := fixture.Correction()
	if err != nil {
		t.Fatal(err)
	}

	activities := []source.ActivityObservation{option.Observation(), lifecycle.Observation(), conversion.Observation()}
	debtKinds := []source.DebtType{source.TreasuryNote, source.TreasuryBond, source.ConventionalGilt}
	for _, kind := range debtKinds {
		trade, e := fixture.Debt(kind)
		if e != nil {
			t.Fatal(e)
		}
		activities = append(activities, trade.Observation())
	}

	normalize := normalizeFunc(func(ctx context.Context, _ provider.Artifact, claims provider.Claims) (provider.Result, error) {
		for _, activity := range activities {
			if e := claims.PutActivity(ctx, activity); e != nil {
				return provider.Result{}, e
			}
		}
		if e := claims.PutCorrection(ctx, correction); e != nil {
			return provider.Result{}, e
		}
		count := uint64(len(activities))
		return provider.NewResult(provider.Counts{Rows: count + 1, Activities: count, Corrections: 1})
	})

	base := registration(t)
	reg, err := provider.NewRegistration(base.Descriptor(), "v1", fixture.Provider{}, normalize, fixture.Provider{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	f := newFixture(t, reg)
	id, err := f.host.Acquire(t.Context(), f.id, "statements")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.host.Normalize(t.Context(), id, "v1"); err != nil {
		t.Fatal(err)
	}
	records, err := f.store.Records(t.Context(), id, "v1")
	if err != nil || len(records) != len(activities)+1 {
		t.Fatal(records, err)
	}

	byLocation := make(map[string]persistence.Record, len(records))
	for _, record := range records {
		byLocation[record.Location] = record
	}

	t.Run("adjusted option", func(t *testing.T) {
		terms := observedTrade(t, byLocation["option:1"].Observation).Fields().Instrument.Option().Fields()
		if terms.Strike.Amount() != "123.4500000000000000001" || terms.Multiplier.Value() != "10" ||
			len(terms.Deliverables) != 2 || terms.Deliverables[0].Quantity().Value() != "12.5" ||
			terms.Deliverables[1].Cash().Amount() != "0.75" || terms.EffectiveFrom.String() != "2026-10-01" {
			t.Fatal("adjusted terms lost", terms)
		}
	})
	t.Run("lifecycle", func(t *testing.T) {
		terms := observedLifecycle(t, byLocation["lifecycle:1"].Observation).Fields()
		if terms.Kind != source.Exercise || len(terms.Relationships) != 1 {
			t.Fatal("lifecycle terms lost", terms)
		}
	})
	for _, kind := range debtKinds {
		t.Run(string(kind), func(t *testing.T) {
			trade := observedTrade(t, byLocation["debt:"+string(kind)].Observation)
			terms := trade.Fields()
			debt := terms.Instrument.Debt().Fields()
			if debt.Type != kind || debt.CouponRate.Value() != "4.125000000000001" ||
				trade.QuotationBasis() != "" || terms.AccruedInterest.Amount() != "-12.345" {
				t.Fatal("debt terms lost", terms)
			}
		})
	}
	t.Run("conversion", func(t *testing.T) {
		terms := observedConversion(t, byLocation["fx:1"].Observation).Fields()
		if terms.Sent.Amount() != "10.1234567890000000001" || terms.Received.Amount() != "" ||
			len(terms.Charges) != 1 || terms.Charges[0].Total().Currency().String() != "GBP" ||
			len(terms.Charges[0].Components()) != 1 || terms.Charges[0].Components()[0].Total().Amount() != "" ||
			len(terms.Charges[0].Components()[0].Components()) != 1 {
			t.Fatal("conversion roles lost", terms)
		}
	})
	t.Run("correction", func(t *testing.T) {
		terms := byLocation["correction:1"].Correction.Fields()
		if terms.Kind != source.Replacement || terms.Target.Value() != "original" ||
			terms.Replacement.Value() != "option-1" {
			t.Fatal("correction references lost", terms)
		}
	})
}
