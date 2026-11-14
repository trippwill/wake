// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

// Package clientaccess manages independently revocable financial read grants.
package clientaccess

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"go.trippwill.dev/wake/internal/credential"
)

type Error string

func (e Error) Error() string { return string(e) }

const (
	ErrUnauthorized   Error = "client authentication required"
	ErrInvalidClient  Error = "invalid client details"
	ErrBusy           Error = "too many active financial requests"
	MaxClients              = 100
	MaxRequests             = 64
	MaxClientRequests       = 8
)

// Client contains only the public metadata of an active grant.
type Client struct {
	id, name string
	created  time.Time
}

func NewClient(id, name string, created time.Time) (Client, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(id)
	if err != nil || len(decoded) != 16 || base64.RawURLEncoding.EncodeToString(decoded) != id ||
		name == "" || len(name) > 100 || !utf8.ValidString(name) || strings.TrimSpace(name) != name ||
		strings.ContainsFunc(name, unicode.IsControl) || created.IsZero() {
		return Client{}, ErrInvalidClient
	}

	return Client{id: id, name: name, created: created.UTC()}, nil
}

func (c Client) ID() string         { return c.id }
func (c Client) Name() string       { return c.name }
func (c Client) Created() time.Time { return c.created }

type Store interface {
	Clients(context.Context) ([]Client, error)
	CreateClient(context.Context, Client, []byte) error
	ClientVerifier(context.Context, string) ([]byte, error)
	RevokeClient(context.Context, string) error
}

// Manager serializes revocation with admission. Only it mutates running grants.
type Manager struct {
	store  Store
	mu     sync.Mutex
	active map[string]map[*request]struct{}
	total  int
}

type request struct{ cancel context.CancelFunc }

func New(store Store) *Manager {
	return &Manager{store: store, active: make(map[string]map[*request]struct{})}
}

func (m *Manager) Clients(ctx context.Context) ([]Client, error) { return m.store.Clients(ctx) }

func (m *Manager) Create(ctx context.Context, name string) (Client, *credential.Secret, error) {
	var (
		id     [16]byte
		secret [32]byte
	)

	_, _ = rand.Read(id[:])

	_, _ = rand.Read(secret[:])
	defer clear(secret[:])

	client, err := NewClient(base64.RawURLEncoding.EncodeToString(id[:]), name, time.Now())
	if err != nil {
		return Client{}, nil, err
	}

	value := []byte(client.ID() + "." + base64.RawURLEncoding.EncodeToString(secret[:]))
	defer clear(value)

	verifier := sha256.Sum256(value)

	m.mu.Lock()
	defer m.mu.Unlock()

	clients, err := m.store.Clients(ctx)
	if err != nil {
		return Client{}, nil, err
	}

	if len(clients) >= MaxClients {
		return Client{}, nil, ErrBusy
	}

	if err := m.store.CreateClient(ctx, client, verifier[:]); err != nil {
		return Client{}, nil, err
	}

	return client, credential.NewSecret(value), nil
}

// TokenID admits only the canonical fixed-size credential representation.
func TokenID(value string) (string, error) {
	if len(value) != 66 || value[22] != '.' {
		return "", ErrUnauthorized
	}

	id, err := base64.RawURLEncoding.DecodeString(value[:22])
	if err != nil || len(id) != 16 || base64.RawURLEncoding.EncodeToString(id) != value[:22] {
		return "", ErrUnauthorized
	}

	secret, err := base64.RawURLEncoding.DecodeString(value[23:])
	defer clear(secret)

	if err != nil || len(secret) != 32 || base64.RawURLEncoding.EncodeToString(secret) != value[23:] {
		return "", ErrUnauthorized
	}

	return value[:22], nil
}

// Admit checks storage on every request; storage failures never grant access.
// The returned release joins request lifetime to revocation and must be called.
func (m *Manager) Admit(ctx context.Context, value string) (context.Context, func(), error) {
	id, err := TokenID(value)
	if err != nil {
		return nil, nil, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	verifier, err := m.store.ClientVerifier(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	defer clear(verifier)

	actual := sha256.Sum256([]byte(value))
	if len(verifier) != sha256.Size || subtle.ConstantTimeCompare(actual[:], verifier) != 1 {
		return nil, nil, ErrUnauthorized
	}

	if m.total >= MaxRequests || len(m.active[id]) >= MaxClientRequests {
		return nil, nil, ErrBusy
	}

	ctx, cancel := context.WithCancel(ctx)
	r := &request{cancel: cancel}

	if m.active[id] == nil {
		m.active[id] = make(map[*request]struct{})
	}

	m.active[id][r] = struct{}{}
	m.total++

	return ctx, func() {
		cancel()
		m.mu.Lock()
		defer m.mu.Unlock()

		if _, ok := m.active[id][r]; ok {
			delete(m.active[id], r)

			m.total--
			if len(m.active[id]) == 0 {
				delete(m.active, id)
			}
		}
	}, nil
}

func (m *Manager) Revoke(ctx context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.store.RevokeClient(ctx, id); err != nil {
		return err
	}

	for r := range m.active[id] {
		r.cancel()
	}

	return nil
}
