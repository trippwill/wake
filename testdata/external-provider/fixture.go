// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

// Package fixture is a deterministic external source integration.
package fixture

import (
	"context"
	"go.trippwill.dev/wake/financial"
	"go.trippwill.dev/wake/provider"
	"go.trippwill.dev/wake/source"
	"time"
)

type Provider struct{}

func Register() (provider.Registration, error) {
	stream, err := provider.NewStream("statements", "fixture-v1")
	if err != nil {
		return provider.Registration{}, err
	}
	field, err := provider.NewField("api_secret", "API secret", provider.SecretField, true)
	if err != nil {
		return provider.Registration{}, err
	}
	tenant, err := provider.NewField("tenant", "Tenant identifier", provider.TextField, true)
	if err != nil {
		return provider.Registration{}, err
	}
	pacing, err := provider.NewPacingPolicy("requests", time.Second)
	if err != nil {
		return provider.Registration{}, err
	}
	d, err := provider.NewDescriptor("fixture", "Synthetic fixture", []provider.Stream{stream}, []provider.Field{tenant, field}, []provider.PacingPolicy{pacing})
	if err != nil {
		return provider.Registration{}, err
	}
	return provider.NewRegistration(d, "v1", Provider{}, Provider{}, Provider{}, nil)
}
func (Provider) Acquire(ctx context.Context, r provider.Request, a provider.Access) (provider.Step, error) {
	if tenant, _ := r.Settings.Get("tenant"); r.Stream != "statements" || tenant != "synthetic-tenant" {
		return provider.Step{}, provider.ErrUnavailable
	}
	if !r.State.IsZero() && r.State.Version() != "v1" {
		return provider.Step{}, provider.ErrUnavailable
	}
	err := a.WithSecret(ctx, "api_secret", func(secret []byte) error {
		if string(secret) != "synthetic-token" {
			return provider.ErrUnauthorized
		}
		return nil
	})
	if err != nil {
		return provider.Step{}, err
	}
	scope, err := provider.NewPacingScope([]byte("fixture-endpoint"))
	if err != nil {
		return provider.Step{}, err
	}
	reservation, err := a.ReservePacing(ctx, "requests", scope)
	if err != nil {
		return provider.Step{}, err
	}
	if !reservation.Granted() {
		return provider.NewStep(r.State, provider.Artifact{}, reservation.NextEligibleAt())
	}
	progress, err := provider.NewProgress(provider.ProgressRetained, 1, 1)
	if err != nil {
		return provider.Step{}, err
	}
	if err = a.Report(ctx, progress); err != nil {
		return provider.Step{}, err
	}
	artifact, err := provider.NewArtifact("fixture-v1", "text/plain", r.Now, []byte("synthetic equity execution\n"))
	if err != nil {
		return provider.Step{}, err
	}
	state, err := provider.NewState("v1", []byte(`{"page":2}`))
	if err != nil {
		return provider.Step{}, err
	}
	return provider.NewStep(state, artifact, r.Now.Add(time.Hour))
}
func (p Provider) Discover(ctx context.Context, r provider.Request, a provider.Access) (provider.Discovery, error) {
	step, err := p.Acquire(ctx, r, a)
	if err != nil {
		return provider.Discovery{}, err
	}
	if step.Artifact().IsZero() {
		return provider.NewDiscovery(step, nil)
	}
	ref, err := source.NewReference("fixture-broker", "account", "", "account-1")
	if err != nil {
		return provider.Discovery{}, err
	}
	currency, err := financial.ParseCurrency("USD")
	if err != nil {
		return provider.Discovery{}, err
	}
	candidate, err := provider.NewCandidate(ref, "Synthetic brokerage account", currency)
	if err != nil {
		return provider.Discovery{}, err
	}
	return provider.NewDiscovery(step, []provider.Candidate{candidate})
}
func (Provider) Normalize(ctx context.Context, artifact provider.Artifact, sink provider.Claims) (provider.Result, error) {
	if artifact.Format() != "fixture-v1" {
		return provider.Result{}, provider.ErrUnavailable
	}
	if string(artifact.Content()) != "synthetic equity execution\n" {
		return provider.Result{}, provider.ErrMalformed
	}
	account, _ := source.NewReference("fixture-broker", "account", "", "account-1")
	instrumentRef, _ := source.NewReference("fixture-broker", "instrument", "", "equity-1")
	event, _ := source.NewReference("fixture-broker", "trade", "account-1", "trade-1")
	instrument, err := source.NewInstrument(source.InstrumentInput{Reference: instrumentRef, Kind: source.Equity, Ticker: "SYN", Name: "Synthetic Equity"})
	if err != nil {
		return provider.Result{}, err
	}
	observation, err := source.NewInstrumentObservation("line:1:instrument", instrument, financial.Date{})
	if err != nil {
		return provider.Result{}, err
	}
	if err = sink.PutInstrument(ctx, observation); err != nil {
		return provider.Result{}, err
	}
	currency, _ := financial.ParseCurrency("USD")
	quantity, _ := financial.NewQuantity("1.0000000000000000000000000001")
	price, _ := financial.NewPrice("123.4500000000000000000000000001", currency)
	date, _ := financial.ParseDate("2026-10-23")
	at, _ := financial.ParseTimestamp("2026-10-23T12:00:00.1200Z")
	trade, err := source.NewTrade(source.TradeInput{Location: "line:1", Account: account, Instrument: instrument, Event: event, EventDistinct: true, Side: source.Buy, Quantity: quantity, Price: price, TradeDate: date, ExecutedAt: at})
	if err != nil {
		return provider.Result{}, err
	}
	if err = sink.PutTrade(ctx, trade); err != nil {
		return provider.Result{}, err
	}
	if err = sink.Reject(ctx, "line:2", "invalid-row"); err != nil {
		return provider.Result{}, err
	}
	return provider.NewResult(provider.Counts{Rows: 3, Trades: 1, Instruments: 1, Rejected: 1})
}

