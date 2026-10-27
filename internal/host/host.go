// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

// Package host owns bounded provider work, scoped credentials and joined lifetime.
package host

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"go.trippwill.dev/wake/internal/credential"
	"go.trippwill.dev/wake/provider"
)

type Error string

func (e Error) Error() string { return string(e) }

const (
	ErrStopped Error = "provider host stopped"
	ErrBusy    Error = "connection already running"
	ErrStorage Error = "provider storage failed"
)

type (
	Connection struct {
		ID, Provider string
		Settings     provider.Settings
	}
	WorkKey    struct{ Connection, Stream, Operation string }
	Checkpoint struct {
		Revision               int64
		State                  provider.State
		Next                   time.Time
		Observed, Acknowledged provider.Progress
		Failure                string
	}
	Retained struct {
		ID, Connection, Provider, Stream string
		Artifact                         provider.Artifact
	}
	// Store is the host's transactional persistence capability, never passed to providers.
	Store interface {
		Configure(context.Context, Connection, map[string][]byte) error
		Reconfigure(context.Context, Connection) error
		Connection(context.Context, string) (Connection, error)
		Secret(context.Context, string, string) ([]byte, error)
		ReplaceSecret(context.Context, string, string, []byte) error
		Checkpoint(context.Context, WorkKey) (Checkpoint, error)
		Observe(context.Context, WorkKey, provider.Progress) error
		Fail(context.Context, WorkKey, string) error
		Accept(
			context.Context,
			WorkKey,
			int64,
			provider.Step,
			[]provider.Candidate,
			provider.Progress,
		) (string, error)
		Retained(context.Context, string) (Retained, error)
		Normalize(context.Context, string, string, provider.Normalizer) (provider.Result, error)
		Reserve(context.Context, string, string, string, time.Time, time.Duration) (provider.Reservation, error)
		Delay(context.Context, string, string, string, time.Time) error
	}
)

type Host struct {
	store         Store
	envelope      *credential.Envelope
	registrations map[string]provider.Registration
	now           func() time.Time
	ctx           context.Context
	cancel        context.CancelFunc
	mu            sync.Mutex
	stopped       bool
	running       map[string]bool
	active        sync.WaitGroup
	once          sync.Once
}

func New(
	ctx context.Context,
	store Store,
	envelope *credential.Envelope,
	now func() time.Time,
	registrations ...provider.Registration,
) (*Host, error) {
	if store == nil {
		return nil, provider.ErrInvalid
	}

	if now == nil {
		now = time.Now
	}

	runtime, cancel := context.WithCancel(ctx)
	h := &Host{
		store:         store,
		envelope:      envelope,
		registrations: map[string]provider.Registration{},
		now:           now,
		ctx:           runtime,
		cancel:        cancel,
		running:       map[string]bool{},
	}
	for _, r := range registrations {
		id := r.Descriptor().ID()
		if id == "" || h.registrations[id].Descriptor().ID() != "" {
			cancel()
			return nil, provider.ErrInvalid
		}

		h.registrations[id] = r
	}

	return h, nil
}

func (h *Host) Close() {
	h.once.Do(func() { h.mu.Lock(); h.stopped = true; h.cancel(); h.mu.Unlock(); h.active.Wait() })
}

func (h *Host) admit(ctx context.Context, key string) (context.Context, func(), error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.stopped || h.ctx.Err() != nil {
		return nil, nil, ErrStopped
	}

	if h.running[key] {
		return nil, nil, ErrBusy
	}

	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}

	h.running[key] = true
	h.active.Add(1)

	work, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(h.ctx, cancel)

	return work, func() {
		stop()
		cancel()
		h.mu.Lock()
		delete(h.running, key)
		h.mu.Unlock()
		h.active.Done()
	}, nil
}

