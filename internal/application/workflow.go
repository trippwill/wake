// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package application

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"go.trippwill.dev/wake/internal/credential"
	"go.trippwill.dev/wake/internal/host"
	"go.trippwill.dev/wake/internal/logging"
	"go.trippwill.dev/wake/provider"
	"go.trippwill.dev/wake/source"
)

type (
	StorageStatus struct {
		Paused, ReferenceReview, WarehouseReset bool
		Revision                                int64
	}
	Candidate struct {
		Artifact  string
		Candidate provider.Candidate
	}
	WorkStatus       struct{ Operation, Progress, Failure, Next string }
	ProcessingStatus struct {
		Artifact, Failure                                                         string
		TradeClaims, LifecycleClaims, ConversionClaims, ExcludedRows, NeedsReview int64
		Complete                                                                  bool
		RequestedFrom, RequestedTo, ReportedFrom, ReportedTo                      string
	}
	ConnectionView struct {
		ID, Name, Query, Provider        string
		MissingAccess                    bool
		SettingsStored, CredentialStored bool
		Candidates                       []Candidate
		Work                             []WorkStatus
		Processing                       []ProcessingStatus
	}
	Dashboard struct {
		Storage          StorageStatus
		Connections      []ConnectionView
		Account          Account
		Executions       []Execution
		Lifecycles       []Lifecycle
		Conversions      []Conversion
		Corrections      []CorrectionReview
		Recovery         []RecoveryJob
		Instruments      []Instrument
		ReferenceKey     bool
		ReferenceFailure string
	}
	Store interface {
		ResumeStorage(context.Context, int64, string, time.Time) error
		Schedule(context.Context, host.WorkKey, bool) error
		Confirm(context.Context, string, string, source.Reference, AccountDetails) (Account, error)
		Dashboard(context.Context) (Dashboard, error)
		PendingWork(context.Context, time.Time) ([]host.WorkKey, error)
		PendingArtifacts(context.Context, time.Time) ([]string, error)
		Process(context.Context, string, string) error
		ProcessingFailure(context.Context, string, time.Time) error
		ExecutionDetail(context.Context, string) (ExecutionDetail, error)
		LifecycleDetail(context.Context, string) (LifecycleDetail, error)
		ConversionDetail(context.Context, string) (ConversionDetail, error)
		InstrumentDetail(context.Context, string) (InstrumentDetail, error)
		Retained(context.Context, string) (host.Retained, error)
		EvidenceContext(context.Context, string) (EvidenceContext, error)
		ReferenceEvidence(context.Context, string) (ReferenceEvidence, error)
		CorrectionForm(context.Context, string, string) (CorrectionForm, error)
		SaveDecision(context.Context, Decision, string, time.Time) error
		CheckCorrection(context.Context, Decision) error
		RetryProcessing(context.Context, string) error
		StartRebuild(context.Context, string, string, time.Time) (string, error)
		RunRecovery(context.Context) (bool, error)
		RetryRecovery(context.Context, string) error
	}
)

