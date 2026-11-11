// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package snaptrade

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"go.trippwill.dev/wake/provider"
)

const (
	streamID        = "activities"
	format          = "snaptrade-json"
	positionsID     = "positions"
	positionsFormat = "snaptrade-positions-json"
	clientField     = "client_id"
	keyField        = "consumer_key"
	clientPacing    = "customer"
	accountPacing   = "account"
)

type Options struct {
	HTTPClient     *http.Client
	BaseURL        string
	MaxReportBytes int64
}

type adapter struct {
	client *http.Client
	base   string
	limit  int64
}

func Registration(options Options) (provider.Registration, error) {
	base := options.BaseURL
	if base == "" {
		base = "https://api.snaptrade.com"
	}

	u, err := url.Parse(base)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil ||
		u.Path != "" && u.Path != "/" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return provider.Registration{}, provider.ErrInvalid
	}

	limit := options.MaxReportBytes
	if limit == 0 {
		limit = 64 << 20
	}

	if limit < 1 || limit > 64<<20 {
		return provider.Registration{}, provider.ErrInvalid
	}

	client := options.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}

	copyClient := *client
	copyClient.Timeout = 30 * time.Second
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	stream, _ := provider.NewStream(streamID, format)
	stream, _ = stream.WithDiscovery()
	stream, _ = stream.WithPolling(24*time.Hour, time.Hour)
	positions, _ := provider.NewStream(positionsID, positionsFormat)
	positions, _ = positions.WithPolling(24*time.Hour, time.Hour)
	clientID, _ := provider.NewField(clientField, "Personal Client ID", provider.TextField, true)
	key, _ := provider.NewField(keyField, "Personal consumer key", provider.SecretField, true)
	customer, _ := provider.NewPacingPolicy(clientPacing, time.Second)
	account, _ := provider.NewPacingPolicy(accountPacing, 15*time.Second)

	descriptor, err := provider.NewDescriptor(
		"snaptrade", "SnapTrade Personal", []provider.Stream{stream, positions}, []provider.Field{clientID, key},
		[]provider.PacingPolicy{customer, account},
	)
	if err != nil {
		return provider.Registration{}, err
	}

	a := &adapter{client: &copyClient, base: strings.TrimSuffix(base, "/"), limit: limit}

	return provider.NewRegistration(descriptor, "snaptrade-4", a, a, a, a)
}

func (*adapter) PrepareSettings(settings provider.Settings) (provider.Settings, error) {
	client, _ := settings.Get(clientField)
	if client == "" || len(client) > 256 {
		return provider.Settings{}, provider.ErrInvalid
	}

	return settings, nil
}

func signature(path, query string, key []byte) (string, error) {
	var canonical bytes.Buffer

	encoder := json.NewEncoder(&canonical)
	encoder.SetEscapeHTML(false)

	if err := encoder.Encode(struct {
		Content any    `json:"content"`
		Path    string `json:"path"`
		Query   string `json:"query"`
	}{nil, path, query}); err != nil {
		return "", provider.ErrInvalid
	}

	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(bytes.TrimSuffix(canonical.Bytes(), []byte("\n")))

	return base64.StdEncoding.EncodeToString(mac.Sum(nil)), nil
}

type response struct {
	body      []byte
	header    http.Header
	status    int
	notBefore time.Time
}

func (a *adapter) get(
	ctx context.Context, now time.Time, clientID, path string, parameters url.Values, key []byte,
) (response, error) {
	if len(key) == 0 {
		return response{}, provider.ErrUnauthorized
	}

	parameters.Set("clientId", clientID)
	parameters.Set("timestamp", strconv.FormatInt(now.Unix(), 10))
	query := parameters.Encode()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, a.base+path+"?"+query, nil)
	if err != nil {
		return response{}, provider.ErrInvalid
	}

	signed, err := signature(request.URL.EscapedPath(), request.URL.RawQuery, key)
	if err != nil {
		return response{}, err
	}

	request.Header.Set("Signature", signed)
	request.Header.Set("Accept", "application/json")

	reply, err := a.client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return response{}, ctx.Err()
		}

		return response{}, provider.ErrTemporary
	}
	defer func() { _ = reply.Body.Close() }()

	result := response{header: reply.Header, status: reply.StatusCode}
	if reply.StatusCode != http.StatusOK {
		return result, nil
	}

	body, err := io.ReadAll(io.LimitReader(reply.Body, a.limit+1))
	if err != nil {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}

		return result, provider.ErrTemporary
	}

	if int64(len(body)) > a.limit {
		return result, provider.ErrLimit
	}

	if bytes.Contains(body, key) || bytes.Contains(body, []byte(signed)) {
		return result, provider.ErrMalformed
	}

	if err = validateJSON(ctx, body, key, []byte(signed)); err != nil {
		return result, err
	}

	result.body = body

	return result, nil
}

func retryTime(header http.Header, key string, now time.Time) time.Time {
	value := strings.TrimSpace(header.Get(key))
	if seconds, err := strconv.ParseInt(value, 10, 32); err == nil && seconds >= 0 {
		return now.Add(time.Duration(seconds) * time.Second)
	}

	if key == "Retry-After" {
		if date, err := http.ParseTime(value); err == nil {
			return date
		}
	}

	return time.Time{}
}
