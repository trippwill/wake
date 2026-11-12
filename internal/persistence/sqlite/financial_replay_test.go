// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"go.trippwill.dev/wake/financial"
	"go.trippwill.dev/wake/internal/persistence/sqlite/sqlc"
	"go.trippwill.dev/wake/provider"
	"go.trippwill.dev/wake/providers/ibkrflex"
	"go.trippwill.dev/wake/source"
)

func TestOptionIdentityHistoricalTermsAndReplay(t *testing.T) {
	ctx := context.Background()
	s := newStoreWithSyntheticAccount(t)
	processConfirmationReport(t, s, "first", syntheticOption+equityRow("U1", "underlier-fill", "100"))

	view, err := s.Dashboard(ctx)
	if err != nil || len(view.Executions) != 2 || len(view.Instruments) != 2 {
		t.Fatal(view, err)
	}

	original := view.Executions[1]
	f := original.Terms().Fields().Instrument.Option().Fields()
	if original.Terms().QuantityUnit() != "contracts" || f.Multiplier.Value() != "10" ||
		f.Strike.Amount() != "100.00000000001" ||
		len(f.Deliverables) != 0 {
		t.Fatal("invented or lost option terms", f)
	}

	detail, err := s.InstrumentDetail(ctx, original.Instrument())
	if err != nil || detail.Underlier == "" || detail.Underlier == original.Instrument() ||
		len(detail.Listings) != 1 {
		t.Fatal("underlier and contract collapsed", detail, err)
	}

	processConfirmationReport(t, s, "overlap", syntheticOption)

	view, err = s.Dashboard(ctx)
	if err != nil || len(view.Executions) != 2 || view.Executions[1].ID() != original.ID() {
		t.Fatal("duplicate option", err)
	}

	records, err := s.Records(ctx, "first", ibkrflex.Version)
	if err != nil ||
		observedTrade(t, records[0].Observation).Fields().Instrument.Option().Fields().Strike != f.Strike {
		t.Fatal("retained terms", err)
	}
}