var _ provider.Acquirer = Provider{}
var _ provider.Normalizer = Provider{}
var _ provider.Discoverer = Provider{}

// Option is a provider-neutral adjusted contract, not an IBKR field claim.
func Option() (source.Trade, error) {
	c, _ := financial.ParseCurrency("USD")
	strike, _ := financial.NewPrice("123.4500000000000000001", c)
	multiplier, _ := financial.NewQuantity("10")
	quantity, _ := financial.NewQuantity("12.5")
	cash, _ := financial.NewMoney("0.75", c)
	ref, _ := source.NewReference("fixture-broker", "instrument", "", "equity-1")
	u, _ := source.NewInstrument(source.InstrumentInput{Reference: ref, Kind: source.Equity, Name: "Underlier"})
	security, _ := source.NewSecurityDeliverable(ref, quantity)
	money, _ := source.NewCashDeliverable(cash)
	effective, _ := financial.ParseDate("2026-10-01")
	expiry, _ := financial.ParseDate("2026-11-20")
	terms, err := source.NewOptionTerms(source.OptionInput{Underlier: u, Right: source.Call, Strike: strike, Multiplier: multiplier, EffectiveFrom: effective, Expiry: expiry, Deliverables: []source.Deliverable{security, money}})
	if err != nil {
		return source.Trade{}, err
	}
	contract, _ := source.NewReference("fixture-broker", "contract", "", "option-1")
	i, err := source.NewInstrument(source.InstrumentInput{Reference: contract, Kind: source.Option, Name: "Adjusted option", Option: terms})
	if err != nil {
		return source.Trade{}, err
	}
	a, _ := source.NewReference("fixture-broker", "account", "", "account-1")
	e, _ := source.NewReference("fixture-broker", "trade", "account-1", "option-1")
	price, _ := financial.NewPrice("1.0000000000000000000001", c)
	date, _ := financial.ParseDate("2026-10-23")
	return source.NewTrade(source.TradeInput{Location: "option:1", Account: a, Event: e, Instrument: i, Side: source.Buy, Quantity: multiplier, Price: price, TradeDate: date})
}

func Lifecycle() (source.Lifecycle, error) {
	t, err := Option()
	if err != nil {
		return source.Lifecycle{}, err
	}
	f := t.Fields()
	target, _ := source.NewReference("fixture-broker", "trade", "account-1", "booking-1")
	link, _ := source.NewRelationship(source.StockBookingLink, target)
	return source.NewLifecycle(source.LifecycleInput{Location: "lifecycle:1", Account: f.Account, Event: f.Event, Instrument: f.Instrument, Kind: source.Exercise, Quantity: f.Quantity, Date: f.TradeDate, Relationships: []source.Relationship{link}})
}

