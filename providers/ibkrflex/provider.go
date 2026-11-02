// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

// Package ibkrflex implements the Confirmation protocol using only public contracts.
package ibkrflex

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	"go.trippwill.dev/wake/provider"
)

const (
	Confirmations       = "confirmations"
	ConfirmationFormat  = "ibkr-flex-confirmation-xml"
	Version             = "confirmation-3"
	continuationVersion = "confirmation-state-1"
	DefaultBaseURL      = "https://ndcdyn.interactivebrokers.com/AccountManagement/FlexWebService"
	webBudget           = "web-service"
)

type (
	Options struct {
		HTTPClient     *http.Client
		BaseURL        string
		MaxReportBytes int64
	}
	Source struct {
		http  *http.Client
		base  string
		limit int64
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
	query, _ := provider.NewField("query_id", "Confirmation Query ID", provider.TextField, true)
	token, _ := provider.NewField("token", "Flex Web Service token", provider.SecretField, true)
	policy, _ := provider.NewPacingPolicy(webBudget, 6*time.Second)

	descriptor, err := provider.NewDescriptor(
		"ibkr-flex",
		"IBKR Flex",
		[]provider.Stream{stream},
		[]provider.Field{query, token},
		[]provider.PacingPolicy{policy},
	)
	if err != nil {
		return provider.Registration{}, err
	}

	s := &Source{http: &copyClient, base: strings.TrimRight(u.String(), "/"), limit: limit}

	return provider.NewRegistration(descriptor, Version, s, s, s, s)
}

func (s *Source) PrepareSettings(settings provider.Settings) (provider.Settings, error) {
	values := settings.Values()
	if len(values) != 1 || !digits(values["query_id"]) ||
		len(values["query_id"]) > 64 {
		return provider.Settings{}, provider.ErrInvalid
	}

	return provider.NewSettings(values)
}

func streamFormat(stream string) (string, string, error) {
	if stream != Confirmations {
		return "", "", provider.ErrUnavailable
	}

	return ConfirmationFormat, "TCF", nil
}
