// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package application

import (
	"context"
	"errors"
	"sync"
	"time"

	"go.trippwill.dev/wake/internal/credential"
	"go.trippwill.dev/wake/internal/host"
	"go.trippwill.dev/wake/provider"
	"go.trippwill.dev/wake/source"
)

type (
	Candidate struct {
		Artifact  string
		Candidate provider.Candidate
	}
	WorkStatus       struct{ Operation, Progress, Failure, Next string }
	ProcessingStatus struct {
		Artifact, Failure                                    string
		TradeClaims, ExcludedRows, NeedsReview               int64
		Complete                                             bool
		RequestedFrom, RequestedTo, ReportedFrom, ReportedTo string
	}
	ConnectionView struct {
		ID, Name, Query string
		Candidates      []Candidate
		Work            []WorkStatus
		Processing      []ProcessingStatus
	}
	Dashboard struct {
		Connections      []ConnectionView
		Account          Account
		Executions       []Execution
		Instruments      []Instrument
		ReferenceKey     bool
		ReferenceFailure string
	}
	Store interface {
		Schedule(context.Context, host.WorkKey, bool) error
		Retry(context.Context, host.WorkKey) error
		Confirm(context.Context, string, string, source.Reference, AccountDetails) (Account, error)
		Dashboard(context.Context) (Dashboard, error)
		PendingWork(context.Context, time.Time) ([]host.WorkKey, error)
		PendingArtifacts(context.Context, time.Time) ([]string, error)
		Process(context.Context, string, string) error
		ProcessingFailure(context.Context, string, time.Time) error
		ExecutionDetail(context.Context, string) (ExecutionDetail, error)
		InstrumentDetail(context.Context, string) (InstrumentDetail, error)
		Retained(context.Context, string) (host.Retained, error)
		ReferenceEvidence(context.Context, string) (ReferenceEvidence, error)
	}
)

type (
	Reference interface {
		RunOnce(context.Context) (bool, error)
		SetKey(context.Context, *credential.Secret) error
	}
	Workflow struct {
		store                     Store
		host                      *host.Host
		reference                 Reference
		provider, stream, version string
		wake                      chan struct{}
		now                       func() time.Time
		mu                        sync.Mutex
		subscribers               map[chan struct{}]bool
	}
)

func New(
	store Store,
	h *host.Host,
	reference Reference,
	providerID, stream, version string,
	now func() time.Time,
) *Workflow {
	if now == nil {
		now = time.Now
	}

	return &Workflow{
		store:       store,
		host:        h,
		reference:   reference,
		provider:    providerID,
		stream:      stream,
		version:     version,
		wake:        make(chan struct{}, 1),
		now:         now,
		subscribers: map[chan struct{}]bool{},
	}
}

func (w *Workflow) notify() {
	w.mu.Lock()
	defer w.mu.Unlock()

	for ch := range w.subscribers {
		select {
		case ch <- struct{}{}:
		default:
		}
	}

	select {
	case w.wake <- struct{}{}:
	default:
	}
}

func (w *Workflow) Subscribe() (chan struct{}, func()) {
	ch := make(chan struct{}, 1)

	w.mu.Lock()
	w.subscribers[ch] = true
	w.mu.Unlock()

	return ch, func() { w.mu.Lock(); delete(w.subscribers, ch); w.mu.Unlock() }
}

func (w *Workflow) CreateConnection(
	ctx context.Context,
	name, query string,
	token *credential.Secret,
) (string, error) {
	settings, err := provider.NewSettings(map[string]string{"name": name, "query_id": query})
	if err != nil {
		return "", err
	}

	id, err := w.host.Configure(ctx, w.provider, settings, map[string]*credential.Secret{"token": token})
	if err != nil {
		return "", err
	}

	err = w.store.Schedule(ctx, host.WorkKey{Connection: id, Stream: w.stream, Operation: "discover"}, true)
	if err == nil {
		w.notify()
	}

	return id, err
}

