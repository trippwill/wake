// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

// Package ibkrflex acquires Confirmation activity and reported Activity results
// using only public contracts.
package ibkrflex

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	"go.trippwill.dev/wake/provider"
)

const (
	Confirmations        = "confirmations"
	ConfirmationFormat   = "ibkr-flex-confirmation-xml"
	Activity             = "activity"
	ActivityFormat       = "ibkr-flex-activity-xml"
	Version              = "ibkr-flex/1"
	continuationVersion  = "confirmation-state-1"
	DefaultBaseURL       = "https://ndcdyn.interactivebrokers.com/AccountManagement/FlexWebService"
	webBudget            = "web-service"
	confirmationInterval = 5 * time.Minute
	activityInterval     = 24 * time.Hour
)

type (
	Options struct {
		HTTPClient     *http.Client
		BaseURL        string
		MaxReportBytes int64
	}
	Source struct {
		http    *http.Client
		base    string
		limit   int64
		streams []provider.Stream
	}
)

func Registration(options Options) (provider.Registration, error) {
	base := options.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}

	u, err := url.Parse(base)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery ||
		u.Fragment != "" {
		return provider.Registration{}, provider.ErrInvalid
	}

	client := options.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}

	copyClient := *client
	copyClient.Timeout = 30 * time.Second
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	limit := options.MaxReportBytes
	if limit == 0 {
		limit = 64 << 20
	}

	if limit < 1 || limit > 64<<20 {
		return provider.Registration{}, provider.ErrInvalid
	}

	stream, _ := provider.NewStream(Confirmations, ConfirmationFormat)
	stream, _ = stream.WithDiscovery()
	stream, _ = stream.WithPolling(confirmationInterval, time.Minute)
	activity, _ := provider.NewStream(Activity, ActivityFormat)
	activity, _ = activity.WithPolling(activityInterval, time.Hour)
	query, _ := provider.NewField("query_id", "Confirmation Query ID", provider.TextField, true)
	activityQuery, _ := provider.NewField(
		"activity_query_id",
		"Activity Query ID (transfers, cash, realized P&L and positions)",
		provider.TextField,
		false,
	)
	token, _ := provider.NewField("token", "Flex Web Service token", provider.SecretField, true)
	policy, _ := provider.NewPacingPolicy(webBudget, 6*time.Second)

	descriptor, err := provider.NewDescriptor(
		"ibkr-flex",
		"IBKR Flex",
		[]provider.Stream{stream, activity},
		[]provider.Field{query, activityQuery, token},
		[]provider.PacingPolicy{policy},
	)
	if err != nil {
		return provider.Registration{}, err
	}

	s := &Source{
		http:    &copyClient,
		base:    strings.TrimRight(u.String(), "/"),
		limit:   limit,
		streams: descriptor.Streams(),
	}

	registration, err := provider.NewRegistration(descriptor, Version, s, s, s, s)
	if err != nil {
		return provider.Registration{}, err
	}

	return registration.WithStreamConfigurer(s)
}

func (s *Source) ConfigureStreams(settings provider.Settings) ([]provider.StreamConfiguration, error) {
	configurations := []provider.StreamConfiguration{}
	for i, key := range []string{"query_id", "activity_query_id"} {
		value, _ := settings.Get(key)
		if value == "" {
			continue
		}

		if !digits(value) || len(value) > 64 {
			return nil, provider.ErrInvalid
		}

		configuration, err := provider.NewStreamConfiguration(s.streams[i], []byte("flex-query/1:"+value))
		if err != nil {
			return nil, err
		}

		configurations = append(configurations, configuration)
	}

	return configurations, nil
}

func (s *Source) PrepareSettings(settings provider.Settings) (provider.Settings, error) {
	values := settings.Values()
	if len(values) < 1 || len(values) > 2 || !digits(values["query_id"]) ||
		len(values["query_id"]) > 64 {
		return provider.Settings{}, provider.ErrInvalid
	}

	for key, value := range values {
		if key != "query_id" && key != "activity_query_id" || !digits(value) || len(value) > 64 {
			return provider.Settings{}, provider.ErrInvalid
		}
	}

	return provider.NewSettings(values)
}

func streamFormat(stream string) (string, string, error) {
	if stream == Activity {
		return ActivityFormat, "AF", nil
	}

	if stream != Confirmations {
		return "", "", provider.ErrUnavailable
	}

	return ConfirmationFormat, "TCF", nil
}