type (
	Reference interface {
		CheckAccess(context.Context) error
		RunOnce(context.Context) (bool, error)
		SetKey(context.Context, *credential.Secret) error
	}
	Workflow struct {
		logger                    *slog.Logger
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
	logger *slog.Logger,
) *Workflow {
	if now == nil {
		now = time.Now
	}

	return &Workflow{
		logger:      logging.Component(logger, "workflow"),
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
) (idResult string, resultErr error) {
	ctx, finishLog := logging.Start(ctx, w.logger, "connection_create")
	defer func() { finishLog(resultErr) }()

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

func (w *Workflow) ReplaceToken(ctx context.Context, id string, token *credential.Secret) (resultErr error) {
	ctx, finishLog := logging.Start(ctx, w.logger, "connection_access_repair")
	defer func() { finishLog(resultErr) }()

	if err := w.host.ReplaceSecret(ctx, id, "token", token); err != nil {
		return err
	}

	return w.Retry(ctx, id)
}

func (w *Workflow) UpdateConnection(ctx context.Context, id, name, query string) (resultErr error) {
	ctx, finishLog := logging.Start(ctx, w.logger, "connection_update")
	defer func() { finishLog(resultErr) }()

	settings, err := provider.NewSettings(map[string]string{"name": name, "query_id": query})
	if err != nil {
		return err
	}

	if err = w.host.UpdateSettings(ctx, id, settings); err != nil {
		return err
	}

	return w.Retry(ctx, id)
}

func (w *Workflow) Retry(ctx context.Context, id string) (resultErr error) {
	ctx, finishLog := logging.Start(ctx, w.logger, "acquisition_retry")
	defer func() { finishLog(resultErr) }()

	view, err := w.store.Dashboard(ctx)
	if err != nil {
		return err
	}

	operation := "discover"
	if view.Account.Connection() == id {
		operation = "acquire"
	}

	err = w.host.Retry(ctx, host.WorkKey{Connection: id, Stream: w.stream, Operation: operation})
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
) (result Account, resultErr error) {
	ctx, finishLog := logging.Start(ctx, w.logger, "account_confirm")
	defer func() { finishLog(resultErr) }()

	a, err := w.store.Confirm(ctx, id, artifact, ref, details)
	if err == nil {
		w.notify()
	}

	return a, err
}

func (w *Workflow) SetReferenceKey(ctx context.Context, key *credential.Secret) (resultErr error) {
	ctx, finishLog := logging.Start(ctx, w.logger, "reference_access_repair")
	defer func() { finishLog(resultErr) }()

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

		if err != nil && ctx.Err() == nil {
			w.logger.WarnContext(
				ctx,
				"durable workflow will retry",
				"event",
				"workflow_retry",
				"code",
				logging.Code(err),
				"retry_after_ms",
				1000,
			)
		} else if err == nil && failed {
			w.logger.InfoContext(ctx, "durable workflow recovered", "event", "workflow_recovered")
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
	changed, recoveryErr := w.store.RunRecovery(ctx)
	if changed {
		w.notifyReaders()
	}

	if recoveryErr != nil {
		return recoveryErr
	}

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
		work := logging.Correlate(ctx)
		if _, err = w.host.Normalize(work, id, w.version); err == nil {
			processContext, finish := logging.Start(work, w.logger, "process", "artifact_id", id)
			err = w.store.Process(processContext, id, w.version)
			finish(err)
		}

		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}

			next := now.Add(time.Minute)
			if e := w.store.ProcessingFailure(work, id, next); e != nil {
				return e
			}

			w.logger.WarnContext(
				work,
				"processing retry retained",
				"event",
				"processing_deferred",
				"operation_id",
				logging.ID(work),
				"artifact_id",
				id,
				"code",
				logging.Code(err),
				"next_eligible_at",
				next.UTC(),
			)
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

func (w *Workflow) LifecycleDetail(ctx context.Context, id string) (LifecycleDetail, error) {
	return w.store.LifecycleDetail(ctx, id)
}

func (w *Workflow) ConversionDetail(ctx context.Context, id string) (ConversionDetail, error) {
	return w.store.ConversionDetail(ctx, id)
}

func (w *Workflow) CorrectionForm(ctx context.Context, kind, id string) (CorrectionForm, error) {
	return w.store.CorrectionForm(ctx, kind, id)
}

func (w *Workflow) SaveDecision(ctx context.Context, d Decision, actor string) (resultErr error) {
	ctx, finishLog := logging.Start(ctx, w.logger, "decision_save")
	defer func() { finishLog(resultErr) }()

	err := w.store.SaveDecision(ctx, d, actor, w.now())
	if err == nil {
		w.notify()
	}

	return err
}

func (w *Workflow) RetryProcessing(ctx context.Context, id string) (resultErr error) {
	ctx, finishLog := logging.Start(ctx, w.logger, "processing_retry")
	defer func() { finishLog(resultErr) }()

	err := w.store.RetryProcessing(ctx, id)
	if err == nil {
		w.notify()
	}

	return err
}

func (w *Workflow) StartRebuild(
	ctx context.Context,
	submission, actor string,
) (result string, resultErr error) {
	ctx, finishLog := logging.Start(ctx, w.logger, "rebuild_request")
	defer func() { finishLog(resultErr) }()

	id, err := w.store.StartRebuild(ctx, submission, actor, w.now())
	if err == nil {
		w.notify()
	}

	return id, err
}

func (w *Workflow) RetryRecovery(ctx context.Context, id string) (resultErr error) {
	ctx, finishLog := logging.Start(ctx, w.logger, "rebuild_retry")
	defer func() { finishLog(resultErr) }()

	err := w.store.RetryRecovery(ctx, id)
	if err == nil {
		w.notify()
	}

	return err
}

// ResumeStorage grants upstream authority only after local access is repaired and reviewed.
func (w *Workflow) ResumeStorage(ctx context.Context, revision int64, actor string) (resultErr error) {
	ctx, finishLog := logging.Start(ctx, w.logger, "storage_resume")
	defer func() { finishLog(resultErr) }()

	view, err := w.store.Dashboard(ctx)
	if err != nil {
		return err
	}

	if view.Storage.Revision != revision {
		return ErrStale
	}

	if view.Storage.ReferenceReview {
		return ErrAccessReview
	}

	for _, connection := range view.Connections {
		if connection.MissingAccess {
			return ErrAccessReview
		}

		if err = w.host.CheckAccess(ctx, connection.ID); err != nil {
			return err
		}
	}

	if w.reference != nil {
		if err = w.reference.CheckAccess(ctx); err != nil {
			return err
		}
	}

	if view.Storage.WarehouseReset {
		for _, connection := range view.Connections {
			if connection.Provider != w.provider {
				continue
			}

			if err = w.store.Schedule(
				ctx,
				host.WorkKey{Connection: connection.ID, Stream: w.stream, Operation: "discover"},
				true,
			); err != nil {
				return err
			}
		}
	}

	err = w.store.ResumeStorage(ctx, revision, actor, w.now())
	if err == nil {
		w.notify()
	}

	return err
}

func (w *Workflow) EvidenceContext(ctx context.Context, id string) (EvidenceContext, error) {
	return w.store.EvidenceContext(ctx, id)
}

type (
	EvidenceObservation struct{ ActivityID, Location, Outcome, Rule, Version string }
	EvidenceContext     struct {
		ConnectionName, Operation, RequestedFrom, RequestedTo, ReportedFrom, ReportedTo string
		Observations                                                                    []EvidenceObservation
	}
)

// CheckCorrection checks an edit without retaining a decision or scheduling work.
func (w *Workflow) CheckCorrection(ctx context.Context, d Decision) error {
	return w.store.CheckCorrection(ctx, d)
}
