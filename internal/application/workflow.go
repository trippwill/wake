// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package application

import (
	"context"
	"errors"
	"log/slog"
	"slices"
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
	PendingArtifact struct{ ID, Provider string }
	Candidate       struct {
		Artifact  string
		Candidate provider.Candidate
	}
	WorkStatus struct {
		Operation, Failure, Next string
		Progress                 provider.ProgressPhase
	}
	ProcessingStatus struct {
		Artifact, Failure                                                         string
		Receipt, Retrieved, Hash, ComparisonPolicy                                string
		TradeClaims, LifecycleClaims, ConversionClaims, ExcludedRows, NeedsReview int64
		Complete                                                                  bool
		RequestedFrom, RequestedTo, ReportedFrom, ReportedTo                      string
	}
	ConnectionView struct {
		ID, Name, Provider                    string
		Settings                              provider.Settings
		Descriptor                            provider.Descriptor
		CredentialKeys                        []string
		CanDiscover, CanAcquire, CanNormalize bool
		MissingAccess                         bool
		SettingsStored, CredentialStored      bool
		Candidates                            []Candidate
		Work                                  []WorkStatus
		Processing                            []ProcessingStatus
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
		Confirm(context.Context, string, string, source.Reference, AccountDetails, bool) (Account, error)
		Dashboard(context.Context) (Dashboard, error)
		PendingWork(context.Context, time.Time) ([]host.WorkKey, error)
		PendingArtifacts(context.Context, time.Time, []string) ([]PendingArtifact, error)
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
		logger      *slog.Logger
		store       Store
		host        *host.Host
		reference   Reference
		wake        chan struct{}
		now         func() time.Time
		mu          sync.Mutex
		subscribers map[chan struct{}]bool
	}
)