func (h *Host) Configure(
	ctx context.Context,
	id string,
	settings provider.Settings,
	secrets map[string]*credential.Secret,
) (string, error) {
	r, ok := h.registrations[id]
	if !ok {
		return "", provider.ErrUnavailable
	}

	var prepareErr error

	settings, prepareErr = r.PrepareSettings(settings)
	if prepareErr != nil {
		return "", safeError(prepareErr)
	}
	// Validate every setting, including unexpected keys, before opening secrets.
	allowed := map[string]provider.Field{}
	for _, f := range r.Descriptor().Fields() {
		allowed[f.Key()] = f
	}

	for key := range settings.Values() {
		f, ok := allowed[key]
		if !ok || f.Kind() != provider.TextField {
			return "", provider.ErrInvalid
		}
	}

	for key := range secrets {
		f, ok := allowed[key]
		if !ok || f.Kind() != provider.SecretField {
			return "", provider.ErrInvalid
		}
	}

	for _, f := range r.Descriptor().Fields() {
		if !f.Required() {
			continue
		}

		_, text := settings.Get(f.Key())
		if (f.Kind() == provider.TextField && !text) ||
			(f.Kind() == provider.SecretField && secrets[f.Key()] == nil) {
			return "", provider.ErrInvalid
		}
	}

	connection := Connection{ID: rand.Text(), Provider: id, Settings: settings}
	encrypted := map[string][]byte{}
	for key, secret := range secrets {
		if h.envelope == nil {
			return "", provider.ErrUnauthorized
		}

		value, err := h.envelope.Encrypt(secret, secretAAD(connection, key))
		if err != nil {
			return "", provider.ErrUnauthorized
		}

		payload, err := value.MarshalBinary()
		if err != nil {
			return "", provider.ErrUnauthorized
		}

		encrypted[key] = payload
	}

	work, finish, err := h.admit(ctx, connection.ID)
	if err != nil {
		return "", err
	}
	defer finish()

	if err = h.store.Configure(work, connection, encrypted); err != nil {
		return "", ErrStorage
	}

	return connection.ID, nil
}

func secretAAD(c Connection, key string) []byte {
	b, _ := json.Marshal([]string{c.ID, c.Provider, key})
	return b
}

func (h *Host) UpdateSettings(ctx context.Context, id string, settings provider.Settings) error {
	work, finish, err := h.admit(ctx, id)
	if err != nil {
		return err
	}
	defer finish()

	connection, err := h.store.Connection(work, id)
	if err != nil {
		return ErrStorage
	}

	r, ok := h.registrations[connection.Provider]
	if !ok {
		return provider.ErrUnavailable
	}

	settings, err = r.PrepareSettings(settings)
	if err != nil {
		return safeError(err)
	}

	allowed := map[string]bool{}
	for _, field := range r.Descriptor().Fields() {
		if field.Kind() == provider.TextField {
			allowed[field.Key()] = true
			if field.Required() {
				if _, found := settings.Get(field.Key()); !found {
					return provider.ErrInvalid
				}
			}
		}
	}

	for key := range settings.Values() {
		if !allowed[key] {
			return provider.ErrInvalid
		}
	}

	connection.Settings = settings
	if err = h.store.Reconfigure(work, connection); err != nil {
		return ErrStorage
	}

	return nil
}

// ReplaceSecret preserves connection identity and all durable continuations.
func (h *Host) ReplaceSecret(ctx context.Context, id, key string, secret *credential.Secret) error {
	work, finish, err := h.admit(ctx, id)
	if err != nil {
		return err
	}
	defer finish()

	c, err := h.store.Connection(work, id)
	if err != nil {
		return ErrStorage
	}

	r, ok := h.registrations[c.Provider]
	if !ok {
		return provider.ErrUnavailable
	}

	allowed := false
	for _, f := range r.Descriptor().Fields() {
		if f.Key() == key && f.Kind() == provider.SecretField {
			allowed = true
		}
	}

	if !allowed || h.envelope == nil || secret == nil {
		return provider.ErrUnauthorized
	}

	encrypted, err := h.envelope.Encrypt(secret, secretAAD(c, key))
	if err != nil {
		return provider.ErrUnauthorized
	}

	payload, err := encrypted.MarshalBinary()
	if err != nil {
		return provider.ErrUnauthorized
	}

	if err = h.store.ReplaceSecret(work, id, key, payload); err != nil {
		return ErrStorage
	}

	return nil
}

