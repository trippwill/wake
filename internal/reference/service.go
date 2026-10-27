// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

// Package reference owns OpenFIGI mapping, shared admission and retained evidence.
package reference

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"go.trippwill.dev/wake/internal/credential"
	"go.trippwill.dev/wake/provider"
	"go.trippwill.dev/wake/source"
)

const Rule = "openfigi-share-class-link/1"

type Job struct {
	IDType  string `json:"idType"`
	IDValue string `json:"idValue"`
}

func (j Job) Key() string { b, _ := json.Marshal(j); return string(b) }

type (
	Record struct {
		FIGI       string `json:"figi"`
		Name       string `json:"name"`
		Ticker     string `json:"ticker"`
		ShareClass string `json:"shareClassFIGI"`
		Exchange   string `json:"exchCode"`
		Market     string `json:"marketSector"`
	}
	Result struct {
		Status, Evidence string
		Records          []Record
		Next             time.Time
	}
	Pending struct {
		ID         string
		Instrument source.Instrument
	}
	Evidence struct {
		At                time.Time
		Request, Response []byte
		Status            int
		Headers           http.Header
	}
	Store interface {
		ReferenceKey(context.Context) ([]byte, string, error)
		SetReferenceKey(context.Context, []byte) error
		ReferenceFailure(context.Context, string) error
		ReserveReference(context.Context, time.Time, int) (bool, time.Time, error)
		ReferenceCooldown(context.Context, time.Time) error
		ReferenceAllowance(context.Context, int, time.Time) error
		ReferenceCached(context.Context, string, time.Time) (Result, bool, error)
		SaveReference(context.Context, Evidence, []Job, []Result) ([]Result, error)
		PendingReference(context.Context, time.Time) ([]Pending, error)
		ApplyReference(context.Context, string, Result) error
	}
)

type Service struct {
	store    Store
	envelope *credential.Envelope
	client   *http.Client
	url      string
	now      func() time.Time
	mu       sync.Mutex
}

func New(
	store Store,
	envelope *credential.Envelope,
	client *http.Client,
	endpoint string,
	now func() time.Time,
) (*Service, error) {
	if endpoint == "" {
		endpoint = "https://api.openfigi.com/v3/mapping"
	}

	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" ||
		u.Fragment != "" {
		return nil, provider.ErrInvalid
	}

	if client == nil {
		client = http.DefaultClient
	}

	copyClient := *client
	copyClient.Timeout = 30 * time.Second
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	if now == nil {
		now = time.Now
	}

	return &Service{store: store, envelope: envelope, client: &copyClient, url: endpoint, now: now}, nil
}

var keyAAD = []byte("wake/openfigi/key/1")

func (s *Service) SetKey(ctx context.Context, key *credential.Secret) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if key == nil {
		return provider.ErrInvalid
	}

	b, err := key.Copy()
	if err != nil {
		return provider.ErrUnauthorized
	}
	defer clear(b)

	if len(b) == 0 {
		return s.store.SetReferenceKey(ctx, []byte{})
	}

	if len(b) > 4096 || bytes.ContainsAny(b, "\r\n\x00") || s.envelope == nil {
		return provider.ErrUnauthorized
	}

	encrypted, err := s.envelope.Encrypt(key, keyAAD)
	if err != nil {
		return provider.ErrUnauthorized
	}

	payload, err := encrypted.MarshalBinary()
	if err != nil {
		return provider.ErrUnauthorized
	}

	return s.store.SetReferenceKey(ctx, payload)
}

func job(p Pending) (Job, bool) {
	// Symbols alone cannot identify a share class. Prefer an explicit identifier.
	for _, kind := range []source.IdentifierKind{source.VenueFIGI, source.UnclassifiedFIGI, source.ISIN, source.CUSIP, source.SEDOL, source.ShareClassFIGI} {
		for _, id := range p.Instrument.Identifiers() {
			if id.Kind() != kind {
				continue
			}

			idType := ""
			switch kind {
			case source.ISIN:
				idType = "ID_ISIN"
			case source.CUSIP:
				idType = "ID_CUSIP"
			case source.SEDOL:
				idType = "ID_SEDOL"
			default:
				idType = "ID_BB_GLOBAL"
			}

			return Job{IDType: idType, IDValue: id.Value()}, true
		}
	}

	return Job{}, false
}

