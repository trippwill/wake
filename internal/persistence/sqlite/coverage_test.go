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

// Synthetic reports use a disposable account, never operator data or credentials.
func coverageStore(t *testing.T) *Store {
	t.Helper()
	s, _ := testStore(t)

	_, err := s.db.Exec(`INSERT INTO connections(id,provider) VALUES('synthetic','ibkr-flex');
INSERT INTO artifacts(id,connection_id,stream,operation,format,media_type,retrieved,hash,content) VALUES('discovery','synthetic','confirmations','discover','ibkr-confirmation-v1','application/xml','2026-10-20T00:00:00Z','synthetic',X'00');
INSERT INTO accounts(id,singleton,name,currency,connection_id,provider,stream,authority,kind,scope,value,artifact_id) VALUES('account',1,'Synthetic account','','synthetic','ibkr-flex','confirmations','ibkr','account','','U1','discovery');`)
	if err != nil {
		t.Fatal(err)
	}

	return s
}

func coverageReport(t *testing.T, s *Store, id, rows string) {
	t.Helper()

	ctx := context.Background()
	body := []byte(workflowReport(rows))
	artifact, _ := provider.NewArtifact(ibkrflex.ConfirmationFormat, "application/xml", time.Now(), body)

	r, err := ibkrflex.Registration(ibkrflex.Options{})
	if err != nil {
		t.Fatal(err)
	}

	err = s.queries.PutArtifact(
		ctx,
		sqlc.PutArtifactParams{
			ID:           id,
			ConnectionID: "synthetic",
			Stream:       "confirmations",
			Operation:    "acquire",
			Format:       ibkrflex.ConfirmationFormat,
			MediaType:    "application/xml",
			Retrieved:    time.Now().UTC().Format(time.RFC3339Nano),
			Hash:         artifact.Hash(),
			Content:      body,
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	recordTestReceipt(t, s, id)

	if _, err = s.Normalize(ctx, id, ibkrflex.Version, r.Normalizer()); err != nil {
		t.Fatal(err)
	}

	if err = s.Process(ctx, id, ibkrflex.Version); err != nil {
		t.Fatal(err)
	}
}

const syntheticOption = `<TradeConfirm accountId="U1" assetCategory="OPT" symbol="SYN 261120C00100000" description="Synthetic option" conid="200" underlyingConid="100" underlyingSymbol="SYN" listingExchange="CBOE" tradeID="option-trade" execID="option-fill" orderID="order-1" transactionType="ExchTrade" levelOfDetail="EXECUTION" buySell="BUY" quantity="2" price="1.234567890123456789" currency="USD" tradeDate="2026-10-20" strike="100.00000000001" expiry="2026-11-20" putCall="C" multiplier="10"/>`

func TestOptionIdentityHistoricalTermsAndReplay(t *testing.T) {
	ctx := context.Background()
	s := coverageStore(t)
	coverageReport(t, s, "first", syntheticOption+equityRow("U1", "underlier-fill", "100"))

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

	coverageReport(t, s, "overlap", syntheticOption)

	view, err = s.Dashboard(ctx)
	if err != nil || len(view.Executions) != 2 || view.Executions[1].ID() != original.ID() {
		t.Fatal("duplicate option", err)
	}

	records, err := s.Records(ctx, "first", ibkrflex.Version)
	if err != nil || records[0].Trade.Fields().Instrument.Option().Fields().Strike != f.Strike {
		t.Fatal("retained terms", err)
	}
}

// coverageClaims admits explicitly labelled provider-neutral claims against retained
// synthetic evidence, for facts the Confirmation format does not supply.
func coverageClaims(t *testing.T, s *Store, id string, n provider.Normalizer) {
	t.Helper()
	coverageReport(t, s, id, "")

	if _, err := s.Normalize(context.Background(), id, "synthetic-claims/1", n); err != nil {
		t.Fatal(err)
	}

	if err := s.Process(context.Background(), id, "synthetic-claims/1"); err != nil {
		t.Fatal(err)
	}
}

type claimNormalizer func(context.Context, provider.Artifact, provider.Claims) (provider.Result, error)

func (n claimNormalizer) Normalize(
	ctx context.Context,
	a provider.Artifact,
	c provider.Claims,
) (provider.Result, error) {
	return n(ctx, a, c)
}

func TestLifecycleRelationshipsResolveWithoutInventingBookings(t *testing.T) {
	ctx := context.Background()
	s := coverageStore(t)
	coverageReport(t, s, "option", syntheticOption)

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
			Date:          f.TradeDate,
			Relationships: []source.Relationship{link},
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	n := claimNormalizer(
		func(ctx context.Context, _ provider.Artifact, c provider.Claims) (provider.Result, error) {
			if e := c.PutLifecycle(ctx, l); e != nil {
				return provider.Result{}, e
			}

			return provider.NewResult(provider.Counts{Rows: 1, Lifecycles: 1})
		},
	)
	coverageClaims(t, s, "exercise", n)

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
	coverageReport(t, s, "bookings", booking)
	coverageClaims(t, s, "repeat-exercise", n)

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
		len(observationRowsForTest(t, s, "activity", id)) != 2 {
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

const syntheticDebt = `<TradeConfirm accountId="U1" assetCategory="BOND" subCategory="Govt" symbol="SYN TREASURY" description="Synthetic Treasury missing terms" conid="debt-1" issuerCountryCode="US" transactionType="ExchTrade" levelOfDetail="EXECUTION" execID="debt-fill" buySell="BUY" quantity="10000" price="99.123456789" currency="USD" tradeDate="2026-10-20" accruedInt="-12.3456789" amount="9912.35" proceeds="-9912.35" netCash="-9925.6956789"/>`

func TestDebtNominalMissingTermsAndReferenceSubject(t *testing.T) {
	ctx := context.Background()
	s := coverageStore(t)
	coverageReport(t, s, "debt", syntheticDebt)

	view, err := s.Dashboard(ctx)
	if err != nil || len(view.Executions) != 1 || len(view.Instruments) != 1 {
		t.Fatal(view, err)
	}

	e := view.Executions[0]
	f := e.Terms().Fields()
	if e.Listing() != "" || e.Terms().QuantityUnit() != "nominal" || f.QuotationBasis != "" ||
		f.AccruedInterest.Amount() != "-12.3456789" ||
		!f.Instrument.Debt().Fields().Maturity.IsZero() ||
		f.Instrument.Debt().Fields().Coupon.Value() != "" {
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

const syntheticFX = `<TradeConfirm accountId="U1" assetCategory="CASH" symbol="EUR.USD" description="EUR.USD" transactionType="ExchTrade" levelOfDetail="EXECUTION" execID="fx-fill" tradeID="fx-trade" buySell="SELL" quantity="-100.123456789" price="1.234567890123" currency="USD" tradeDate="2026-10-20" proceeds="123.45" netCash="123.25" commission="-0.2" commissionCurrency="GBP"/>`

func TestConversionUnknownCounterpartAndIndependentMoney(t *testing.T) {
	ctx := context.Background()
	s := coverageStore(t)
	coverageReport(t, s, "fx", syntheticFX)

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
		f.Net.Amount() != "123.25" ||
		f.Charges[0].Money().Currency().String() != "GBP" {
		t.Fatal("conversion roles changed", f)
	}

	coverageReport(t, s, "fx-repeat", syntheticFX)

	view, err = s.Dashboard(ctx)
	if err != nil || len(view.Conversions) != 1 || view.Conversions[0].ID() != c.ID() {
		t.Fatal("conversion replay", err)
	}
}

func TestCompatibleEnrichmentAndCompetingOptionalClaims(t *testing.T) {
	ctx := context.Background()
	s := coverageStore(t)
	coverageReport(t, s, "baseline", syntheticOption)

	v, err := s.Dashboard(ctx)
	if err != nil {
		t.Fatal(err)
	}

	id := v.Executions[0].ID()
	revision := v.Executions[0].Revision()
	enriched := strings.Replace(
		syntheticOption,
		`multiplier="10"`,
		`multiplier="10" netCash="-2.0000000001"`,
		1,
	)
	coverageReport(t, s, "enrichment", enriched)

	d, err := s.ExecutionDetail(ctx, id)
	if err != nil || d.Execution.Terms().Fields().Net.Amount() != "-2.0000000001" ||
		d.Execution.Revision() <= revision {
		t.Fatal("optional enrichment", d, err)
	}

	revision = d.Execution.Revision()

	coverageReport(t, s, "enrichment-repeat", enriched)

	d, err = s.ExecutionDetail(ctx, id)
	if err != nil || d.Execution.Revision() != revision {
		t.Fatal("unchanged replay revised entity", err)
	}

	competing := strings.Replace(enriched, `netCash="-2.0000000001"`, `netCash="-3"`, 1)
	coverageReport(t, s, "competing-enrichment", competing)

	d, err = s.ExecutionDetail(ctx, id)
	if err != nil || !d.Execution.State().NeedsReview() || d.Execution.Terms().Fields().Net.Amount() != "" {
		t.Fatal("arrival order selected optional claim", d, err)
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
			s := coverageStore(t)
			instruction := correctionRow("replacement", marker, "Toriginal")
			coverageReport(t, s, "early", instruction)

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

			coverageReport(t, s, "original", equityRow("U1", "original", "100"))

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

			coverageReport(t, s, "repeat", instruction)

			if len(snapshotChanges(t, s, baseline).Items) != 0 {
				t.Fatal("repeated replacement/cancellation published")
			}

			d, err := s.ExecutionDetail(ctx, original.ID())
			if err != nil || d.Execution.ID() != original.ID() || d.Execution.Status() != want {
				t.Fatal("correction replay", err)
			}

			if marker == "Co" {
				coverageReport(t, s, "competitor", correctionRow("competitor", "Co", "Toriginal"))

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

func TestIdentifierMismatchCannotEnrichAcceptedTrade(t *testing.T) {
	ctx := context.Background()
	s := coverageStore(t)
	coverageReport(t, s, "baseline", equityRow("U1", "same", "100"))
	changed := strings.Replace(equityRow("U1", "same", "100"), `US0000000001`, `US0000000002`, 1)
	coverageReport(t, s, "mismatch", changed)

	v, err := s.Dashboard(ctx)
	if err != nil || len(v.Executions) != 1 || !v.Executions[0].State().NeedsReview() ||
		v.Executions[0].Terms().Fields().Instrument.Identifiers()[0].Value() != "US0000000001" {
		t.Fatal(v, err)
	}
}

func TestSyntheticConfirmationSequenceAcceptsActivityByKind(t *testing.T) {
	ctx := context.Background()
	s := coverageStore(t)
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

		if result.Lifecycles() != 3 || result.Conversions() != 2 || result.Rejected() != 0 {
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
	s := coverageStore(t)
	coverageReport(t, s, "original-option", syntheticOption)

	v, err := s.Dashboard(ctx)
	if err != nil {
		t.Fatal(err)
	}

	original := v.Executions[0]
	f := original.Terms().Fields()
	i := f.Instrument.Fields()
	o := i.Option.Fields()
	o.Multiplier, _ = financial.NewQuantity("50")
	o.EffectiveFrom, _ = financial.ParseDate("2026-10-21")
	units, _ := financial.NewQuantity("12.5")
	security, _ := source.NewSecurityDeliverable(o.Underlier.Reference(), units)
	usd, _ := financial.ParseCurrency("USD")
	cash, _ := financial.NewMoney("0.75", usd)
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
	f.TradeDate, _ = financial.ParseDate("2026-10-22")

	adjusted, err := source.NewTrade(f)
	if err != nil {
		t.Fatal(err)
	}

	n := claimNormalizer(
		func(ctx context.Context, _ provider.Artifact, c provider.Claims) (provider.Result, error) {
			if e := c.PutTrade(ctx, adjusted); e != nil {
				return provider.Result{}, e
			}

			return provider.NewResult(provider.Counts{Rows: 1, Trades: 1})
		},
	)
	coverageClaims(t, s, "reported-adjustment", n)

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
	s := coverageStore(t)
	coverageReport(t, s, "option", syntheticOption)

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
	coverageReport(t, s, "etf", etf)

	root, err := s.InstrumentDetail(ctx, underlier)
	if err != nil || root.Instrument.Kind() != "fund" || root.Instrument.Status() == "conflict" {
		t.Fatal("underlier was prematurely classified", root, err)
	}

	d, err = s.InstrumentDetail(ctx, option)
	if err != nil || d.Underlier != underlier {
		t.Fatal("underlier identity changed", d, err)
	}
}