func (h *Host) Acquire(ctx context.Context, id, stream string) (string, error) {
	return h.step(ctx, id, stream, false)
}

func (h *Host) Discover(ctx context.Context, id, stream string) (string, error) {
	return h.step(ctx, id, stream, true)
}

func (h *Host) step(ctx context.Context, id, stream string, discover bool) (string, error) {
	work, finish, err := h.admit(ctx, id)
	if err != nil {
		return "", err
	}
	defer finish()

	c, err := h.store.Connection(work, id)
	if err != nil {
		return "", err
	}

	r, ok := h.registrations[c.Provider]
	if !ok {
		return "", provider.ErrUnavailable
	}

	format := ""
	for _, s := range r.Descriptor().Streams() {
		if s.ID() == stream {
			format = s.Format()
		}
	}

	if format == "" {
		return "", provider.ErrUnavailable
	}

	if (discover && r.Discoverer() == nil) || (!discover && r.Acquirer() == nil) {
		return "", provider.ErrUnavailable
	}

	operation := "acquire"
	if discover {
		operation = "discover"
	}

	key := WorkKey{id, stream, operation}

	checkpoint, err := h.store.Checkpoint(work, key)
	if err != nil {
		return "", ErrStorage
	}

	now := h.now().UTC()
	if checkpoint.Next.After(now) {
		return "", provider.ErrTemporary
	}

	access := &access{host: h, connection: c, registration: r, key: key, live: true, ctx: work}

	request := provider.Request{Stream: stream, Settings: c.Settings, State: checkpoint.State, Now: now}

	var (
		step       provider.Step
		candidates []provider.Candidate
	)
	if discover {
		var result provider.Discovery

		result, err = r.Discoverer().Discover(work, request, access)
		step = result.Step()
		candidates = result.Candidates()
	} else {
		step, err = r.Acquirer().Acquire(work, request, access)
	}

	access.close()

	if err == nil {
		err = work.Err()
	}

	if err == nil && access.failure != nil {
		err = ErrStorage
	}

	if err != nil {
		safe := safeError(err)
		h.fail(work, key, safe)

		return "", safe
	}

	if step.NextEligibleAt().IsZero() || (!step.Artifact().IsZero() && step.Artifact().Format() != format) {
		h.fail(work, key, provider.ErrInvalid)
		return "", provider.ErrInvalid
	}

	artifact, err := h.store.Accept(work, key, checkpoint.Revision, step, candidates, access.progress)
	if err != nil {
		if errors.Is(err, provider.ErrConflict) {
			return "", provider.ErrConflict
		}

		h.fail(work, key, ErrStorage)

		return "", ErrStorage
	}

	return artifact, nil
}

func (h *Host) fail(ctx context.Context, key WorkKey, err error) {
	detached, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()

	_ = h.store.Fail(detached, key, err.Error())
}

func safeError(err error) error {
	for _, kind := range []error{context.Canceled, context.DeadlineExceeded, ErrStorage, provider.ErrUnauthorized, provider.ErrTemporary, provider.ErrUnavailable, provider.ErrMalformed, provider.ErrLimit} {
		if errors.Is(err, kind) {
			return kind
		}
	}

	return provider.ErrInvalid
}

func (h *Host) Normalize(ctx context.Context, id, version string) (provider.Result, error) {
	work, finish, err := h.admit(ctx, "artifact:"+id)
	if err != nil {
		return provider.Result{}, err
	}
	defer finish()

	retained, err := h.store.Retained(work, id)
	if err != nil {
		return provider.Result{}, err
	}

	r, ok := h.registrations[retained.Provider]
	if !ok || r.Version() != version || r.Normalizer() == nil {
		return provider.Result{}, provider.ErrUnavailable
	}

	return h.store.Normalize(work, id, version, r.Normalizer())
}

