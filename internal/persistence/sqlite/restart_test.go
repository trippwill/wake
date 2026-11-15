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
	"go.trippwill.dev/wake/internal/reference"
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
	refFloor := floor.Add(time.Hour)

	store, err := OpenInstalled(ctx, profile, nil)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := store.db.Exec(`UPDATE sync.configurations SET settings=CAST('{"query_id":"123"}' AS BLOB);
	INSERT INTO main.connections(id,provider,name) VALUES('unsupported','unknown-provider','Unsupported source');
	INSERT INTO sync.configurations(id,provider,settings) VALUES('unsupported','unknown-provider',X'7b7d');
	INSERT INTO main.connections(id,provider,name) VALUES('unconfirmed','ibkr-flex','Unconfirmed account');
	INSERT INTO sync.configurations(id,provider,settings) VALUES('unconfirmed','ibkr-flex',X'7b7d');
	INSERT INTO main.connections(id,provider,name) VALUES('unprovisioned','ibkr-flex','Unprovisioned account');
	INSERT INTO main.accounts(id,name,currency,connection_id,provider,authority,kind,scope,value,artifact_id)
	VALUES('unprovisioned-account','Unprovisioned account','','unprovisioned','ibkr-flex','ibkr','account','','U2','discovery');
	INSERT INTO sync.configurations(id,provider,settings) VALUES('unprovisioned','ibkr-flex',X'7b7d');`); err != nil {
		t.Fatal(err)
	}

	if err := store.ReplaceSecret(ctx, "synthetic", "token", payload); err != nil {
		t.Fatal(err)
	}

	if _, err := store.db.Exec("UPDATE acquisition_receipts SET reported_to=? WHERE operation='acquire'",
		time.Now().AddDate(0, 0, -3).UTC().Format(time.DateOnly)); err != nil {
		t.Fatal(err)
	}

	if err := store.Delay(
		ctx,
		"ibkr-flex",
		"web-service",
		"surviving-budget",
		floor.Add(-time.Hour),
	); err != nil {
		t.Fatal(err)
	}

	if allowed, _, err := store.ReserveReference(ctx, time.Now(), 25); err != nil || !allowed {
		t.Fatal("unable to reserve reference fixture", err)
	}

	if err := store.ReferenceCooldown(ctx, refFloor); err != nil {
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

	if _, err := store.db.Exec("DELETE FROM sync.reference_budget"); err != nil {
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

	if _, err := store.db.Exec("UPDATE sync.recovery_state SET recovered_at=?",
		time.Now().UTC().AddDate(0, 0, -2).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}

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
	if err != nil || !original.Required || len(original.Scopes) != 4 {
		t.Fatal(original, err)
	}

	for _, scope := range original.Scopes {
		if scope.Connection != key.Connection && scope.Blocked == "" {
			t.Fatal("ineligible scope was presented for approval", scope.Connection)
		}
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

	if err := store.ApproveRestart(ctx, review, "", now); !errors.Is(err, application.ErrAccessReview) {
		t.Fatal("storage accepted an approval without an actor", err)
	}

	stale := review
	stale.Revision--
	if err := store.ApproveRestart(ctx, stale, "admin/fixture", now); !errors.Is(err, application.ErrStale) {
		t.Fatal("storage accepted an obsolete approval revision", err)
	}

	for _, write := range []string{
		"INSERT ON sync.restart_scopes",
		"INSERT ON sync.work",
		"INSERT ON sync.scheduled",
		"UPDATE ON sync.recovery_state",
		"UPDATE ON sync.profile",
		"UPDATE ON access.upstream_grant",
	} {
		//nolint:gosec // The test uses only fixed table names to inject failures.
		query := "CREATE TEMP TRIGGER refuse_restart BEFORE " + write +
			" BEGIN SELECT RAISE(ABORT,'fixture restart failure'); END"
		if _, err := store.db.Exec(query); err != nil {
			t.Fatal(err)
		}

		if err := workflow.ApproveRestart(ctx, review.Revision, review.Digest, "admin/fixture"); err == nil {
			t.Fatal("approval ignored failed write", write)
		}

		if _, err := store.db.Exec("DROP TRIGGER temp.refuse_restart"); err != nil {
			t.Fatal(err)
		}

		if _, err := store.Checkpoint(ctx, key); !errors.Is(err, provider.ErrUnavailable) {
			t.Fatal("partial approval was visible", write, err)
		}
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

	refService, err := reference.New(
		store,
		envelope,
		server.Client(),
		server.URL+"/mapping",
		func() time.Time { return now },
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}

	withReference := application.New(
		store,
		profile.Registrations,
		providerHost,
		refService,
		func() time.Time { return now },
		nil,
	)

	referenceReview, err := withReference.PreviewRestart(ctx)
	if err != nil || !referenceReview.Reference || !referenceReview.ReferenceAt.Equal(refFloor) ||
		!referenceReview.Scopes[0].Approved {
		t.Fatal("review lost the reference floor or existing scope", referenceReview, err)
	}

	if err := withReference.ApproveRestart(
		ctx,
		referenceReview.Revision,
		referenceReview.Digest,
		"admin/fixture",
	); err != nil {
		t.Fatal(err)
	}

	if allowed, next, err := store.ReserveReference(
		ctx,
		now,
		25,
	); err != nil || allowed ||
		!next.Equal(refFloor) {
		t.Fatal("reference approval bypassed a surviving cooldown", allowed, next, err)
	}

	if allowed, _, err := store.ReserveReference(ctx, refFloor.Add(time.Second), 25); err != nil || !allowed {
		t.Fatal("reference remained paused after approved pacing expired", err)
	}

	referenceReview, err = withReference.PreviewRestart(ctx)
	if err != nil {
		t.Fatal(err)
	}

	before, _ := upstreamTokens(t, store)
	if err := withReference.ApproveRestart(
		ctx,
		referenceReview.Revision,
		referenceReview.Digest,
		"admin/fixture",
	); err != nil {
		t.Fatal(err)
	}

	if token, _ := upstreamTokens(t, store); token != before {
		t.Fatal("repeat approval changed existing authority")
	}

	for _, configured := range []string{"synthetic replacement key", ""} {
		updated := credential.NewSecret([]byte(configured))
		err := withReference.SetReferenceKey(ctx, updated)
		updated.Clear()

		if err != nil {
			t.Fatal(err)
		}

		afterCooldown := refFloor.Add(2 * time.Minute)
		if allowed, _, err := store.ReserveReference(ctx, afterCooldown, 25); err != nil || allowed {
			t.Fatal("changed OpenFIGI key inherited the old grant", err)
		}

		if err := withReference.ApproveRestart(
			ctx,
			referenceReview.Revision,
			referenceReview.Digest,
			"admin/fixture",
		); !errors.Is(
			err,
			application.ErrStale,
		) {
			t.Fatal("old review authorized changed OpenFIGI credentials", err)
		}

		referenceReview, err = withReference.PreviewRestart(ctx)
		if err != nil || !referenceReview.Reference {
			t.Fatal("updated OpenFIGI configuration could not be reviewed", err)
		}

		if err := withReference.ApproveRestart(
			ctx,
			referenceReview.Revision,
			referenceReview.Digest,
			"admin/fixture",
		); err != nil {
			t.Fatal(err)
		}

		if allowed, _, err := store.ReserveReference(ctx, afterCooldown, 25); err != nil || !allowed {
			t.Fatal("fresh reference grant did not admit work", err)
		}

		referenceReview, err = withReference.PreviewRestart(ctx)
		if err != nil {
			t.Fatal(err)
		}
	}

	// A report can advance durable coverage long after the original recovery.
	through := time.Now().UTC().Format(time.DateOnly)
	if _, err := store.db.Exec(
		"UPDATE acquisition_receipts SET reported_to=? WHERE operation='acquire'",
		through,
	); err != nil {
		t.Fatal(err)
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

	review, err = withReference.PreviewRestart(ctx)
	if err != nil || review.Scopes[0].Connection != key.Connection ||
		review.Scopes[0].Blocked != "" || review.Scopes[0].Plan.To().String() != through {
		t.Fatal("reapproval did not include newer retained coverage", review, err)
	}

	repeated, err := withReference.PreviewRestart(ctx)
	if err != nil || repeated.Digest != review.Digest {
		t.Fatal("source planning time changed between previews", err)
	}

	if err := withReference.ApproveRestart(ctx, review.Revision, review.Digest, "admin/fixture"); err != nil {
		t.Fatal(err)
	}

	if err := store.ReplaceSecret(ctx, key.Connection, "token", payload); err != nil {
		t.Fatal(err)
	}

	if _, err := store.Checkpoint(ctx, key); !errors.Is(err, provider.ErrUnavailable) {
		t.Fatal("replacement credential inherited the prior scope", err)
	}

	review, err = withReference.PreviewRestart(ctx)
	if err != nil || review.Scopes[0].Blocked != "" || review.Scopes[0].Plan.To().String() != through {
		t.Fatal("replacement credential could not be reviewed", review, err)
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
