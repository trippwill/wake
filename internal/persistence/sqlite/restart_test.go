// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/credential"
	"go.trippwill.dev/wake/internal/host"
	"go.trippwill.dev/wake/internal/upstream"
	"go.trippwill.dev/wake/provider"
	"go.trippwill.dev/wake/providers/ibkrflex"
)

func TestReviewedRestartPacingAndSourceIsolation(t *testing.T) {
	ctx := t.Context()
	profile := seedUpgradeStores(t)
	envelope := testEnvelope(t)

	secret := credential.NewSecret([]byte("synthetic restart credential"))
	defer secret.Clear()

	encrypted, err := envelope.Encrypt(secret, credential.SourceSecretAAD("synthetic", "ibkr-flex", "token"))
	if err != nil {
		t.Fatal(err)
	}

	payload, err := encrypted.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}

	floor := time.Now().Add(2 * time.Hour).UTC()

	store, err := OpenInstalled(ctx, profile, nil)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := store.db.Exec(`UPDATE sync.configurations SET settings=CAST('{"query_id":"123"}' AS BLOB);
	INSERT INTO main.connections(id,provider,name) VALUES('unsupported','unknown-provider','Unsupported source');
	INSERT INTO sync.configurations(id,provider,settings) VALUES('unsupported','unknown-provider',X'7b7d');`); err != nil {
		t.Fatal(err)
	}

	if err := store.ReplaceSecret(ctx, "synthetic", "token", payload); err != nil {
		t.Fatal(err)
	}

	archive := filepath.Join(filepath.Dir(profile.WarehousePath), "prior.tar")

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	if err := Backup(ctx, profile, archive); err != nil {
		t.Fatal(err)
	}

	store, err = OpenInstalled(ctx, profile, nil)
	if err != nil {
		t.Fatal(err)
	}

	if err := store.Delay(ctx, "ibkr-flex", "web-service", "surviving-budget", floor); err != nil {
		t.Fatal(err)
	}

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	plan, err := RecoverSynchronization(ctx, profile, archive, "")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := RecoverSynchronization(ctx, profile, archive, plan.ID); err != nil {
		t.Fatal(err)
	}

	var (
		called               atomic.Int32
		wantedFrom, wantedTo atomic.Value
	)

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called.Add(1)

		query := r.URL.Query()
		if r.URL.Path != "/SendRequest" || query.Get("q") != "456" ||
			query.Get("fd") != wantedFrom.Load() || query.Get("td") != wantedTo.Load() {
			t.Error(
				"network step differed from reviewed scope",
				r.URL.Path,
				query.Get("q"),
				query.Get("fd"),
				query.Get("td"),
			)
		}

		_, _ = fmt.Fprint(
			w,
			"<FlexStatementResponse><Status>Success</Status><ReferenceCode>new-report</ReferenceCode></FlexStatementResponse>",
		)
	}))
	defer server.Close()

	registration, err := ibkrflex.Registration(
		ibkrflex.Options{HTTPClient: server.Client(), BaseURL: server.URL},
	)
	if err != nil {
		t.Fatal(err)
	}

	profile.Registrations = []provider.Registration{registration}

	store, err = OpenInstalled(ctx, profile, nil)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = store.Close() }()

	now := floor.Add(-time.Second)

	providerHost, err := host.New(ctx, store, envelope, nil, func() time.Time { return now }, nil, registration)
	if err != nil {
		t.Fatal(err)
	}
	defer providerHost.Close()

	workflow := application.New(
		store,
		profile.Registrations,
		providerHost,
		nil,
		func() time.Time { return now },
		nil,
	)
	key := host.WorkKey{Connection: "synthetic", Stream: ibkrflex.Confirmations, Operation: "acquire"}
	blocked := host.WorkKey{Connection: "unsupported", Stream: "activity", Operation: "acquire"}

	if _, err := store.Checkpoint(ctx, key); !errors.Is(err, provider.ErrUnavailable) {
		t.Fatal("unreviewed source admitted", err)
	}

	original, err := workflow.PreviewRestart(ctx)
	if err != nil || !original.Required || len(original.Scopes) != 2 ||
		original.Scopes[0].Blocked == "" && original.Scopes[1].Blocked == "" {
		t.Fatal(original, err)
	}

	changeSettings := func(queryID string) {
		t.Helper()

		connection, err := store.Connection(ctx, "synthetic")
		if err != nil {
			t.Fatal(err)
		}

		connection.Settings = confirmationSettings(queryID)
		if err := store.Reconfigure(ctx, connection); err != nil {
			t.Fatal(err)
		}
	}
	changeSettings("456")

	if err := workflow.ApproveRestart(
		ctx,
		original.Revision,
		original.Digest,
		"admin/stale",
	); !errors.Is(
		err,
		application.ErrStale,
	) {
		t.Fatal("old preview admitted changed configuration", err)
	}

	review, err := workflow.PreviewRestart(ctx)
	if err != nil {
		t.Fatal(err)
	}

	for _, scope := range review.Scopes {
		if scope.Connection != key.Connection {
			continue
		}

		if scope.Blocked != "" || !scope.NotBefore.Equal(floor) {
			t.Fatal("surviving pacing was not carried forward", scope.Blocked, scope.NotBefore, floor)
		}

		wantedFrom.Store(compactFixtureDate(scope.Plan.From().String()))
		wantedTo.Store(compactFixtureDate(scope.Plan.To().String()))
	}

	if err := workflow.ApproveRestart(ctx, review.Revision, review.Digest, "admin/fixture"); err != nil {
		t.Fatal(err)
	}

	if _, err := store.Checkpoint(ctx, blocked); !errors.Is(err, provider.ErrUnavailable) {
		t.Fatal("unsupported source admitted", err)
	}

	if allowed, _, err := store.ReserveReference(ctx, now, 25); err != nil || allowed {
		t.Fatal("unreviewed reference access admitted", err)
	}

	if err := store.Retry(ctx, key); err != nil {
		t.Fatal(err)
	}

	if err := workflow.RunOnce(ctx, now); err != nil || called.Load() != 0 {
		t.Fatal("retry bypassed recovery pacing", err, called.Load())
	}

	now = floor.Add(time.Second)
	if err := workflow.RunOnce(ctx, now); err != nil || called.Load() != 1 {
		t.Fatal("approved source did not request its bounded report", err, called.Load())
	}

	revision, err := store.UpstreamRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}

	old := upstream.WithWork(upstream.WithRevision(ctx, revision), upstream.Work(key))

	changeSettings("789")

	if _, err := store.Secret(old, key.Connection, "token"); !errors.Is(err, provider.ErrConflict) {
		t.Fatal("superseded acquisition retained authority", err)
	}

	if _, err := store.Checkpoint(ctx, key); !errors.Is(err, provider.ErrUnavailable) {
		t.Fatal("new settings inherited the old scope", err)
	}

	changeSettings("456")

	if _, err := store.Checkpoint(ctx, key); !errors.Is(err, provider.ErrUnavailable) {
		t.Fatal("restoring prior settings revived the revoked scope", err)
	}
}

