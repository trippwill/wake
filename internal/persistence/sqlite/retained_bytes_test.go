// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"go.trippwill.dev/wake/financial"
	"go.trippwill.dev/wake/internal/persistence/sqlite/recordcodec"
	"go.trippwill.dev/wake/internal/persistence/sqlite/sqlc"
	"go.trippwill.dev/wake/provider"
	"go.trippwill.dev/wake/providers/ibkrflex"
	"go.trippwill.dev/wake/source"
)

func TestRetainedRecordBytesAndClaimIDs(t *testing.T) {
	s := newStoreWithSyntheticAccount(t)
	normalizeConfirmationReport(t, s, "retained-bytes", syntheticOption+syntheticOption)
	ctx := t.Context()
	account, _ := source.NewReference("synthetic", "account", "firm", "A1")
	target, _ := source.NewReference("synthetic", "event", "A1", "E1")
	ref, _ := source.NewReference("synthetic", "security", "listing", "D1")
	report, _ := source.NewReference("synthetic", "report", "A1", "P1")
	date, _ := financial.ParseDate("2026-11-02")
	currency, _ := financial.ParseCurrency("USD")
	quantity, _ := financial.ParseQuantity("12.0000000001234500")
	price, _ := financial.ParsePrice("98.876543210000", currency)

	debt, err := source.NewInstrument(source.InstrumentInput{Reference: ref, Kind: source.Debt, Ticker: "SYN"})
	if err != nil {
		t.Fatal(err)
	}

	observed, err := source.NewInstrumentObservation("instrument/1", debt, date)
	if err != nil {
		t.Fatal(err)
	}

	holding, err := source.NewPosition(debt, quantity, price)
	if err != nil {
		t.Fatal(err)
	}

	holding, err = holding.WithReportedUnits()
	if err != nil {
		t.Fatal(err)
	}

	positions, err := source.NewPositionSetObservation(source.PositionSetInput{
		Location: "holdings/1", Account: account, Reference: report, Date: date, Complete: true,
		Positions: []source.Position{holding},
	})
	if err != nil {
		t.Fatal(err)
	}

	correction, err := source.NewCorrection(source.CorrectionInput{
		Location: "corrections/1", Account: account, Target: target, Kind: source.Cancellation,
	})
	if err != nil {
		t.Fatal(err)
	}

	const version = "retained-golden/1"

	normalizer := claimNormalizer(
		func(ctx context.Context, _ provider.Artifact, c provider.Claims) (provider.Result, error) {
			if err := c.PutInstrument(ctx, observed); err != nil {
				return provider.Result{}, err
			}

			if err := c.PutPositionSet(ctx, positions); err != nil {
				return provider.Result{}, err
			}

			if err := c.PutCorrection(ctx, correction); err != nil {
				return provider.Result{}, err
			}

			if err := c.Reject(ctx, "rejections/1", "synthetic-unknown"); err != nil {
				return provider.Result{}, err
			}

			return provider.NewResult(
				provider.Counts{Rows: 5, Instruments: 2, PositionSets: 1, Corrections: 1, Rejected: 1},
			)
		},
	)

	if _, err = s.Normalize(ctx, "retained-bytes", version, normalizer); err != nil {
		t.Fatal(err)
	}

	var got []string
	for _, v := range []string{ibkrflex.Version, version} {
		rows, e := s.queries.Records(ctx, sqlc.RecordsParams{ArtifactID: "retained-bytes", Version: v})
		if e != nil {
			t.Fatal(e)
		}

		for _, row := range rows {
			got = append(
				got,
				fmt.Sprintf("%s\t%s\t%d\t%s\t%s", row.Kind, row.Location, row.Ordinal, row.ClaimID, row.Data),
			)
		}
	}

	actual := strings.Join(got, "\n") + "\n"

	want, err := os.ReadFile("testdata/retained_record_bytes.tsv")
	if err != nil {
		t.Fatal(err)
	}

	if actual != string(want) {
		t.Fatalf("retained kinds, locations, occurrence ordinals, claim IDs or JSON changed:\n%s", actual)
	}

	records, err := s.Records(ctx, "retained-bytes", version)
	if err != nil {
		t.Fatal(err)
	}

	for _, record := range records {
		if record.Kind == "positions" {
			f := record.PositionSet.Fields()
			if f.Location != record.Location || len(f.Positions) != 1 ||
				f.Positions[0].Unit() != "units" || f.Positions[0].Quantity().Value() != quantity.Value() ||
				f.Positions[0].UnitCostBasis().Amount() != price.Amount() {
				t.Fatal("position decode changed", f)
			}
		}
	}
}

func TestSourceClaimMalformedPositionBindingValidation(t *testing.T) {
	const kind = "position-instrument"

	s := newStoreWithSyntheticAccount(t)
	normalizeConfirmationReport(t, s, "retained-malformed", "")
	ctx := t.Context()

	c := claims{
		q: s.queries, artifact: "retained-malformed", version: "malformed/1",
		connection: "synthetic", stream: "confirmations", occurrences: map[string]int64{},
	}

	raw := []byte(
		`{"Account":["synthetic","account","","A1"],"Instrument":{"Kind":"retained-invalid","Identifiers":[{"Kind":"isin","Value":"invalid","Field":"isin"}]}}`,
	)
	if err := c.put(ctx, "binding/1", kind, raw); err != nil {
		t.Fatal(err)
	}

	records, err := s.Records(ctx, c.artifact, c.version)
	if err != nil || len(records) != 1 {
		t.Fatal(records, err)
	}

	p := retainedProcessing{q: s.queries, id: c.artifact, version: c.version}
	if _, err = p.RetainedClaims(ctx); !errors.Is(err, source.ErrInvalidInstrument) {
		t.Fatal("retained binding did not defer instrument validation", err)
	}

	for _, invalid := range []struct {
		kind string
		data string
	}{{kind, `{"Instrument":true}`}, {"activity", `{"Fields":true}`}} {
		if _, err := materializeRecords(
			[]sqlc.RecordValue{{Kind: invalid.kind, Data: []byte(invalid.data)}},
			nil,
		); !errors.Is(
			err,
			ErrCorrupt,
		) {
			t.Fatal(invalid.kind, err)
		}
	}
}

func TestPositionBindingCanonicalKeyUsesRetainedShape(t *testing.T) {
	raw := []byte(
		`{"Account":["synthetic","account","","A1"],"Instrument":{"Kind":"option","AsOf":"as-reported","Option":{"Underlier":{"Kind":"equity","Identifiers":[{"Kind":"isin","Value":"Z","Field":"second"},{"Kind":"isin","Value":"A","Field":"first"},{"Kind":"isin","Value":"A","Field":"first"}]}}}}`,
	)

	key, kind, err := recordcodec.PositionBindingKey(raw)
	if err != nil {
		t.Fatal(err)
	}

	want := `{"Reference":["","","",""],"Kind":"option","Ticker":"","Name":"","Venue":"","AsOf":"as-reported","Identifiers":null,"Option":{"Underlier":{"Reference":["","","",""],"Kind":"equity","Ticker":"","Name":"","Venue":"","AsOf":"","Identifiers":[{"Kind":"isin","Value":"A","Field":"first"},{"Kind":"isin","Value":"A","Field":"first"},{"Kind":"isin","Value":"Z","Field":"second"}],"Option":null,"Debt":null},"Right":"","Expiry":"","EffectiveFrom":"","Multiplier":"","Strike":{"Amount":"","Currency":""},"Deliverables":null},"Debt":null}`
	if key != want || kind != source.Option {
		t.Fatal("position binding key or reported kind changed", key, kind)
	}
}
