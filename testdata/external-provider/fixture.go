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
	stream, err := provider.NewStream("confirmations", "fixture-v1")
	if err != nil {
		return provider.Registration{}, err
	}
	field, err := provider.NewField("token", "Access token", provider.SecretField, true)
	if err != nil {
		return provider.Registration{}, err
	}
	pacing, err := provider.NewPacingPolicy("requests", time.Second)
	if err != nil {
		return provider.Registration{}, err
	}
	d, err := provider.NewDescriptor("fixture", "Synthetic fixture", []provider.Stream{stream}, []provider.Field{field}, []provider.PacingPolicy{pacing})
	if err != nil {
		return provider.Registration{}, err
	}
	return provider.NewRegistration(d, "v1", Provider{}, Provider{}, Provider{}, nil)
}
func (Provider) Acquire(ctx context.Context, r provider.Request, a provider.Access) (provider.Step, error) {
	if r.Stream != "confirmations" {
		return provider.Step{}, provider.ErrUnavailable
	}
	if !r.State.IsZero() && r.State.Version() != "v1" {
		return provider.Step{}, provider.ErrUnavailable
	}
	err := a.WithSecret(ctx, "token", func(secret []byte) error {
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
	progress, err := provider.NewProgress("report-retained", 1, 1)
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
	trade, err := source.NewTrade(source.TradeInput{Location: "line:1", Account: account, Instrument: instrument, Event: event, Side: source.Buy, Quantity: quantity, Price: price, TradeDate: date, ExecutedAt: at})
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