func TestLifecycleRelationshipsResolveWithoutInventingBookings(t *testing.T) {
	ctx := context.Background()
	s := newStoreWithSyntheticAccount(t)
	processConfirmationReport(t, s, "option", syntheticOption)

	view, err := s.Dashboard(ctx)
	if err != nil {
		t.Fatal(err)
	}

	f := view.Executions[0].Terms().Fields()
	event, _ := source.NewReference("ibkr", "trade-id", "U1", "exercise-1")
	target, _ := source.NewReference("ibkr", "trade-id", "U1", "Tbooking")
	link, _ := source.NewRelationship(source.StockBookingLink, target)

	l, err := source.NewLifecycle(
		source.LifecycleInput{
			Location:      "synthetic:exercise",
			Account:       f.Account,
			Event:         event,
			Instrument:    f.Instrument,
			Kind:          source.Exercise,
			Quantity:      f.Quantity,
			Date:          f.Date,
			Relationships: []source.Relationship{link},
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	n := claimNormalizer(
		func(ctx context.Context, _ provider.Artifact, c provider.Claims) (provider.Result, error) {
			if e := c.PutActivity(ctx, l.Observation()); e != nil {
				return provider.Result{}, e
			}

			return provider.NewResult(provider.Counts{Rows: 1, Activities: 1})
		},
	)
	processSyntheticClaims(t, s, "exercise", n)

	view, err = s.Dashboard(ctx)
	if err != nil || len(view.Lifecycles) != 1 || len(view.Executions) != 1 {
		t.Fatal(view, err)
	}

	id := view.Lifecycles[0].ID()

	d, err := s.LifecycleDetail(ctx, id)
	if err != nil || len(d.Relationships) != 1 || d.Relationships[0].TargetID != "" {
		t.Fatal("unresolved endpoint", d, err)
	}

	baseline, err := snapshotState(ctx, s)
	if err != nil {
		t.Fatal(err)
	}

	publishedBefore, err := s.Resource(ctx, "activity", id)
	if err != nil || len(publishedBefore.Activity.Relationships) != 1 ||
		publishedBefore.Activity.Relationships[0].TargetID != nil {
		t.Fatal("unresolved public endpoint", err)
	}

	booking := strings.ReplaceAll(
		equityRow("U1", "booking", "100"),
		`transactionType="ExchTrade"`,
		`transactionType="BookTrade"`,
	)
	booking = strings.Replace(booking, `code="O"`, `code="Ex"`, 1)
	processConfirmationReport(t, s, "bookings", booking)
	processSyntheticClaims(t, s, "repeat-exercise", n)

	publishedAfter, err := s.Resource(ctx, "activity", id)
	if err != nil || publishedAfter.Revision <= publishedBefore.Revision ||
		publishedAfter.Activity.Relationships[0].TargetID == nil {
		t.Fatal("late endpoint did not advance public owner", err)
	}

	journal := snapshotChanges(t, s, baseline)
	changedOwner := 0
	for _, entry := range journal.Items {
		if entry.Resource.ID == id {
			changedOwner++
		}
	}

	if changedOwner != 1 {
		t.Fatal("link transition missing or replay published", changedOwner)
	}

	d, err = s.LifecycleDetail(ctx, id)
	if err != nil || d.Relationships[0].TargetID == "" ||
		len(observationRowsForTest(t, s, "activity", id)) != 1 {
		t.Fatal("late endpoint/replay", d, err)
	}

	view, err = s.Dashboard(ctx)
	if err != nil || len(view.Lifecycles) != 1 || len(view.Executions) != 2 ||
		view.Executions[0].Terms().Fields().Kind != source.StockBooking {
		t.Fatal(view, err)
	}

	if view.Executions[0].ID() == id {
		t.Fatal("booking and exercise collapsed")
	}
}

func TestDebtNominalMissingTermsAndReferenceSubject(t *testing.T) {
	ctx := context.Background()
	s := newStoreWithSyntheticAccount(t)
	processConfirmationReport(t, s, "debt", syntheticDebt)

	view, err := s.Dashboard(ctx)
	if err != nil || len(view.Executions) != 1 || len(view.Instruments) != 1 {
		t.Fatal(view, err)
	}

	e := view.Executions[0]
	f := e.Terms().Fields()
	if e.Listing() != "" || e.Terms().QuantityUnit() != "nominal" || e.Terms().QuotationBasis() != "" ||
		f.AccruedInterest.Amount() != "-12.3456789" ||
		!f.Instrument.Debt().Fields().Maturity.IsZero() ||
		f.Instrument.Debt().Fields().CouponRate.Value() != "" {
		t.Fatal("invented debt terms or listing", f)
	}

	data, err := encodeTrade(e.Terms())
	if err != nil {
		t.Fatal(err)
	}

	roundtrip, err := decodeTrade(data)
	if err != nil || roundtrip.Fields().AccruedInterest != f.AccruedInterest {
		t.Fatal("debt roundtrip", err)
	}
}

func TestConversionUnknownCounterpartAndIndependentMoney(t *testing.T) {
	ctx := context.Background()
	s := newStoreWithSyntheticAccount(t)
	processConfirmationReport(t, s, "fx", syntheticFX)

	view, err := s.Dashboard(ctx)
	if err != nil || len(view.Conversions) != 1 || len(view.Instruments) != 0 || len(view.Executions) != 0 {
		t.Fatal(view, err)
	}

	c := view.Conversions[0]

	f := c.Terms().Fields()
	if f.Sent.Currency().String() != "EUR" || f.Sent.Amount() != "100.123456789" ||
		f.Received.Currency().String() != "USD" ||
		f.Received.Amount() != "" ||
		f.Rate.Amount() != "1.234567890123" ||
		f.Proceeds.Amount() != "123.45" ||
		f.NetCash.Amount() != "123.25" ||
		f.Charges[0].Total().Currency().String() != "GBP" {
		t.Fatal("conversion roles changed", f)
	}

	processConfirmationReport(t, s, "fx-repeat", syntheticFX)

	view, err = s.Dashboard(ctx)
	if err != nil || len(view.Conversions) != 1 || view.Conversions[0].ID() != c.ID() {
		t.Fatal("conversion replay", err)
	}
}

func correctionRow(event, marker, target string) string {
	row := strings.Replace(equityRow("U1", event, "100"), `code="O"`, `code="`+marker+`"`, 1)
	return strings.Replace(row, `orderID="O1"`, `orderID="O1" origTradeID="`+target+`"`, 1)
}

func TestEarlyCancellationAndReplacementKeepOriginalIdentities(t *testing.T) {
	for _, marker := range []string{"Ca", "Co"} {
		t.Run(marker, func(t *testing.T) {
			ctx := context.Background()
			s := newStoreWithSyntheticAccount(t)
			instruction := correctionRow("replacement", marker, "Toriginal")
			processConfirmationReport(t, s, "early", instruction)

			v, err := s.Dashboard(ctx)
			if err != nil || len(v.Corrections) != 1 || !v.Corrections[0].Status.NeedsReview() {
				t.Fatal("early correction", v, err)
			}

			replacement := ""
			if marker == "Co" {
				if len(v.Executions) != 1 || !v.Executions[0].State().NeedsReview() {
					t.Fatal(v)
				}

				replacement = v.Executions[0].ID()
			} else if len(v.Executions) != 0 {
				t.Fatal("cancellation became trade")
			}

			processConfirmationReport(t, s, "original", equityRow("U1", "original", "100"))

			v, err = s.Dashboard(ctx)
			if err != nil || v.Corrections[0].Status != "applied" {
				t.Fatal(v, err)
			}

			original := v.Executions[0]

			want := "cancelled"
			if marker == "Co" {
				want = "superseded"
			}

			if original.Status() != want {
				t.Fatal("original status", original.Status())
			}

			if marker == "Co" && (v.Executions[1].ID() != replacement || v.Executions[1].Status() != "accepted") {
				t.Fatal("replacement identity changed")
			}

			baseline, err := snapshotState(ctx, s)
			if err != nil {
				t.Fatal(err)
			}

			processConfirmationReport(t, s, "repeat", instruction)

			if len(snapshotChanges(t, s, baseline).Items) != 0 {
				t.Fatal("repeated replacement/cancellation published")
			}

			d, err := s.ExecutionDetail(ctx, original.ID())
			if err != nil || d.Execution.ID() != original.ID() || d.Execution.Status() != want {
				t.Fatal("correction replay", err)
			}

			if marker == "Co" {
				processConfirmationReport(t, s, "competitor", correctionRow("competitor", "Co", "Toriginal"))

				v, err = s.Dashboard(ctx)
				if err != nil {
					t.Fatal(err)
				}

				for _, e := range v.Executions {
					if !e.State().NeedsReview() {
						t.Fatal("competing replacement partially applied", e.Status())
					}
				}
			}
		})
	}
}

func TestSyntheticConfirmationSequenceAcceptsActivityByKind(t *testing.T) {
	ctx := context.Background()
	s := newStoreWithSyntheticAccount(t)
	for index, name := range []string{"initial.xml", "initial.xml", "overlap.xml", "overlap.xml"} {
		//nolint:gosec // Fixed synthetic fixture names from the literal sequence above.
		body, err := os.ReadFile("../../../testdata/ibkrflex/confirmations/" + name)
		if err != nil {
			t.Fatal(err)
		}

		body = []byte(strings.ReplaceAll(string(body), "U0000001", "U1"))

		artifact, err := provider.NewArtifact(ibkrflex.ConfirmationFormat, "application/xml", time.Now(), body)
		if err != nil {
			t.Fatal(err)
		}

		id := fmt.Sprint("sequence-", index)
		if err = s.queries.PutArtifact(
			ctx,
			sqlc.PutArtifactParams{
				ID:           id,
				ConnectionID: "synthetic",
				Stream:       "confirmations",
				Operation:    "acquire",
				Format:       artifact.Format(),
				MediaType:    artifact.MediaType(),
				Retrieved:    artifact.RetrievedAt().Format(time.RFC3339Nano),
				Hash:         artifact.Hash(),
				Content:      body,
			},
		); err != nil {
			t.Fatal(err)
		}

		recordTestReceipt(t, s, id)

		r, err := ibkrflex.Registration(ibkrflex.Options{})
		if err != nil {
			t.Fatal(err)
		}

		result, err := s.Normalize(ctx, id, ibkrflex.Version, r.Normalizer())
		if err != nil {
			t.Fatal(err)
		}

		expected := uint64(17)
		if name == "overlap.xml" {
			expected = 12
		}

		if result.Activities() != expected || result.Rejected() != 0 {
			t.Fatal("claim kinds", result.Counts())
		}

		if err = s.Process(ctx, id, ibkrflex.Version); err != nil {
			t.Fatal(err)
		}
	}

	view, err := s.Dashboard(ctx)
	if err != nil || len(view.Executions) != 14 || len(view.Lifecycles) != 3 || len(view.Conversions) != 2 {
		t.Fatal("sequence duplicated or lost activity", view, err)
	}

	bookings := 0
	for _, e := range view.Executions {
		if e.Terms().Fields().Kind == source.StockBooking {
			bookings++
		}
	}

	if bookings != 2 {
		t.Fatal("stock bookings became exchange executions", bookings)
	}
}

func TestReportedAdjustmentKeepsHistoricalOptionTerms(t *testing.T) {
	ctx := context.Background()
	s := newStoreWithSyntheticAccount(t)
	processConfirmationReport(t, s, "original-option", syntheticOption)

	v, err := s.Dashboard(ctx)
	if err != nil {
		t.Fatal(err)
	}

	original := v.Executions[0]
	f := original.Terms().Fields()
	i := f.Instrument.Fields()
	o := i.Option.Fields()
	o.Multiplier, _ = financial.ParseQuantity("50")
	o.EffectiveFrom, _ = financial.ParseDate("2026-10-21")
	units, _ := financial.ParseQuantity("12.5")
	security, _ := source.NewSecurityDeliverable(o.Underlier.Reference(), units)
	usd, _ := financial.ParseCurrency("USD")
	cash, _ := financial.ParseMoney("0.75", usd)
	cashComponent, _ := source.NewCashDeliverable(cash)
	o.Deliverables = []source.Deliverable{security, cashComponent}

	i.Option, err = source.NewOptionTerms(o)
	if err != nil {
		t.Fatal(err)
	}

	f.Instrument, err = source.NewInstrument(i)
	if err != nil {
		t.Fatal(err)
	}

	f.Location = "synthetic:reported-adjustment"
	f.Event, _ = source.NewReference("ibkr", "execution-id", "U1", "adjusted-fill")
	f.EventAliases = nil
	f.Date, _ = financial.ParseDate("2026-10-22")

	adjusted, err := source.NewTrade(f)
	if err != nil {
		t.Fatal(err)
	}

	n := claimNormalizer(
		func(ctx context.Context, _ provider.Artifact, c provider.Claims) (provider.Result, error) {
			if e := c.PutActivity(ctx, adjusted.Observation()); e != nil {
				return provider.Result{}, e
			}

			return provider.NewResult(provider.Counts{Rows: 1, Activities: 1})
		},
	)
	processSyntheticClaims(t, s, "reported-adjustment", n)

	v, err = s.Dashboard(ctx)
	if err != nil || len(v.Executions) != 2 || v.Executions[0].Instrument() != original.Instrument() {
		t.Fatal("adjustment repurposed contract", v, err)
	}

	if v.Executions[1].Terms().Fields().Instrument.Option().Fields().Multiplier.Value() != "10" ||
		len(v.Executions[1].Terms().Fields().Instrument.Option().Fields().Deliverables) != 0 {
		t.Fatal("historical terms overwritten")
	}

	terms := v.Executions[0].Terms().Fields().Instrument.Option().Fields()
	if terms.Multiplier.Value() != "50" || terms.Deliverables[0].Quantity().Value() != "12.5" ||
		terms.Deliverables[1].Cash().Amount() != "0.75" {
		t.Fatal("adjusted terms lost", terms)
	}
}

func TestOptionUnderlierCanLaterBeReportedAsETF(t *testing.T) {
	ctx := context.Background()
	s := newStoreWithSyntheticAccount(t)
	processConfirmationReport(t, s, "option", syntheticOption)

	before, err := s.Dashboard(ctx)
	if err != nil {
		t.Fatal(err)
	}

	option := before.Executions[0].Instrument()

	d, err := s.InstrumentDetail(ctx, option)
	if err != nil {
		t.Fatal(err)
	}

	underlier := d.Underlier
	etf := strings.Replace(equityRow("U1", "etf-fill", "100"), `subCategory="COMMON"`, `subCategory="ETF"`, 1)
	processConfirmationReport(t, s, "etf", etf)

	root, err := s.InstrumentDetail(ctx, underlier)
	if err != nil || root.Instrument.Kind() != "fund" || root.Instrument.Status() == "conflict" {
		t.Fatal("underlier was prematurely classified", root, err)
	}

	d, err = s.InstrumentDetail(ctx, option)
	if err != nil || d.Underlier != underlier {
		t.Fatal("underlier identity changed", d, err)
	}
}