// Debt carries synthetic terms not supplied by Confirmation trade rows.
func Debt(kind source.DebtType) (source.Trade, error) {
	t, err := Option()
	if err != nil {
		return source.Trade{}, err
	}
	f := t.Fields()
	code := "USD"
	if kind == source.ConventionalGilt {
		code = "GBP"
	}
	c, _ := financial.ParseCurrency(code)
	maturity, _ := financial.ParseDate("2036-10-20")
	issue, _ := financial.ParseDate("2026-10-20")
	coupon, _ := financial.NewQuantity("4.125000000000001")
	terms, err := source.NewDebtTerms(source.DebtInput{Type: kind, Currency: c, Maturity: maturity, IssueDate: issue, Coupon: coupon, PaymentsPerYear: 2})
	if err != nil {
		return source.Trade{}, err
	}
	ref, _ := source.NewReference("fixture-broker", "issue", "", string(kind))
	f.Instrument, err = source.NewInstrument(source.InstrumentInput{Reference: ref, Kind: source.Debt, Name: "Synthetic debt issue", Debt: terms})
	if err != nil {
		return source.Trade{}, err
	}
	f.Price, _ = financial.NewPrice("99.123456789", c)
	f.AccruedInterest, _ = financial.NewMoney("-12.345", c)
	return source.NewTrade(f)
}

func Conversion() (source.Conversion, error) {
	t, err := Option()
	if err != nil {
		return source.Conversion{}, err
	}
	f := t.Fields()
	eur, _ := financial.ParseCurrency("EUR")
	usd, _ := financial.ParseCurrency("USD")
	gbp, _ := financial.ParseCurrency("GBP")
	sent, _ := source.NewCurrencyAmount(eur, "10.1234567890000000001")
	received, _ := source.NewCurrencyAmount(usd, "")
	rate, _ := financial.NewPrice("1.2345678901", usd)
	money, _ := financial.NewMoney("-0.25", gbp)
	charge, _ := source.NewCharge("commission", "total", money)
	return source.NewConversion(source.ConversionInput{Location: "fx:1", Account: f.Account, Event: f.Event, Date: f.TradeDate, Sent: sent, Received: received, RateBase: eur, Rate: rate, Charges: []source.Charge{charge}})
}

func Correction() (source.Correction, error) {
	t, err := Option()
	if err != nil {
		return source.Correction{}, err
	}
	f := t.Fields()
	target, _ := source.NewReference("fixture-broker", "trade", "account-1", "original")
	return source.NewCorrection(source.CorrectionInput{Location: "correction:1", Account: f.Account, Target: target, Replacement: f.Event, Kind: source.Replacement})
}

// DocumentNormalizer intentionally has no acquisition, discovery, or settings
// capability. Original bytes and source location enter through public contracts.
type DocumentNormalizer struct{}

func RegisterDocument() (provider.Registration, error) {
	stream, err := provider.NewStream("documents", "synthetic-document-v1")
	if err != nil {
		return provider.Registration{}, err
	}
	descriptor, err := provider.NewDescriptor("synthetic-document", "Synthetic document", []provider.Stream{stream}, nil, nil)
	if err != nil {
		return provider.Registration{}, err
	}
	return provider.NewRegistration(descriptor, "document-v1", nil, DocumentNormalizer{}, nil, nil)
}
func (DocumentNormalizer) Normalize(ctx context.Context, artifact provider.Artifact, claims provider.Claims) (provider.Result, error) {
	if artifact.Format() != "synthetic-document-v1" {
		return provider.Result{}, provider.ErrUnavailable
	}
	if string(artifact.Content()) != "synthetic document bytes" {
		return provider.Result{}, provider.ErrMalformed
	}
	trade, err := Option()
	if err != nil {
		return provider.Result{}, err
	}
	fields := trade.Fields()
	fields.Location = "page:1/region:2/row:3"
	trade, err = source.NewTrade(fields)
	if err != nil {
		return provider.Result{}, err
	}
	if err = claims.PutTrade(ctx, trade); err != nil {
		return provider.Result{}, err
	}
	return provider.NewResult(provider.Counts{Rows: 1, Trades: 1})
}