func New(
	store Store,
	h *host.Host,
	reference Reference,
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

// Providers exposes only the compiled registrations selected by composition.
func (w *Workflow) Providers() []provider.Registration {
	if w.host == nil {
		return nil
	}

	return w.host.Registrations()
}

func (w *Workflow) Provider(id string) (provider.Registration, bool) {
	if w.host == nil {
		return provider.Registration{}, false
	}

	return w.host.Registration(id)
}

// ConnectionDescriptor reads provider metadata without decoding financial history.
func (w *Workflow) ConnectionDescriptor(ctx context.Context, id string) (provider.Descriptor, error) {
	if w.host == nil {
		return provider.Descriptor{}, provider.ErrUnavailable
	}

	connection, err := w.host.Connection(ctx, id)
	if err != nil {
		return provider.Descriptor{}, err
	}

	registration, ok := w.Provider(connection.Provider)
	if !ok {
		return provider.Descriptor{}, provider.ErrUnavailable
	}

	return registration.Descriptor(), nil
}

func (w *Workflow) CreateConnection(
	ctx context.Context,
	name, providerID string,
	settings provider.Settings,
	secrets map[string]*credential.Secret,
) (result string, resultErr error) {
	ctx, finishLog := logging.Start(ctx, w.logger, "connection_create")
	defer func() { finishLog(resultErr) }()

	registration, ok := w.Provider(providerID)
	if !ok {
		return "", provider.ErrUnavailable
	}

	id, err := w.host.Configure(ctx, providerID, name, settings, secrets)
	if err != nil {
		return "", err
	}

	if registration.Discoverer() != nil {
		for _, stream := range registration.Descriptor().Streams() {
			if err = w.store.Schedule(
				ctx,
				host.WorkKey{Connection: id, Stream: stream.ID(), Operation: "discover"},
				true,
			); err != nil {
				return id, err
			}
		}
	}

	w.notify()

	return id, nil
}

func (w *Workflow) ReplaceSecret(
	ctx context.Context,
	id, key string,
	secret *credential.Secret,
) (resultErr error) {
	ctx, finishLog := logging.Start(ctx, w.logger, "connection_access_repair")
	defer func() { finishLog(resultErr) }()

	if err := w.host.ReplaceSecret(ctx, id, key, secret); err != nil {
		return err
	}

	return w.Retry(ctx, id)
}

func (w *Workflow) UpdateConnection(
	ctx context.Context,
	id, name string,
	settings provider.Settings,
) (resultErr error) {
	ctx, finishLog := logging.Start(ctx, w.logger, "connection_update")
	defer func() { finishLog(resultErr) }()

	if err := w.host.UpdateSettings(ctx, id, name, settings); err != nil {
		return err
	}

	return w.Retry(ctx, id)
}

func (w *Workflow) Retry(ctx context.Context, id string) (resultErr error) {
	ctx, finishLog := logging.Start(ctx, w.logger, "acquisition_retry")
	defer func() { finishLog(resultErr) }()

	connection, err := w.host.Connection(ctx, id)
	if err != nil {
		return err
	}

	registration, ok := w.Provider(connection.Provider)
	if !ok {
		return provider.ErrUnavailable
	}

	view, err := w.store.Dashboard(ctx)
	if err != nil {
		return err
	}

	for _, stream := range registration.Descriptor().Streams() {
		operation := "discover"
		if view.Account.Connection() == id && view.Account.Stream() == stream.ID() &&
			registration.Acquirer() != nil {
			operation = "acquire"
		} else if registration.Discoverer() == nil {
			continue
		}

		if err = w.host.Retry(
			ctx,
			host.WorkKey{Connection: id, Stream: stream.ID(), Operation: operation},
		); err != nil {
			return err
		}
	}

	w.notify()

	return nil
}

func (w *Workflow) Confirm(
	ctx context.Context,
	id, artifact string,
	ref source.Reference,
	details AccountDetails,
) (result Account, resultErr error) {
	ctx, finishLog := logging.Start(ctx, w.logger, "account_confirm")
	defer func() { finishLog(resultErr) }()

	connection, err := w.host.Connection(ctx, id)
	if err != nil {
		return Account{}, err
	}

	registration, ok := w.Provider(connection.Provider)
	if !ok {
		return Account{}, provider.ErrUnavailable
	}

	a, err := w.store.Confirm(ctx, id, artifact, ref, details, registration.Acquirer() != nil)
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

func (w *Workflow) Dashboard(ctx context.Context) (Dashboard, error) {
	view, err := w.store.Dashboard(ctx)
	if err != nil {
		return view, err
	}

	for index := range view.Connections {
		connection := &view.Connections[index]

		registration, ok := w.Provider(connection.Provider)
		if !ok {
			continue
		}

		connection.Descriptor = registration.Descriptor()

		connection.CanAcquire = registration.Acquirer() != nil

		connection.CanDiscover = registration.Discoverer() != nil
		connection.CanNormalize = registration.Normalizer() != nil
		connection.CredentialStored = true
		for _, field := range connection.Descriptor.Fields() {
			if field.Kind() == provider.SecretField && field.Required() &&
				!slices.Contains(connection.CredentialKeys, field.Key()) {
				connection.CredentialStored = false
			}
		}

		connection.MissingAccess = !connection.SettingsStored || !connection.CredentialStored
	}

	return view, nil
}

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

	providers := []string{}
	for _, registration := range w.Providers() {
		if registration.Normalizer() != nil {
			providers = append(providers, registration.Descriptor().ID())
		}
	}

	artifacts, err := w.store.PendingArtifacts(ctx, now, providers)
	if err != nil {
		return err
	}

	for _, artifact := range artifacts {
		id := artifact.ID
		work := logging.Correlate(ctx)
		registration, ok := w.Provider(artifact.Provider)
		switch {
		case !ok:
			err = provider.ErrUnavailable
		case registration.Normalizer() == nil:
			continue
		default:
			if _, err = w.host.Normalize(work, id, registration.Version()); err == nil {
				processContext, finish := logging.Start(work, w.logger, "process", "artifact_id", id)
				err = w.store.Process(processContext, id, registration.Version())
				finish(err)
			}
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

	view, err := w.Dashboard(ctx)
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
			registration, ok := w.Provider(connection.Provider)
			if !ok || registration.Discoverer() == nil {
				continue
			}

			for _, stream := range registration.Descriptor().Streams() {
				if err = w.store.Schedule(
					ctx,
					host.WorkKey{Connection: connection.ID, Stream: stream.ID(), Operation: "discover"},
					true,
				); err != nil {
					return err
				}
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
	AcquisitionReceipt struct {
		ID, Operation, Retrieved, Hash, Policy, RequestedFrom, RequestedTo, ReportedFrom, ReportedTo string
		Equivalent                                                                                   bool
	}
	EvidenceObservation struct{ ActivityID, Location, Outcome, Rule, Version string }
	EvidenceContext     struct {
		ConnectionName, Operation, RequestedFrom, RequestedTo, ReportedFrom, ReportedTo string
		Observations                                                                    []EvidenceObservation
		Receipts                                                                        []AcquisitionReceipt
		ReceiptCount                                                                    int64
	}
)

// CheckCorrection checks an edit without retaining a decision or scheduling work.
func (w *Workflow) CheckCorrection(ctx context.Context, d Decision) error {
	return w.store.CheckCorrection(ctx, d)
}