func compactFixtureDate(date string) string { return date[:4] + date[5:7] + date[8:] }

func TestInstallationArchiveFromPreviousSyncFormatRequiresReviewedRestart(t *testing.T) {
	ctx := t.Context()
	profile := seedUpgradeStores(t)
	paths := profile.paths()

	store, err := OpenInstalled(ctx, profile, nil)
	if err != nil {
		t.Fatal(err)
	}

	_, err = store.db.Exec(
		"DROP TABLE sync.restart_scopes; DROP TABLE sync.recovery_sources; DROP TABLE sync.recovery_state;" +
			"UPDATE sync.store_metadata SET version='wake-sync/1'",
	)
	if err := errors.Join(err, store.Close()); err != nil {
		t.Fatal(err)
	}

	var owners [storeCount]*os.File
	for i, path := range paths {
		owner, err := lockStoreFile(path, false)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = owner.Close() }()

		owners[i] = owner
	}

	plan, err := validateStorageSet(ctx, paths[:], owners[:], installationArchiveBaseline())
	if err != nil {
		t.Fatal(err)
	}

	archive := filepath.Join(filepath.Dir(paths[0]), "previous-format.tar")
	if err := writeInstallationBackup(ctx, archive, paths, owners, plan, InstallationEvidence{}); err != nil {
		t.Fatal(err)
	}

	closeRestoreCopies(owners)

	if _, err := OpenInstalled(ctx, profile, nil); !errors.Is(err, ErrUnsupportedSchema) {
		t.Fatal("ordinary startup accepted the prior sync format", err)
	}

	if recovery, err := RecoverSynchronization(
		ctx,
		profile,
		archive,
		"",
	); err != nil ||
		len(recovery.Proposed) != 1 {
		t.Fatal("prior archive did not propose reviewed settings", recovery, err)
	}

	target := vacantRecoveryProfile(t)
	if _, err := Restore(ctx, target, archive, true); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenInstalled(ctx, target, nil)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = reopened.Close() }()

	restart, err := reopened.RestartInputs(ctx)
	if err != nil || restart.At.IsZero() || len(restart.Sources) != 1 {
		t.Fatal("restored prior archive bypassed restart review", restart, err)
	}

	if _, err := reopened.UpstreamRevision(ctx); !errors.Is(err, provider.ErrUnavailable) {
		t.Fatal("restored prior archive activated an upstream grant", err)
	}
}