func (w *Workflow) ReplaceToken(ctx context.Context, id string, token *credential.Secret) error {
	if err := w.host.ReplaceSecret(ctx, id, "token", token); err != nil {
		return err
	}

	return w.Retry(ctx, id)
}

func (w *Workflow) UpdateConnection(ctx context.Context, id, name, query string) error {
	settings, err := provider.NewSettings(map[string]string{"name": name, "query_id": query})
	if err != nil {
		return err
	}

	if err = w.host.UpdateSettings(ctx, id, settings); err != nil {
		return err
	}

	return w.Retry(ctx, id)
}

func (w *Workflow) Retry(ctx context.Context, id string) error {
	view, err := w.store.Dashboard(ctx)
	if err != nil {
		return err
	}

	operation := "discover"
	if view.Account.Connection() == id {
		operation = "acquire"
	}

	err = w.store.Retry(ctx, host.WorkKey{Connection: id, Stream: w.stream, Operation: operation})
	if err == nil {
		w.notify()
	}

	return err
}

func (w *Workflow) Confirm(
	ctx context.Context,
	id, artifact string,
	ref source.Reference,
	details AccountDetails,
) (Account, error) {
	a, err := w.store.Confirm(ctx, id, artifact, ref, details)
	if err == nil {
		w.notify()
	}

	return a, err
}

func (w *Workflow) SetReferenceKey(ctx context.Context, key *credential.Secret) error {
	err := w.reference.SetKey(ctx, key)
	if err == nil {
		w.notify()
	}

	return err
}

func (w *Workflow) Dashboard(ctx context.Context) (Dashboard, error) { return w.store.Dashboard(ctx) }

func (w *Workflow) ExecutionDetail(ctx context.Context, id string) (ExecutionDetail, error) {
	return w.store.ExecutionDetail(ctx, id)
}

func (w *Workflow) InstrumentDetail(ctx context.Context, id string) (InstrumentDetail, error) {
	return w.store.InstrumentDetail(ctx, id)
}

func (w *Workflow) Evidence(ctx context.Context, id string) (host.Retained, error) {
	return w.store.Retained(ctx, id)
}

func (w *Workflow) ReferenceEvidence(ctx context.Context, id string) (ReferenceEvidence, error) {
	return w.store.ReferenceEvidence(ctx, id)
}

// Run owns all scheduled work. The daemon cancels and joins it before storage and
// credential teardown. Each boundary below persists the progress it acknowledges.
func (w *Workflow) Run(ctx context.Context, reportFailure func()) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	failed := false

	for {
		if ctx.Err() != nil {
			return
		}

		err := w.RunOnce(ctx, w.now())
		if err != nil && !failed && ctx.Err() == nil && reportFailure != nil {
			reportFailure()
		}

		failed = err != nil

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-w.wake:
		}
	}
}

func (w *Workflow) RunOnce(ctx context.Context, now time.Time) error {
	work, err := w.store.PendingWork(ctx, now)
	if err != nil {
		return err
	}

	for _, key := range work {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		if key.Operation == "discover" {
			_, err = w.host.Discover(ctx, key.Connection, key.Stream)
		} else {
			_, err = w.host.Acquire(ctx, key.Connection, key.Stream)
		}

		if errors.Is(err, host.ErrStorage) {
			return err
		}
		// Safe source failures are retained by the host; independent work continues.
		if err == nil {
			w.notifyReaders()
		}
	}

	artifacts, err := w.store.PendingArtifacts(ctx, now)
	if err != nil {
		return err
	}

	for _, id := range artifacts {
		if _, err = w.host.Normalize(ctx, id, w.version); err == nil {
			err = w.store.Process(ctx, id, w.version)
		}

		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}

			if e := w.store.ProcessingFailure(ctx, id, now.Add(time.Minute)); e != nil {
				return e
			}
		}

		w.notifyReaders()
	}

	if w.reference != nil {
		var changed bool

		changed, err = w.reference.RunOnce(ctx)
		if changed {
			w.notifyReaders()
		}
	}

	return err
}

func (w *Workflow) notifyReaders() {
	w.mu.Lock()
	defer w.mu.Unlock()

	for ch := range w.subscribers {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}
