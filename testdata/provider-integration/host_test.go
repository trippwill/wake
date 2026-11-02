// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package providerproof_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	fixture "example.org/wakefixture"
	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/credential"
	"go.trippwill.dev/wake/internal/host"
	persistence "go.trippwill.dev/wake/internal/persistence/sqlite"
	"go.trippwill.dev/wake/provider"
	"go.trippwill.dev/wake/source"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fixtureHost struct {
	diagnostics bytes.Buffer
	path, id    string
	store       *persistence.Store
	host        *host.Host
	envelope    *credential.Envelope
	now         time.Time
}

func newFixture(t *testing.T, r provider.Registration) *fixtureHost {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(dir, "master.key")
	if err := credential.CreateMasterKey(keyPath); err != nil {
		t.Fatal(err)
	}
	key, err := credential.LoadMasterKey(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	defer key.Clear()
	envelope, err := credential.NewEnvelope(key)
	if err != nil {
		t.Fatal(err)
	}
	f := &fixtureHost{path: filepath.Join(dir, "wake.db"), envelope: envelope, now: time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)}
	f.open(t, r)
	token := credential.NewSecret([]byte("synthetic-token"))
	defer token.Clear()
	settings, _ := provider.NewSettings(map[string]string{"tenant": "synthetic-tenant"})
	f.id, err = f.host.Configure(context.Background(), "fixture", "Synthetic source", settings, map[string]*credential.Secret{"api_secret": token})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		f.host.Close()
		if err := f.store.Close(); err != nil {
			t.Error(err)
		}
		envelope.Clear()
	})
	return f
}
func registration(t *testing.T) provider.Registration {
	t.Helper()
	r, err := fixture.Register()
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func (f *fixtureHost) open(t *testing.T, r provider.Registration) {
	t.Helper()
	var err error
	f.store, err = persistence.Open(context.Background(), persistence.Profile{WarehousePath: f.path, ConnectionsPath: filepath.Join(filepath.Dir(f.path), "connections.db")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	f.host, err = host.New(context.Background(), f.store, f.envelope, slog.New(slog.NewJSONHandler(&f.diagnostics, &slog.HandlerOptions{Level: slog.LevelDebug})), func() time.Time { return f.now }, r)
	if err != nil {
		t.Fatal(err)
	}
}
func (f *fixtureHost) sql(t *testing.T, text string, args ...any) {
	t.Helper()
	db, err := sql.Open("sqlite", f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err = db.Exec("ATTACH DATABASE ? AS operational", filepath.Join(filepath.Dir(f.path), "operational.db")); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(text, args...); err != nil {
		t.Fatal(err)
	}
}
func (f *fixtureHost) count(t *testing.T, table string) int {
	t.Helper()
	db, err := sql.Open("sqlite", f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var n int
	if err = db.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
func (f *fixtureHost) checkpoint(t *testing.T, operation string) host.Checkpoint {
	t.Helper()
	cp, err := f.store.Checkpoint(context.Background(), host.WorkKey{Connection: f.id, Stream: "statements", Operation: operation})
	if err != nil {
		t.Fatal(err)
	}
	return cp
}
func TestExternalProviderRealHostStoreAndRestart(t *testing.T) {
	ctx := context.Background()
	r := registration(t)
	f := newFixture(t, r)
	discovery, err := f.host.Discover(ctx, f.id, "statements")
	if err != nil || discovery == "" {
		t.Fatal(discovery, err)
	}
	candidates, err := f.store.Candidates(ctx, f.id)
	if err != nil || len(candidates) != 1 || candidates[0].Reference().Authority() != "fixture-broker" {
		t.Fatal(candidates, err)
	}
	cp := f.checkpoint(t, "discover")
	if cp.Revision != 1 || cp.State.Version() != "v1" || cp.Acknowledged.Completed() != 1 {
		t.Fatal("discovery acknowledgement", cp)
	}
	// Discovery never creates financial accounts or recurring synchronization authority.
	db, err := sql.Open("sqlite", f.path)
	if err != nil {
		t.Fatal(err)
	}
	var n int
	err = db.QueryRow("SELECT (SELECT count(*) FROM accounts) + (SELECT count(*) FROM scheduled WHERE enabled=1)").Scan(&n)
	_ = db.Close()
	if err != nil || n != 0 {
		t.Fatal("discovery activated account", n, err)
	}
	if id, e := f.host.Acquire(ctx, f.id, "statements"); e != nil || id != "" {
		t.Fatal("shared pacing must defer immediate acquisition", id, e)
	}
	f.now = f.now.Add(2 * time.Second)
	artifact, err := f.host.Acquire(ctx, f.id, "statements")
	if err != nil || artifact == "" {
		t.Fatal(artifact, err)
	}
	result, err := f.host.Normalize(ctx, artifact, "v1")
	if err != nil || result.Trades() != 1 || result.Rejected() != 1 {
		t.Fatal(result, err)
	}
	records, err := f.store.Records(ctx, artifact, "v1")
	if err != nil || len(records) != 3 {
		t.Fatal(len(records), err)
	}
	for _, record := range records {
		if record.Kind != "trade" {
			continue
		}
		terms := record.Trade.Fields()
		if terms.Quantity.Value() != "1.0000000000000000000000000001" || terms.Price.Amount() != "123.4500000000000000000000000001" || terms.ExecutedAt.String() != "2026-10-23T12:00:00.1200Z" || terms.Event.Scope() != "account-1" {
			t.Fatal("normalized exact/source terms changed")
		}
	}
	if _, err = f.host.Normalize(ctx, artifact, "v1"); err != nil {
		t.Fatal(err)
	}
	attempts, err := f.store.Attempts(ctx, artifact)
	if err != nil || len(attempts) != 1 {
		t.Fatal("replay repeated processing", attempts, err)
	}
	retained, err := f.store.Retained(ctx, artifact)
	if err != nil || string(retained.Artifact.Content()) != "synthetic equity execution\n" {
		t.Fatal("original evidence lost", err)
	}
	f.host.Close()
	if err = f.store.Close(); err != nil {
		t.Fatal(err)
	}
	var resumed provider.State
	wrapped := acquireFunc(func(ctx context.Context, req provider.Request, a provider.Access) (provider.Step, error) {
		resumed = req.State
		return fixture.Provider{}.Acquire(ctx, req, a)
	})
	r, err = provider.NewRegistration(r.Descriptor(), "v1", wrapped, fixture.Provider{}, fixture.Provider{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	f.open(t, r)
	scope, _ := provider.NewPacingScope([]byte("fixture-endpoint"))
	reservation, err := f.store.Reserve(ctx, "fixture", "requests", scope.Digest(), f.now, time.Second)
	if err != nil || reservation.Granted() {
		t.Fatal("restart reset pacing", err)
	}
	f.now = f.now.Add(2 * time.Hour)
	if _, err = f.host.Acquire(ctx, f.id, "statements"); err != nil {
		t.Fatal(err)
	}
	if resumed.Version() != "v1" || string(resumed.Payload()) != `{"page":2}` {
		t.Fatal("continuation not serialized across restart")
	}
	records, err = f.store.Records(ctx, artifact, "v1")
	if err != nil || len(records) != 3 {
		t.Fatal("restart lost records", err)
	}
	f.host.Close()
	h, err := host.New(ctx, f.store, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	if _, err = h.Normalize(ctx, artifact, "v1"); !errors.Is(err, provider.ErrUnavailable) {
		t.Fatal("missing normalizer substituted", err)
	}
	content, err := os.ReadFile(f.path)
	if err != nil || bytes.Contains(content, []byte("synthetic-token")) {
		t.Fatal("plaintext credential retained", err)
	}
}
func TestAtomicEvidenceAcknowledgementAndCandidateRollback(t *testing.T) {
	for _, operation := range []string{"acquire", "discover"} {
		t.Run(operation, func(t *testing.T) {
			f := newFixture(t, registration(t))
			table := "artifacts"
			if operation == "discover" {
				table = "candidates"
			}
			f.sql(t, "CREATE TRIGGER fail_accept BEFORE INSERT ON "+table+" BEGIN SELECT RAISE(ABORT,'synthetic-storage-failure'); END")
			run := f.host.Acquire
			if operation == "discover" {
				run = f.host.Discover
			}
			if _, err := run(context.Background(), f.id, "statements"); err == nil {
				t.Fatal("failed transaction acknowledged")
			}
			cp := f.checkpoint(t, operation)
			if cp.Revision != 0 || !cp.State.IsZero() || cp.Acknowledged.Phase() != "" || cp.Observed.Phase() == "" || f.count(t, "artifacts") != 0 || f.count(t, "candidates") != 0 {
				t.Fatal("partial acknowledgement", cp)
			}
			f.sql(t, "DROP TRIGGER fail_accept")
			f.now = f.now.Add(2 * time.Second)
			id, err := run(context.Background(), f.id, "statements")
			if err != nil || id == "" {
				t.Fatal(id, err)
			}
			step, _ := provider.NewStep(provider.State{}, provider.Artifact{}, f.now.Add(time.Hour))
			if _, err = f.store.Accept(context.Background(), host.WorkKey{Connection: f.id, Stream: "statements", Operation: operation}, 0, step, nil, provider.Progress{}); !errors.Is(err, provider.ErrConflict) {
				t.Fatal("stale revision accepted", err)
			}
			if f.count(t, "artifacts") != 1 || f.checkpoint(t, operation).Revision != 1 {
				t.Fatal("replay duplicated acknowledged work")
			}
		})
	}
}

type acquireFunc func(context.Context, provider.Request, provider.Access) (provider.Step, error)

func (f acquireFunc) Acquire(ctx context.Context, r provider.Request, a provider.Access) (provider.Step, error) {
	return f(ctx, r, a)
}

type normalizeFunc func(context.Context, provider.Artifact, provider.Claims) (provider.Result, error)

func (f normalizeFunc) Normalize(ctx context.Context, a provider.Artifact, c provider.Claims) (provider.Result, error) {
	return f(ctx, a, c)
}

type swallowingSink struct{ provider.Claims }

func (s swallowingSink) PutTrade(ctx context.Context, t source.Trade) error {
	_ = s.Claims.PutTrade(ctx, t)
	_ = s.Claims.PutTrade(ctx, t)
	return nil
}
func (s swallowingSink) Reject(ctx context.Context, location, reason string) error {
	_ = s.Claims.Reject(ctx, location, reason)
	return nil
}
func TestNormalizationRollbackRejectAccountingAndStorageFailure(t *testing.T) {
	for _, kind := range []string{"provider-failure", "storage-failure", "accounting-failure", "unavailable"} {
		t.Run(kind, func(t *testing.T) {
			base := registration(t)
			normalizer := normalizeFunc(func(ctx context.Context, a provider.Artifact, c provider.Claims) (provider.Result, error) {
				sink := c
				if kind == "storage-failure" {
					sink = swallowingSink{c}
				}
				result, err := fixture.Provider{}.Normalize(ctx, a, sink)
				if err != nil {
					return result, err
				}
				if kind == "unavailable" {
					return provider.Result{}, provider.ErrUnavailable
				}
				if kind == "provider-failure" {
					return provider.Result{}, errors.New("secret-bearing untrusted provider detail")
				}
				if kind == "accounting-failure" {
					return provider.NewResult(provider.Counts{Rows: 1, Rejected: 1})
				}
				return result, nil
			})
			r, err := provider.NewRegistration(base.Descriptor(), "broken-v1", fixture.Provider{}, normalizer, fixture.Provider{}, nil)
			if err != nil {
				t.Fatal(err)
			}
			f := newFixture(t, r)
			id, err := f.host.Acquire(context.Background(), f.id, "statements")
			if err != nil {
				t.Fatal(err)
			}
			_, err = f.host.Normalize(context.Background(), id, "broken-v1")
			if err == nil {
				t.Fatal("normalization unexpectedly committed")
			}
			if strings.Contains(f.diagnostics.String(), "secret-bearing") || strings.Contains(f.diagnostics.String(), "synthetic-token") {
				t.Fatal("private provider material leaked")
			}
			if !strings.Contains(f.diagnostics.String(), `"event":"normalize_finished","outcome":"failed"`) {
				t.Fatal("normalization failure missing", f.diagnostics.String())
			}
			if kind == "unavailable" && !errors.Is(err, provider.ErrUnavailable) {
				t.Fatal("unavailable interpreter became malformed input", err)
			}
			if kind == "storage-failure" && !errors.Is(err, host.ErrStorage) {
				t.Fatal("storage failure became row rejection", err)
			}
			records, e := f.store.Records(context.Background(), id, "broken-v1")
			if e != nil || len(records) != 0 {
				t.Fatal("partial normalization writes", records, e)
			}
			attempts, e := f.store.Attempts(context.Background(), id)
			if e != nil || len(attempts) != 1 || attempts[0].Outcome == "succeeded" {
				t.Fatal("failure not retained", attempts, e)
			}
			retained, e := f.store.Retained(context.Background(), id)
			if e != nil || retained.Artifact.IsZero() {
				t.Fatal("normalization failure lost evidence", e)
			}
		})
	}
}
func TestCancellationCredentialsAndJoinedHostShutdown(t *testing.T) {
	base := registration(t)
	entered := make(chan struct{})
	canceled := make(chan struct{})
	release := make(chan struct{})
	var retained []byte
	var saved provider.Access
	blocking := acquireFunc(func(ctx context.Context, r provider.Request, a provider.Access) (provider.Step, error) {
		saved = a
		err := a.WithSecret(ctx, "api_secret", func(secret []byte) error {
			retained = secret
			close(entered)
			<-ctx.Done()
			close(canceled)
			<-release
			return ctx.Err()
		})
		return provider.Step{}, err
	})
	r, err := provider.NewRegistration(base.Descriptor(), "v1", blocking, fixture.Provider{}, fixture.Provider{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	f := newFixture(t, r)
	done := make(chan error, 1)
	go func() { _, e := f.host.Acquire(context.Background(), f.id, "statements"); done <- e }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("credential callback did not start")
	}
	if _, err = f.host.Acquire(context.Background(), f.id, "statements"); !errors.Is(err, host.ErrBusy) {
		t.Fatal("concurrent connection admitted", err)
	}
	stopped := make(chan struct{})
	go func() { f.host.Close(); close(stopped) }()
	select {
	case <-canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("host did not cancel")
	}
	select {
	case <-stopped:
		t.Fatal("shutdown released live credentials")
	default:
	}
	close(release)
	select {
	case err = <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("provider not joined")
	}
	<-stopped
	if !bytes.Equal(retained, make([]byte, len(retained))) {
		t.Fatal("credential callback bytes not cleared")
	}
	if err = saved.WithSecret(context.Background(), "api_secret", func([]byte) error { return nil }); !errors.Is(err, provider.ErrUnauthorized) {
		t.Fatal("expired access remained usable", err)
	}
	if cp := f.checkpoint(t, "acquire"); cp.Failure != context.Canceled.Error() || cp.Revision != 0 {
		t.Fatal("cancellation acknowledged work", cp)
	}
	if _, err = f.host.Acquire(context.Background(), f.id, "statements"); !errors.Is(err, host.ErrStopped) {
		t.Fatal("shutdown accepted new work", err)
	}
}

func TestUnknownContinuationAndDurableCooldown(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, registration(t))
	key := host.WorkKey{Connection: f.id, Stream: "statements", Operation: "acquire"}
	cp := f.checkpoint(t, "acquire")
	state, _ := provider.NewState("future-v99", []byte(`{"page":42}`))
	step, _ := provider.NewStep(state, provider.Artifact{}, f.now)
	if _, err := f.store.Accept(ctx, key, cp.Revision, step, nil, provider.Progress{}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.host.Acquire(ctx, f.id, "statements"); !errors.Is(err, provider.ErrUnavailable) {
		t.Fatal("unknown continuation silently reset", err)
	}
	if cp = f.checkpoint(t, "acquire"); cp.Revision != 1 || cp.State.Version() != "future-v99" {
		t.Fatal("unknown continuation overwritten")
	}
	scope, _ := provider.NewPacingScope([]byte("shared-service"))
	until := f.now.Add(time.Hour)
	if err := f.store.Delay(ctx, "fixture", "requests", scope.Digest(), until); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Delay(ctx, "fixture", "requests", scope.Digest(), f.now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	f.host.Close()
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	f.open(t, registration(t))
	reservation, err := f.store.Reserve(ctx, "fixture", "requests", scope.Digest(), f.now, time.Second)
	if err != nil || reservation.Granted() || !reservation.NextEligibleAt().Equal(until) {
		t.Fatal("cooldown shortened or reset", reservation, err)
	}
}
func TestNormalizationCancellationJoinsBeforeRollback(t *testing.T) {
	base := registration(t)
	entered := make(chan struct{})
	normalizer := normalizeFunc(func(ctx context.Context, a provider.Artifact, c provider.Claims) (provider.Result, error) {
		result, err := fixture.Provider{}.Normalize(ctx, a, c)
		if err != nil {
			return result, err
		}
		close(entered)
		<-ctx.Done()
		return provider.Result{}, ctx.Err()
	})
	r, err := provider.NewRegistration(base.Descriptor(), "cancel-v1", fixture.Provider{}, normalizer, fixture.Provider{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	f := newFixture(t, r)
	artifact, err := f.host.Acquire(context.Background(), f.id, "statements")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, e := f.host.Normalize(context.Background(), artifact, "cancel-v1"); done <- e }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("normalization did not start")
	}
	f.host.Close()
	if err = <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	records, err := f.store.Records(context.Background(), artifact, "cancel-v1")
	if err != nil || len(records) != 0 {
		t.Fatal("canceled normalization committed rows", err)
	}
	attempts, err := f.store.Attempts(context.Background(), artifact)
	if err != nil || len(attempts) != 1 || attempts[0].Outcome != "canceled" {
		t.Fatal(attempts, err)
	}
}
func TestInterruptedAttemptRecoveryAndCredentialFreeNormalization(t *testing.T) {
	f := newFixture(t, registration(t))
	artifact, err := f.host.Acquire(context.Background(), f.id, "statements")
	if err != nil {
		t.Fatal(err)
	}
	f.sql(t, "INSERT INTO attempts(id,artifact_id,version,outcome,counts) VALUES ('interrupted-proof',?,'old-v1','running',X'')", artifact)
	f.host.Close()
	if err = f.store.Close(); err != nil {
		t.Fatal(err)
	}
	f.open(t, registration(t))
	attempts, err := f.store.Attempts(context.Background(), artifact)
	if err != nil || len(attempts) != 1 || attempts[0].Outcome != "interrupted" {
		t.Fatal("interrupted attempt not recovered", attempts, err)
	}
	f.host.Close()
	f.host, err = host.New(context.Background(), f.store, nil, nil, nil, registration(t))
	if err != nil {
		t.Fatal(err)
	}
	result, err := f.host.Normalize(context.Background(), artifact, "v1")
	if err != nil || result.Trades() != 1 {
		t.Fatal("normalization depended on credentials", result, err)
	}
}

func TestIgnoredRowsKeepEvidenceWithoutFinancialContributions(t *testing.T) {
	base := registration(t)
	n := normalizeFunc(func(context.Context, provider.Artifact, provider.Claims) (provider.Result, error) {
		return provider.NewResult(provider.Counts{Rows: 1, Ignored: 1})
	})
	r, err := provider.NewRegistration(base.Descriptor(), "ignored-v1", fixture.Provider{}, n, fixture.Provider{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	f := newFixture(t, r)
	id, err := f.host.Acquire(context.Background(), f.id, "statements")
	if err != nil {
		t.Fatal(err)
	}
	result, err := f.host.Normalize(context.Background(), id, "ignored-v1")
	if err != nil || result.Ignored() != 1 {
		t.Fatal(result, err)
	}
	records, err := f.store.Records(context.Background(), id, "ignored-v1")
	if err != nil || len(records) != 0 {
		t.Fatal(records, err)
	}
}

func TestPacingStorageFailureCannotBeAcknowledged(t *testing.T) {
	base := registration(t)
	acquire := acquireFunc(func(ctx context.Context, r provider.Request, a provider.Access) (provider.Step, error) {
		scope, _ := provider.NewPacingScope([]byte("fixture-endpoint"))
		_, _ = a.ReservePacing(ctx, "requests", scope)
		return provider.NewStep(r.State, provider.Artifact{}, r.Now.Add(time.Hour))
	})
	r, err := provider.NewRegistration(base.Descriptor(), "v1", acquire, fixture.Provider{}, fixture.Provider{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	f := newFixture(t, r)
	f.sql(t, "CREATE TRIGGER fail_pacing BEFORE INSERT ON pacing BEGIN SELECT RAISE(ABORT,'synthetic pacing failure'); END")
	if _, err = f.host.Acquire(context.Background(), f.id, "statements"); !errors.Is(err, host.ErrStorage) {
		t.Fatal("pacing failure acknowledged", err)
	}
	if cp := f.checkpoint(t, "acquire"); cp.Revision != 0 || cp.Failure != host.ErrStorage.Error() {
		t.Fatal("pacing failure advanced continuation", cp)
	}
}

func TestAdjustedOptionExternalClaimRoundTrip(t *testing.T) {
	ctx := context.Background()
	base := registration(t)
	n := normalizeFunc(func(ctx context.Context, _ provider.Artifact, c provider.Claims) (provider.Result, error) {
		trade, err := fixture.Option()
		if err != nil {
			return provider.Result{}, err
		}
		if err = c.PutTrade(ctx, trade); err != nil {
			return provider.Result{}, err
		}
		return provider.NewResult(provider.Counts{Rows: 1, Trades: 1})
	})
	r, err := provider.NewRegistration(base.Descriptor(), "v1", fixture.Provider{}, n, fixture.Provider{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	f := newFixture(t, r)
	id, err := f.host.Acquire(ctx, f.id, "statements")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.host.Normalize(ctx, id, "v1"); err != nil {
		t.Fatal(err)
	}
	records, err := f.store.Records(ctx, id, "v1")
	if err != nil || len(records) != 1 {
		t.Fatal(records, err)
	}
	terms := records[0].Trade.Fields().Instrument.Option().Fields()
	if terms.Strike.Amount() != "123.4500000000000000001" || terms.Multiplier.Value() != "10" || len(terms.Deliverables) != 2 || terms.Deliverables[0].Quantity().Value() != "12.5" || terms.Deliverables[1].Cash().Amount() != "0.75" || terms.EffectiveFrom.String() != "2026-10-01" {
		t.Fatal("adjusted terms lost", terms)
	}
}
func TestLifecycleExternalClaimRoundTrip(t *testing.T) {
	ctx := context.Background()
	base := registration(t)
	n := normalizeFunc(func(ctx context.Context, _ provider.Artifact, c provider.Claims) (provider.Result, error) {
		trade, err := fixture.Lifecycle()
		if err != nil {
			return provider.Result{}, err
		}
		if err = c.PutLifecycle(ctx, trade); err != nil {
			return provider.Result{}, err
		}
		return provider.NewResult(provider.Counts{Rows: 1, Lifecycles: 1})
	})
	r, err := provider.NewRegistration(base.Descriptor(), "v1", fixture.Provider{}, n, fixture.Provider{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	f := newFixture(t, r)
	id, err := f.host.Acquire(ctx, f.id, "statements")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.host.Normalize(ctx, id, "v1"); err != nil {
		t.Fatal(err)
	}
	records, err := f.store.Records(ctx, id, "v1")
	if err != nil || len(records) != 1 {
		t.Fatal(records, err)
	}
	if records[0].Lifecycle.Fields().Kind != source.Exercise || len(records[0].Lifecycle.Fields().Relationships) != 1 {
		t.Fatal("lifecycle terms lost")
	}
}
func TestDebtExternalClaimRoundTrip(t *testing.T) {
	for _, kind := range []source.DebtType{source.TreasuryNote, source.TreasuryBond, source.ConventionalGilt} {
		t.Run(string(kind), func(t *testing.T) {
			ctx := context.Background()
			base := registration(t)
			n := normalizeFunc(func(ctx context.Context, _ provider.Artifact, c provider.Claims) (provider.Result, error) {
				trade, err := fixture.Debt(kind)
				if err != nil {
					return provider.Result{}, err
				}
				if err = c.PutTrade(ctx, trade); err != nil {
					return provider.Result{}, err
				}
				return provider.NewResult(provider.Counts{Rows: 1, Trades: 1})
			})
			r, err := provider.NewRegistration(base.Descriptor(), "v1", fixture.Provider{}, n, fixture.Provider{}, nil)
			if err != nil {
				t.Fatal(err)
			}
			f := newFixture(t, r)
			id, err := f.host.Acquire(ctx, f.id, "statements")
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.host.Normalize(ctx, id, "v1"); err != nil {
				t.Fatal(err)
			}
			records, err := f.store.Records(ctx, id, "v1")
			if err != nil || len(records) != 1 {
				t.Fatal(records, err)
			}
			terms := records[0].Trade.Fields()
			if terms.Instrument.Debt().Fields().Coupon.Value() != "4.125000000000001" || records[0].Trade.PriceBasis() != "" || terms.AccruedInterest.Amount() != "-12.345" {
				t.Fatal("debt terms lost", terms)
			}
		})
	}
}
func TestConversionExternalClaimRoundTrip(t *testing.T) {
	ctx := context.Background()
	base := registration(t)
	n := normalizeFunc(func(ctx context.Context, _ provider.Artifact, c provider.Claims) (provider.Result, error) {
		trade, err := fixture.Conversion()
		if err != nil {
			return provider.Result{}, err
		}
		if err = c.PutConversion(ctx, trade); err != nil {
			return provider.Result{}, err
		}
		return provider.NewResult(provider.Counts{Rows: 1, Conversions: 1})
	})
	r, err := provider.NewRegistration(base.Descriptor(), "v1", fixture.Provider{}, n, fixture.Provider{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	f := newFixture(t, r)
	id, err := f.host.Acquire(ctx, f.id, "statements")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.host.Normalize(ctx, id, "v1"); err != nil {
		t.Fatal(err)
	}
	records, err := f.store.Records(ctx, id, "v1")
	if err != nil || len(records) != 1 {
		t.Fatal(records, err)
	}
	terms := records[0].Conversion.Fields()
	if terms.Sent.Amount() != "10.1234567890000000001" || terms.Received.Amount() != "" || terms.Charges[0].Money().Currency().String() != "GBP" {
		t.Fatal("conversion roles lost", terms)
	}
}
func TestCorrectionExternalClaimRoundTrip(t *testing.T) {
	ctx := context.Background()
	base := registration(t)
	n := normalizeFunc(func(ctx context.Context, _ provider.Artifact, c provider.Claims) (provider.Result, error) {
		trade, err := fixture.Correction()
		if err != nil {
			return provider.Result{}, err
		}
		if err = c.PutCorrection(ctx, trade); err != nil {
			return provider.Result{}, err
		}
		return provider.NewResult(provider.Counts{Rows: 1, Corrections: 1})
	})
	r, err := provider.NewRegistration(base.Descriptor(), "v1", fixture.Provider{}, n, fixture.Provider{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	f := newFixture(t, r)
	id, err := f.host.Acquire(ctx, f.id, "statements")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.host.Normalize(ctx, id, "v1"); err != nil {
		t.Fatal(err)
	}
	records, err := f.store.Records(ctx, id, "v1")
	if err != nil || len(records) != 1 {
		t.Fatal(records, err)
	}
	terms := records[0].Correction.Fields()
	if terms.Kind != source.Replacement || terms.Target.Value() != "original" || terms.Replacement.Value() != "option-1" {
		t.Fatal("correction references lost", terms)
	}
}

func TestNormalizationReceiptSurvivesAccessLossWithoutCredentials(t *testing.T) {
	ctx := context.Background()
	r := registration(t)
	f := newFixture(t, r)
	artifact, err := f.host.Acquire(ctx, f.id, "statements")
	if err != nil || artifact == "" {
		t.Fatal(artifact, err)
	}
	result, err := f.host.Normalize(ctx, artifact, "v1")
	if err != nil {
		t.Fatal(err)
	}
	f.host.Close()
	if err = f.store.Close(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"connections.db"} {
		if err = os.Remove(filepath.Join(filepath.Dir(f.path), name)); err != nil {
			t.Fatal(err)
		}
	}
	f.open(t, r)
	f.host.Close()
	f.host, err = host.New(ctx, f.store, nil, nil, nil, r)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := f.host.Normalize(ctx, artifact, "v1")
	if err != nil || replay.Counts() != result.Counts() {
		t.Fatal("lost successful receipt", replay, err)
	}
	if attempts, err := f.store.Attempts(ctx, artifact); err != nil || len(attempts) != 1 || attempts[0].Outcome != "succeeded" {
		t.Fatal("replay reran normalization", attempts, err)
	}
	if records, err := f.store.Records(ctx, artifact, "v1"); err != nil || len(records) != 3 {
		t.Fatal("lost retained claims", records, err)
	}
	if _, err = f.host.Acquire(ctx, f.id, "statements"); !errors.Is(err, provider.ErrUnavailable) {
		t.Fatal("recovery restored source authority", err)
	}
}

func TestNormalizationReceiptFailureRollsBackClaims(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, registration(t))
	artifact, err := f.host.Acquire(ctx, f.id, "statements")
	if err != nil {
		t.Fatal(err)
	}
	f.sql(t, `CREATE TRIGGER reject_receipt BEFORE INSERT ON normalization_receipts BEGIN SELECT RAISE(ABORT,'injected receipt failure'); END`)
	if _, err = f.host.Normalize(ctx, artifact, "v1"); !errors.Is(err, host.ErrStorage) {
		t.Fatal("receipt failure acknowledged", err)
	}
	if records, err := f.store.Records(ctx, artifact, "v1"); err != nil || len(records) != 0 {
		t.Fatal("claims escaped rollback", records, err)
	}
	if attempts, err := f.store.Attempts(ctx, artifact); err != nil || len(attempts) != 1 || attempts[0].Outcome != "storage-failed" {
		t.Fatal("missing failed attempt", attempts, err)
	}
	f.sql(t, `DROP TRIGGER reject_receipt`)
	if _, err = f.host.Normalize(ctx, artifact, "v1"); err != nil {
		t.Fatal(err)
	}
}

func TestCommittedRetryIsNotReportedAsAcquisitionSuccess(t *testing.T) {
	base := registration(t)
	acquire := acquireFunc(func(_ context.Context, request provider.Request, _ provider.Access) (provider.Step, error) {
		return provider.NewRetryStep(provider.State{}, request.Now.Add(time.Minute), provider.RetryUpstream, "private-provider-value")
	})
	r, err := provider.NewRegistration(base.Descriptor(), base.Version(), acquire, base.Normalizer(), base.Discoverer(), nil)
	if err != nil {
		t.Fatal(err)
	}
	f := newFixture(t, r)
	if _, err = f.host.Acquire(context.Background(), f.id, "statements"); err != nil {
		t.Fatal(err)
	}
	checkpoint, err := f.store.Checkpoint(context.Background(), host.WorkKey{Connection: f.id, Stream: "statements", Operation: "acquire"})
	if err != nil || checkpoint.Revision != 1 || checkpoint.Failure != "private-provider-value" {
		t.Fatal("retry was not retained", checkpoint, err)
	}
	text := f.diagnostics.String()
	if !strings.Contains(text, `"outcome":"retry_scheduled"`) || !strings.Contains(text, `"code":"upstream_retry"`) || strings.Contains(text, "private-provider-value") || !strings.Contains(text, `"next_eligible_at"`) {
		t.Fatal(text)
	}
}

func TestNormalizationOnlyProviderAdmitsSuppliedEvidenceWithoutCredentials(t *testing.T) {
	registration, err := fixture.RegisterDocument()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err = os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	f := &fixtureHost{path: filepath.Join(dir, "wake.db"), now: time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)}
	f.open(t, registration)
	t.Cleanup(func() {
		f.host.Close()
		if err := f.store.Close(); err != nil {
			t.Error(err)
		}
	})
	workflow := application.New(f.store, f.host, nil, nil, nil)
	id, err := workflow.CreateConnection(t.Context(), "Supplied document", registration.Descriptor().ID(), provider.Settings{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.host.CheckAccess(t.Context(), id); err != nil {
		t.Fatal("normalizer required credentials", err)
	}
	if count := f.count(t, "scheduled"); count != 0 {
		t.Fatal("normalizer scheduled upstream work", count)
	}
	view, err := inspectProvider(t, workflow)
	if err != nil || len(view.Connections) != 1 || view.Connections[0].MissingAccess || view.Connections[0].CanAcquire || view.Connections[0].CanDiscover {
		t.Fatal("normalizer-only capability/readiness mismatch", view, err)
	}
	artifact, err := provider.NewArtifact("synthetic-document-v1", "application/octet-stream", f.now, []byte("synthetic document bytes"))
	if err != nil {
		t.Fatal(err)
	}
	retainedID, err := f.host.Supply(t.Context(), id, "documents", artifact)
	if err != nil {
		t.Fatal(err)
	}
	retained, err := f.store.Retained(t.Context(), retainedID)
	if err != nil || !bytes.Equal(retained.Artifact.Content(), artifact.Content()) {
		t.Fatal("supplied original bytes lost", err)
	}
	for range 2 {
		result, err := f.host.Normalize(t.Context(), retainedID, registration.Version())
		if err != nil || result.Trades() != 1 {
			t.Fatal(result, err)
		}
	}
	records, err := f.store.Records(t.Context(), retainedID, registration.Version())
	if err != nil || len(records) != 1 || records[0].Trade.Fields().Location != "page:1/region:2/row:3" || records[0].Trade.Fields().Instrument.Option().Fields().Strike.Amount() != "123.4500000000000000001" {
		t.Fatal("document source claims lost precision/provenance", records, err)
	}
	attempts, err := f.store.Attempts(t.Context(), retainedID)
	if err != nil || len(attempts) != 1 {
		t.Fatal("successful normalization replay repeated work", attempts, err)
	}
	if _, err = f.host.Acquire(t.Context(), id, "documents"); !errors.Is(err, provider.ErrUnavailable) {
		t.Fatal("normalizer acquired upstream data", err)
	}
}

func TestDiscoveryOnlyConfirmationDoesNotScheduleMissingAcquisition(t *testing.T) {
	base := registration(t)
	r, err := provider.NewRegistration(base.Descriptor(), base.Version(), nil, nil, base.Discoverer(), nil)
	if err != nil {
		t.Fatal(err)
	}
	f := newFixture(t, r)
	workflow := application.New(f.store, f.host, nil, nil, nil)
	artifact, err := f.host.Discover(t.Context(), f.id, "statements")
	if err != nil {
		t.Fatal(err)
	}
	view, err := inspectProvider(t, workflow)
	if err != nil || len(view.Connections) != 1 || len(view.Connections[0].Candidates) != 1 {
		t.Fatal(view, err)
	}
	details, err := application.NewAccountDetails("Discovered account", "")
	if err != nil {
		t.Fatal(err)
	}
	candidate := view.Connections[0].Candidates[0]
	if _, err = workflow.Confirm(t.Context(), f.id, artifact, candidate.Candidate.Reference(), details); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var scheduled int
	if err = db.QueryRow("SELECT count(*) FROM scheduled WHERE operation='acquire' AND enabled=1").Scan(&scheduled); err != nil || scheduled != 0 {
		t.Fatal("discovery required an acquirer", scheduled, err)
	}
}

func TestAcquisitionWithoutNormalizerRetainsEvidenceWithoutProcessingRetries(t *testing.T) {
	base := registration(t)
	r, err := provider.NewRegistration(base.Descriptor(), base.Version(), base.Acquirer(), nil, base.Discoverer(), nil)
	if err != nil {
		t.Fatal(err)
	}
	f := newFixture(t, r)
	workflow := application.New(f.store, f.host, nil, nil, nil)
	artifact, err := f.host.Discover(t.Context(), f.id, "statements")
	if err != nil {
		t.Fatal(err)
	}
	view, err := inspectProvider(t, workflow)
	if err != nil {
		t.Fatal(err)
	}
	candidate := view.Connections[0].Candidates[0]
	details, _ := application.NewAccountDetails("Acquisition only", "")
	if _, err = workflow.Confirm(t.Context(), f.id, artifact, candidate.Candidate.Reference(), details); err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(2 * time.Second)
	for range 2 {
		if err = workflow.RunOnce(t.Context(), f.now); err != nil {
			t.Fatal(err)
		}
	}
	view, err = inspectProvider(t, workflow)
	if err != nil || len(view.Connections[0].Processing) != 1 {
		t.Fatal(view, err)
	}
	status := view.Connections[0].Processing[0]
	if status.Failure != "" || status.Complete || view.Connections[0].CanNormalize {
		t.Fatal("absent optional normalizer became processing failure", status)
	}
	attempts, err := f.store.Attempts(t.Context(), status.Artifact)
	if err != nil || len(attempts) != 0 {
		t.Fatal("raw acquisition enqueued normalization", attempts, err)
	}
	if retained, err := f.store.Retained(t.Context(), status.Artifact); err != nil || retained.Artifact.IsZero() {
		t.Fatal("raw evidence lost", err)
	}
}

type workflowReadProof struct {
	*persistence.Store
	retainedReads int
}

func (s *workflowReadProof) Retained(ctx context.Context, id string) (host.Retained, error) {
	s.retainedReads++
	return s.Store.Retained(ctx, id)
}

func TestPendingMetadataSelectsNormalizerWithoutReadingArtifactBodies(t *testing.T) {
	base := registration(t)
	f := newFixture(t, base)
	document, err := fixture.RegisterDocument()
	if err != nil {
		t.Fatal(err)
	}
	f.host.Close()
	f.host, err = host.New(t.Context(), f.store, f.envelope, nil, func() time.Time { return f.now }, base, document)
	if err != nil {
		t.Fatal(err)
	}
	reads := &workflowReadProof{Store: f.store}
	workflow := application.New(reads, f.host, nil, nil, nil)
	discovery, err := f.host.Discover(t.Context(), f.id, "statements")
	if err != nil {
		t.Fatal(err)
	}
	view, err := inspectProvider(t, workflow)
	if err != nil {
		t.Fatal(err)
	}
	candidate := view.Connections[0].Candidates[0]
	details, _ := application.NewAccountDetails("Metadata processing proof", "")
	if _, err = workflow.Confirm(t.Context(), f.id, discovery, candidate.Candidate.Reference(), details); err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(2 * time.Second)
	artifact, err := f.host.Acquire(t.Context(), f.id, "statements")
	if err != nil || artifact == "" {
		t.Fatal(artifact, err)
	}
	// A nonempty, elapsed retry bound also proves multi-provider SQL parameters
	// remain ordered correctly when the enabled normalizer list has two entries.
	if err = f.store.ProcessingFailure(t.Context(), artifact, f.now.Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if err = workflow.RunOnce(t.Context(), f.now); err != nil {
		t.Fatal(err)
	}
	view, err = inspectProvider(t, workflow)
	query, _ := application.NewListQuery(application.ListOptions{}, application.ActivityScope)
	activities, readErr := workflow.ActivityList(t.Context(), query)
	if err != nil || readErr != nil || activities.Total != 1 || reads.retainedReads != 0 {
		t.Fatal("orchestration decoded full evidence for metadata", view, reads.retainedReads, err)
	}
}

//nolint:gosec // Copy only encrypted synthetic fixtures into private disposable profiles.
func TestEncryptedAccessSeedCopiesHaveIndependentWarehouses(t *testing.T) {
	registration := registration(t)
	seed := newFixture(t, registration)
	seed.host.Close()
	if err := seed.store.Close(); err != nil {
		t.Fatal(err)
	}
	seedPath := filepath.Join(filepath.Dir(seed.path), "connections.db")
	original, err := os.ReadFile(seedPath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(original, []byte("synthetic-token")) {
		t.Fatal("access seed retained plaintext credential")
	}
	hosts := []*host.Host{}
	warehouseIDs := map[string]bool{}
	for range 2 {
		dir := t.TempDir()
		if err = os.Chmod(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		access := filepath.Join(dir, "connections.db")
		if err = os.WriteFile(access, original, 0o600); err != nil {
			t.Fatal(err)
		}
		profile := persistence.Profile{WarehousePath: filepath.Join(dir, "history.sqlite"), ConnectionsPath: access}
		store, err := persistence.Open(t.Context(), profile, nil)
		if err != nil {
			t.Fatal(err)
		}
		h, err := host.New(t.Context(), store, seed.envelope, nil, func() time.Time { return seed.now }, registration)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			h.Close()
			if err := store.Close(); err != nil {
				t.Error(err)
			}
		})
		workflow := application.New(store, h, nil, nil, nil)
		view, err := inspectProvider(t, workflow)
		if err != nil || !view.Storage.Paused || !view.Storage.WarehouseReset || len(view.Connections) != 1 || view.Connections[0].MissingAccess {
			t.Fatal("copied seed lost access or resumed without review", view, err)
		}
		if err = h.CheckAccess(t.Context(), seed.id); err != nil {
			t.Fatal("copied protected credentials unusable", err)
		}
		if err = workflow.ResumeStorage(t.Context(), view.Storage.Revision, "synthetic-test"); err != nil {
			t.Fatal(err)
		}
		db, err := sql.Open("sqlite", profile.WarehousePath)
		if err != nil {
			t.Fatal(err)
		}
		var identity string
		err = db.QueryRow("SELECT id FROM store_metadata").Scan(&identity)
		_ = db.Close()
		if err != nil || identity == "" || warehouseIDs[identity] {
			t.Fatal("copied seed reused warehouse identity", identity, err)
		}
		warehouseIDs[identity] = true
		hosts = append(hosts, h)
	}
	replacement := credential.NewSecret([]byte("synthetic-seed-replacement"))
	defer replacement.Clear()
	if err = hosts[0].ReplaceSecret(t.Context(), seed.id, "api_secret", replacement); err != nil {
		t.Fatal(err)
	}
	if _, err = hosts[0].Acquire(t.Context(), seed.id, "statements"); !errors.Is(err, provider.ErrUnauthorized) {
		t.Fatal("changed clone credential was not isolated", err)
	}
	if id, err := hosts[1].Acquire(t.Context(), seed.id, "statements"); err != nil || id == "" {
		t.Fatal("one clone changed another clone's credential/work", id, err)
	}
	after, err := os.ReadFile(seedPath)
	if err != nil || !bytes.Equal(original, after) {
		t.Fatal("disposable writers modified reusable encrypted seed", err)
	}
}

func inspectProvider(t *testing.T, w *application.Workflow) (application.SourcePage, error) {
	t.Helper()
	query, _ := application.NewListQuery(application.ListOptions{}, application.SourceScope)
	page, err := w.SourcePage(t.Context(), query)
	if err != nil {
		return page, err
	}
	for index, connection := range page.Connections {
		detail, e := w.SourceDetail(t.Context(), connection.ID, query)
		if e != nil {
			return page, e
		}
		page.Connections[index] = detail.Connection
	}
	return page, nil
}
