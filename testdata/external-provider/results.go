// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package fixture

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"go.trippwill.dev/wake/financial"
	"go.trippwill.dev/wake/provider"
	"go.trippwill.dev/wake/source"
)

type ResultsProvider struct{ Provider }

const inlineResult = "synthetic equity execution with reported result\n"

func resultStreams() []provider.Stream {
	activity, _ := provider.NewStream("statements", "fixture-v1")
	activity, _ = activity.WithDiscovery()
	result, _ := provider.NewStream("results", "fixture-results-v1")
	return []provider.Stream{activity, result}
}

func RegisterWithResults() (provider.Registration, error) {
	base, err := Register()
	if err != nil {
		return provider.Registration{}, err
	}
	include, _ := provider.NewField("include_results", "Include reported results", provider.TextField, false)
	delivery, _ := provider.NewField("results_delivery", "Results delivery", provider.TextField, false)
	descriptor, err := provider.NewDescriptor("fixture", "Synthetic source", resultStreams(), append(base.Descriptor().Fields(), include, delivery), base.Descriptor().Pacing())
	if err != nil {
		return provider.Registration{}, err
	}
	registration, err := provider.NewRegistration(descriptor, "v1", ResultsProvider{}, ResultsProvider{}, ResultsProvider{}, nil)
	if err != nil {
		return provider.Registration{}, err
	}
	return registration.WithStreamConfigurer(ResultsProvider{})
}

func inlineResults(settings provider.Settings) bool {
	include, _ := settings.Get("include_results")
	delivery, _ := settings.Get("results_delivery")
	return include == "true" && delivery == "inline"
}

func (ResultsProvider) ConfigureStreams(settings provider.Settings) ([]provider.StreamConfiguration, error) {
	streams := resultStreams()
	tenant, _ := settings.Get("tenant")
	include, _ := settings.Get("include_results")
	delivery, _ := settings.Get("results_delivery")
	material, _ := json.Marshal([]string{tenant, strconv.FormatBool(inlineResults(settings))})
	primary, err := provider.NewStreamConfiguration(streams[0], material)
	if err != nil {
		return nil, err
	}
	out := []provider.StreamConfiguration{primary}
	if include == "true" && delivery == "separate" {
		material, _ = json.Marshal([]string{tenant, include, delivery})
		result, e := provider.NewStreamConfiguration(streams[1], material)
		if e != nil {
			return nil, e
		}
		out = append(out, result)
	}
	return out, nil
}

func (p ResultsProvider) Acquire(ctx context.Context, request provider.Request, access provider.Access) (provider.Step, error) {
	if request.Stream != "results" {
		step, err := p.Provider.Acquire(ctx, request, access)
		if err != nil || step.Artifact().IsZero() || !inlineResults(request.Settings) {
			return step, err
		}
		artifact, err := provider.NewArtifact("fixture-v1", "text/plain", request.Now, []byte(inlineResult))
		if err != nil {
			return provider.Step{}, err
		}
		return provider.NewStep(step.State(), artifact, step.NextEligibleAt())
	}
	if err := access.WithSecret(ctx, "api_secret", func(secret []byte) error {
		if string(secret) != "synthetic-token" {
			return provider.ErrUnauthorized
		}
		return nil
	}); err != nil {
		return provider.Step{}, err
	}
	artifact, err := provider.NewArtifact("fixture-results-v1", "text/plain", request.Now, []byte("synthetic reported result"))
	if err != nil {
		return provider.Step{}, err
	}
	return provider.NewStep(provider.State{}, artifact, request.Now.Add(time.Hour))
}

func (p ResultsProvider) Normalize(ctx context.Context, artifact provider.Artifact, sink provider.Claims) (provider.Result, error) {
	if artifact.Format() != "fixture-results-v1" && !(artifact.Format() == "fixture-v1" && string(artifact.Content()) == inlineResult) {
		return p.Provider.Normalize(ctx, artifact, sink)
	}
	counts := provider.Counts{}
	account, _ := source.NewReference("fixture-broker", "account", "", "account-1")
	event, _ := source.NewReference("fixture-broker", "trade", "account-1", "trade-1")
	eur, _ := financial.ParseCurrency("EUR")
	usd, _ := financial.ParseCurrency("USD")
	amount, _ := financial.ParseMoney("12.34000000000000001", eur)
	cost, _ := financial.ParseMoney("-100.00000000000000001", usd)
	amountDate, _ := financial.ParseDate("2026-10-24")
	costDate, _ := financial.ParseDate("2026-10-23")
	amountField, _ := source.NewActivityField(source.ActivityFieldInput{Name: source.FieldRealizedPnL, Money: amount, ReportedOn: amountDate})
	costField, _ := source.NewActivityField(source.ActivityFieldInput{Name: source.FieldReportedCostBasis, Money: cost, ReportedOn: costDate})
	if artifact.Format() == "fixture-v1" {
		return normalizeExecution(ctx, sink, amountField, costField)
	}
	value, err := source.NewActivityObservation(source.ActivityObservationInput{Location: "result:1", Account: account, Event: event, Fields: []source.ActivityField{amountField, costField}})
	if err != nil {
		return provider.Result{}, err
	}
	if err = sink.PutActivity(ctx, value); err != nil {
		return provider.Result{}, err
	}
	counts.Rows++
	counts.Activities++
	return provider.NewResult(counts)
}