// RunOnce serializes shared admission and suppresses duplicate jobs before HTTP.
// Its boolean reports committed work, for refresh notifications.
func (s *Service) RunOnce(ctx context.Context) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now().UTC()

	pending, err := s.store.PendingReference(ctx, now)
	if err != nil || len(pending) == 0 {
		return false, err
	}

	payload, failure, err := s.store.ReferenceKey(ctx)
	if err != nil {
		return false, err
	}

	if failure != "" {
		return false, nil
	}

	var secret []byte
	if len(payload) > 0 {
		if s.envelope == nil {
			return false, s.store.ReferenceFailure(
				ctx,
				"configured OpenFIGI key cannot be decrypted; replace the key",
			)
		}

		encrypted, e := credential.ParseEncryptedSecret(payload)
		if e != nil {
			return false, s.store.ReferenceFailure(
				ctx,
				"configured OpenFIGI key cannot be decrypted; replace the key",
			)
		}

		value, e := s.envelope.Decrypt(encrypted, keyAAD)
		if e != nil {
			return false, s.store.ReferenceFailure(
				ctx,
				"configured OpenFIGI key cannot be decrypted; replace the key",
			)
		}
		defer value.Clear()

		secret, e = value.Copy()
		if e != nil {
			return false, provider.ErrUnauthorized
		}
		defer clear(secret)
	}

	limit, batch := 25, 10
	if len(secret) > 0 {
		limit, batch = 250, 100
	}

	jobs := []Job{}
	owners := map[string][]string{}
	changed := false
	for _, p := range pending {
		j, ok := job(p)
		if !ok {
			if err = s.store.ApplyReference(
				ctx,
				p.ID,
				Result{Status: "provisional", Next: now.Add(24 * time.Hour)},
			); err != nil {
				return changed, err
			}

			changed = true

			continue
		}

		cached, found, e := s.store.ReferenceCached(ctx, j.Key(), now)
		if e != nil {
			return changed, e
		}

		if found {
			if e = s.store.ApplyReference(ctx, p.ID, cached); e != nil {
				return changed, e
			}

			changed = true

			continue
		}

		if _, found = owners[j.Key()]; !found {
			if len(jobs) == batch {
				continue
			}

			jobs = append(jobs, j)
		}

		owners[j.Key()] = append(owners[j.Key()], p.ID)
	}

	if len(jobs) == 0 {
		return changed, nil
	}

	admitted, next, err := s.store.ReserveReference(ctx, now, limit)
	if err != nil {
		return changed, err
	}

	if !admitted {
		return changed, nil
	}

	requestBytes, err := json.Marshal(jobs)
	if err != nil {
		return changed, err
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url, bytes.NewReader(requestBytes))
	if err != nil {
		return changed, provider.ErrInvalid
	}

	request.Header.Set("Content-Type", "application/json")

	if len(secret) > 0 {
		request.Header.Set("X-OPENFIGI-APIKEY", string(secret))
	}

	response, callErr := s.client.Do(request)

	evidence := Evidence{At: now, Request: requestBytes, Response: []byte{}, Headers: http.Header{}}
	results := make([]Result, len(jobs))
	for i := range results {
		results[i] = Result{Status: "temporary-failure", Next: now.Add(time.Minute)}
	}

	if callErr == nil {
		defer func() { _ = response.Body.Close() }()

		evidence.Status = response.StatusCode
		for _, name := range []string{"Ratelimit-Limit", "Ratelimit-Remaining", "Ratelimit-Reset", "Retry-After"} {
			if value := response.Header.Get(name); value != "" {
				evidence.Headers.Set(name, value)
			}
		}

		body, readErr := io.ReadAll(io.LimitReader(response.Body, (16<<20)+1))
		if readErr == nil && len(body) <= 16<<20 {
			if len(secret) > 0 && bytes.Contains(body, secret) {
				body = []byte(`{"error":"response contained credential material"}`)
			} else if response.StatusCode == http.StatusOK {
				var decoded []struct {
					Data    []Record `json:"data"`
					Error   string   `json:"error"`
					Warning string   `json:"warning"`
				}
				if json.Unmarshal(body, &decoded) == nil && len(decoded) == len(jobs) {
					for i, r := range decoded {
						switch {
						case len(r.Data) == 1 && r.Data[0].ShareClass != "" && r.Data[0].Name != "" && r.Data[0].Market == "Equity":
							results[i] = Result{Status: "resolved", Records: r.Data, Next: now.Add(7 * 24 * time.Hour)}
						case len(r.Data) > 0:
							results[i] = Result{Status: "ambiguous", Records: r.Data, Next: now.Add(7 * 24 * time.Hour)}
						case r.Warning != "" || r.Error == "No identifier found.":
							results[i] = Result{Status: "no-match", Next: now.Add(6 * time.Hour)}
						}
					}
				}
			}

			evidence.Response = body
		}

		cooldown := time.Time{}

		reset, _ := strconv.Atoi(response.Header.Get("Ratelimit-Reset"))
		if reset < 1 || reset > 86400 {
			reset = 60
		}

		if response.StatusCode == http.StatusTooManyRequests ||
			response.Header.Get("Ratelimit-Remaining") == "0" {
			cooldown = now.Add(time.Duration(reset) * time.Second)
		}

		if seconds, e := strconv.Atoi(
			response.Header.Get("Retry-After"),
		); e == nil && seconds > 0 &&
			seconds <= 86400 {
			candidate := now.Add(time.Duration(seconds) * time.Second)
			if candidate.After(cooldown) {
				cooldown = candidate
			}
		}

		if !cooldown.IsZero() {
			if err = s.store.ReferenceCooldown(ctx, cooldown); err != nil {
				return changed, err
			}

			next = cooldown
		}

		if remaining, e := strconv.Atoi(response.Header.Get("Ratelimit-Remaining")); e == nil && remaining >= 0 {
			if err = s.store.ReferenceAllowance(
				ctx,
				limit-min(remaining, limit),
				now.Add(time.Duration(reset)*time.Second),
			); err != nil {
				return changed, err
			}
		}

		if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
			for i := range results {
				results[i].Status = "invalid-key"
			}

			if err = s.store.ReferenceFailure(
				ctx,
				"OpenFIGI rejected the configured key; replace it and retry",
			); err != nil {
				return changed, err
			}
		}

		if response.StatusCode == http.StatusTooManyRequests {
			for i := range results {
				results[i] = Result{Status: "throttled", Next: next}
			}
		}
	}

	if ctx.Err() != nil {
		return changed, ctx.Err()
	}

	results, err = s.store.SaveReference(ctx, evidence, jobs, results)
	if err != nil {
		return changed, err
	}

	for i, j := range jobs {
		for _, id := range owners[j.Key()] {
			if err = s.store.ApplyReference(ctx, id, results[i]); err != nil {
				return changed, err
			}

			changed = true
		}
	}

	return changed, nil
}