type access struct {
	ctx          context.Context
	host         *Host
	connection   Connection
	registration provider.Registration
	key          WorkKey
	mu           sync.Mutex
	live         bool
	active       sync.WaitGroup
	progress     provider.Progress
	failure      error
}

func (a *access) enter(ctx context.Context) (func(), error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if !a.live {
		return nil, provider.ErrUnauthorized
	}

	if err := a.ctx.Err(); err != nil {
		return nil, err
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	a.active.Add(1)

	return a.active.Done, nil
}
func (a *access) close() { a.mu.Lock(); a.live = false; a.mu.Unlock(); a.active.Wait() }
func (a *access) WithSecret(ctx context.Context, key string, fn func([]byte) error) error {
	done, err := a.enter(ctx)
	if err != nil {
		return err
	}
	defer done()

	allowed := false
	for _, f := range a.registration.Descriptor().Fields() {
		if f.Key() == key && f.Kind() == provider.SecretField {
			allowed = true
		}
	}

	if !allowed || a.host.envelope == nil || fn == nil {
		return provider.ErrUnauthorized
	}

	payload, err := a.host.store.Secret(ctx, a.connection.ID, key)
	if err != nil {
		return provider.ErrUnauthorized
	}

	encrypted, err := credential.ParseEncryptedSecret(payload)
	if err != nil {
		return provider.ErrUnauthorized
	}

	secret, err := a.host.envelope.Decrypt(encrypted, secretAAD(a.connection, key))
	if err != nil {
		return provider.ErrUnauthorized
	}
	defer secret.Clear()

	bytes, err := secret.Copy()
	if err != nil {
		return provider.ErrUnauthorized
	}
	defer clear(bytes)

	return fn(bytes)
}

func (a *access) Report(ctx context.Context, p provider.Progress) error {
	done, err := a.enter(ctx)
	if err != nil {
		return err
	}
	defer done()

	if p.Code() == "" {
		return provider.ErrInvalid
	}

	err = a.host.store.Observe(ctx, a.key, p)
	a.mu.Lock()
	defer a.mu.Unlock()

	if err != nil {
		a.failure = err
		return ErrStorage
	}

	a.progress = p

	return nil
}

func (a *access) policy(id string) (provider.PacingPolicy, error) {
	for _, p := range a.registration.Descriptor().Pacing() {
		if p.ID() == id {
			return p, nil
		}
	}

	return provider.PacingPolicy{}, provider.ErrInvalid
}

func (a *access) ReservePacing(
	ctx context.Context,
	id string,
	scope provider.PacingScope,
) (provider.Reservation, error) {
	done, err := a.enter(ctx)
	if err != nil {
		return provider.Reservation{}, err
	}
	defer done()

	p, err := a.policy(id)
	if err != nil || scope.Digest() == "" {
		return provider.Reservation{}, provider.ErrInvalid
	}

	result, err := a.host.store.Reserve(
		ctx,
		a.connection.Provider,
		id,
		scope.Digest(),
		a.host.now(),
		p.Spacing(),
	)
	if err != nil {
		return provider.Reservation{}, a.storageFailure(err)
	}

	return result, nil
}

func (a *access) DelayPacing(
	ctx context.Context,
	id string,
	scope provider.PacingScope,
	until time.Time,
) error {
	done, err := a.enter(ctx)
	if err != nil {
		return err
	}
	defer done()

	if _, err = a.policy(id); err != nil || scope.Digest() == "" || until.IsZero() {
		return provider.ErrInvalid
	}

	if err = a.host.store.Delay(ctx, a.connection.Provider, id, scope.Digest(), until); err != nil {
		return a.storageFailure(err)
	}

	return nil
}

func (a *access) storageFailure(err error) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.failure = err

	return ErrStorage
}
