// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package provider_test

import (
	"bytes"
	"context"
	"testing"
	"time"

	"go.trippwill.dev/wake/provider"
)

type streamConfigurerFunc func(provider.Settings) ([]provider.StreamConfiguration, error)

func (f streamConfigurerFunc) ConfigureStreams(
	settings provider.Settings,
) ([]provider.StreamConfiguration, error) {
	return f(settings)
}

func (streamConfigurerFunc) Normalize(
	context.Context,
	provider.Artifact,
	provider.Claims,
) (provider.Result, error) {
	return provider.NewResult(provider.Counts{})
}

func TestProviderOwnsStreamConfigurationAndCannotChangeDeclaredCapabilities(t *testing.T) {
	base, _ := provider.NewStream("trades", "trades-v1")
	results, _ := provider.NewStream("results", "results-v1")
	results, _ = results.WithDiscovery()

	descriptor, err := provider.NewDescriptor(
		"synthetic",
		"Synthetic",
		[]provider.Stream{base, results},
		nil,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}

	configurer := streamConfigurerFunc(
		func(settings provider.Settings) ([]provider.StreamConfiguration, error) {
			enabled, _ := settings.Get("delivery")

			region, _ := settings.Get("region")
			one, _ := provider.NewStreamConfiguration(base, []byte(region))
			out := []provider.StreamConfiguration{one}
			if enabled == "complete" && region == "west" {
				two, _ := provider.NewStreamConfiguration(results, []byte(region+enabled))
				out = append(out, two)
			}

			return out, nil
		},
	)
	registration, _ := provider.NewRegistration(descriptor, "v1", nil, configurer, nil, nil)

	registration, err = registration.WithStreamConfigurer(configurer)
	if err != nil {
		t.Fatal(err)
	}

	empty, _ := provider.NewSettings(map[string]string{})
	settings, _ := provider.NewSettings(map[string]string{"delivery": "complete", "region": "west"})

	configured, err := registration.ConfigureStreams(settings)
	if err != nil || len(configured) != 2 || !configured[1].Stream().CanDiscover() {
		t.Fatal(configured, err)
	}

	inactive, err := registration.ConfigureStreams(empty)
	if err != nil || len(inactive) != 1 {
		t.Fatal(inactive, err)
	}

	wrongFormat, _ := provider.NewStream("results", "undeclared-v2")
	wrongCapabilities, _ := provider.NewStream("results", "results-v1")
	unknown, _ := provider.NewStream("unknown", "unknown-v1")
	for _, stream := range []provider.Stream{wrongFormat, wrongCapabilities, unknown, {}} {
		configuration, _ := provider.NewStreamConfiguration(stream, nil)

		invalid, _ := registration.WithStreamConfigurer(
			streamConfigurerFunc(func(provider.Settings) ([]provider.StreamConfiguration, error) {
				return []provider.StreamConfiguration{configuration}, nil
			}),
		)
		if _, err = invalid.ConfigureStreams(settings); err != provider.ErrInvalid {
			t.Fatal("undeclared stream or capability admitted", err)
		}
	}

	duplicate, _ := registration.WithStreamConfigurer(
		streamConfigurerFunc(func(provider.Settings) ([]provider.StreamConfiguration, error) {
			return []provider.StreamConfiguration{configured[0], configured[0]}, nil
		}),
	)
	if _, err = duplicate.ConfigureStreams(settings); err != provider.ErrInvalid {
		t.Fatal("duplicate input admitted", err)
	}

	if _, err = provider.NewResult(provider.Counts{Rows: 1, Activities: 2}); err == nil {
		t.Fatal("unaccounted result rows")
	}
}

func TestDefaultStreamConfigurationUsesAllSettingsWithoutReadingFieldKeys(t *testing.T) {
	stream, _ := provider.NewStream("records", "records-v1")
	descriptor, _ := provider.NewDescriptor("synthetic", "Synthetic", []provider.Stream{stream}, nil, nil)
	registration, _ := provider.NewRegistration(descriptor, "v1", nil, streamConfigurerFunc(nil), nil, nil)
	one, _ := provider.NewSettings(map[string]string{"delivery": "one", "region": "west"})
	two, _ := provider.NewSettings(map[string]string{"delivery": "one", "region": "east"})

	before, err := registration.ConfigureStreams(one)
	if err != nil {
		t.Fatal(err)
	}

	after, err := registration.ConfigureStreams(two)
	if err != nil || before[0].Identity() == after[0].Identity() {
		t.Fatal("settings dependency lost", before, after, err)
	}

	payload := []byte("opaque configuration")
	configuration, _ := provider.NewStreamConfiguration(stream, payload)
	identity := configuration.Identity()
	payload[0] = 'x'

	if configuration.Identity() != identity {
		t.Fatal("mutable configuration identity")
	}
}

func TestImmutableContinuationEvidenceAndAccounting(t *testing.T) {
	payload := []byte(`{"page":1}`)

	s, err := provider.NewState("v1", payload)
	if err != nil {
		t.Fatal(err)
	}

	payload[0] = 'x'
	copy := s.Payload()
	copy[0] = 'y'

	if string(s.Payload()) != `{"page":1}` {
		t.Fatal("mutable continuation")
	}

	if _, err = provider.NewState("v1", bytes.Repeat([]byte("x"), 65537)); err != provider.ErrLimit {
		t.Fatal("unbounded continuation", err)
	}

	a, err := provider.NewArtifact("fixture", "text/plain", time.Now(), payload)
	if err != nil {
		t.Fatal(err)
	}

	hash := a.Hash()
	payload[0] = 'z'
	copy = a.Content()
	copy[0] = 'q'

	if a.Hash() != hash {
		t.Fatal("mutable evidence")
	}

	if _, err = provider.NewResult(provider.Counts{Rows: 1, Activities: 1, Rejected: 1}); err == nil {
		t.Fatal("inconsistent counts")
	}

	if _, err = provider.NewProgress("private error text", 0, 0); err == nil {
		t.Fatal("unsafe progress")
	}
}
