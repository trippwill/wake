// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"bytes"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.trippwill.dev/wake/financial"
	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/host"
	"go.trippwill.dev/wake/internal/upstream"
	"go.trippwill.dev/wake/provider"
	"go.trippwill.dev/wake/source"
)

func upstreamTokens(t *testing.T, s *Store) (string, string) {
	t.Helper()

	var current, witness string
	if err := s.db.QueryRowContext(t.Context(), `SELECT s.token,a.token FROM sync_association s CROSS JOIN access.upstream_grant a`).
		Scan(&current, &witness); err != nil {
		t.Fatal(err)
	}

	return current, witness
}

func TestOperationalWitnessRejectsStaleAccess(t *testing.T) {
	s := newStoreWithSyntheticAccount(t)
	ctx := t.Context()

	revision, err := s.UpstreamRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}

	before, _ := upstreamTokens(t, s)
	now := time.Now()
	admitted := upstream.WithRevision(ctx, revision)
	if _, err = s.ReserveTogether(
		admitted,
		"synthetic",
		now,
		host.PacingBudget{Policy: "request", Scope: "scope", Spacing: time.Minute},
	); err != nil {
		t.Fatal(err)
	}

	after, witness := upstreamTokens(t, s)
	if before == after || after != witness {
		t.Fatal("pacing did not rotate both witnesses")
	}

	key := host.WorkKey{Connection: "synthetic", Stream: "confirmations", Operation: "acquire"}

	checkpoint, err := s.Checkpoint(admitted, key)
	if err != nil {
		t.Fatal(err)
	}

	before, _ = upstreamTokens(t, s)

	step, err := provider.NewStep(provider.State{}, provider.Artifact{}, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}

	if id, e := s.Accept(
		admitted,
		key,
		checkpoint.Revision,
		step,
		nil,
		provider.Progress{},
		host.PollCycle{},
	); e != nil ||
		id != "" {
		t.Fatal(id, e)
	}

	after, witness = upstreamTokens(t, s)
	if before == after || after != witness {
		t.Fatal("artifact-free acceptance did not rotate both witnesses")
	}

	if _, err = s.db.ExecContext(ctx, `UPDATE access.upstream_grant SET token=?`, before); err != nil {
		t.Fatal(err)
	}

	if _, err = s.UpstreamRevision(ctx); !errors.Is(err, ErrAssociation) {
		t.Fatal("stale access witness was accepted", err)
	}

	if _, _, err = s.ReserveReference(ctx, now, 10); !errors.Is(err, ErrAssociation) {
		t.Fatal("stale witness allowed reference reservation", err)
	}

	if _, err = s.Snapshot(ctx, application.AnySnapshotVersion()); err != nil {
		t.Fatal("witness mismatch blocked local snapshot", err)
	}
}

func TestOperationalWitnessFailureRollsBackPacing(t *testing.T) {
	for _, fixture := range []struct{ name, sql string }{
		{"statement", `CREATE TRIGGER access.reject_witness BEFORE UPDATE ON upstream_grant BEGIN SELECT RAISE(ABORT,'fixture'); END`},
		{"commit", `CREATE TABLE access.guard(id INTEGER REFERENCES upstream_grant(singleton) DEFERRABLE INITIALLY DEFERRED);
    CREATE TRIGGER access.reject_witness AFTER UPDATE ON upstream_grant BEGIN INSERT INTO guard VALUES(2); END`},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			s := newStoreWithSyntheticAccount(t)
			ctx := t.Context()
			before, _ := upstreamTokens(t, s)
			if _, err := s.db.ExecContext(ctx, fixture.sql); err != nil {
				t.Fatal(err)
			}

			if _, err := s.ReserveTogether(
				ctx,
				"synthetic",
				time.Now(),
				host.PacingBudget{Policy: "request", Scope: "scope", Spacing: time.Minute},
			); err == nil {
				t.Fatal("failed witness write acknowledged")
			}

			current, witness := upstreamTokens(t, s)
			if current != before || current != witness || tableCount(t, s, "pacing") != 0 {
				t.Fatal("failed witness write left a partial transaction")
			}
		})
	}
}

func TestAuthorityChangeRejectsInflightResultsAndStaleSettings(t *testing.T) {
	s := newStoreWithSyntheticAccount(t)
	ctx := t.Context()
	if _, err := s.db.ExecContext(
		ctx,
		`INSERT INTO sync.configurations(id,provider,name,settings) VALUES('synthetic','ibkr-flex','Synthetic',X'7b7d')`,
	); err != nil {
		t.Fatal(err)
	}

	conn, err := s.Connection(ctx, "synthetic")
	if err != nil {
		t.Fatal(err)
	}

	revision, err := s.UpstreamRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}

	admitted := upstream.WithRevision(ctx, revision)
	key := host.WorkKey{Connection: "synthetic", Stream: "confirmations", Operation: "acquire"}

	checkpoint, err := s.Checkpoint(admitted, key)
	if err != nil {
		t.Fatal(err)
	}

	step, err := provider.NewStep(provider.State{}, provider.Artifact{}, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}

	changed := make(chan error, 1)
	go func() {
		_, err := s.ReplaceSecret(ctx, "synthetic", "token", []byte("test ciphertext"))
		changed <- err
	}()

	if err = <-changed; err != nil {
		t.Fatal(err)
	}

	if _, err = s.Accept(
		admitted,
		key,
		checkpoint.Revision,
		step,
		nil,
		provider.Progress{},
		host.PollCycle{},
	); !errors.Is(
		err,
		provider.ErrConflict,
	) {
		t.Fatal("stale result admitted", err)
	}

	if _, _, err = s.ReserveReference(admitted, time.Now(), 10); !errors.Is(err, provider.ErrConflict) {
		t.Fatal("stale reference admission", err)
	}

	if err = s.SavePolling(ctx, conn, "confirmations", time.Minute); !errors.Is(err, provider.ErrConflict) {
		t.Fatal("stale polling change accepted", err)
	}

	if err = s.Reconfigure(ctx, conn); !errors.Is(err, provider.ErrConflict) {
		t.Fatal("stale settings change accepted", err)
	}

	current, err := s.Checkpoint(ctx, key)
	if err != nil || current.Revision != checkpoint.Revision {
		t.Fatal("stale result advanced checkpoint", current, err)
	}

	if err = s.PauseForCredentialKey(ctx); err != nil {
		t.Fatal(err)
	}

	if _, err = s.UpstreamRevision(ctx); !errors.Is(err, provider.ErrUnavailable) {
		t.Fatal("paused association admitted upstream work", err)
	}
}

func TestRestartRejectsOlderAccessCopyWithSameIdentity(t *testing.T) {
	profile, s := profileFixture(t)
	ctx := t.Context()

	revision, err := s.UpstreamRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if err = s.Close(); err != nil {
		t.Fatal(err)
	}

	olderAccess, err := os.ReadFile(profile.ConnectionsPath)
	if err != nil {
		t.Fatal(err)
	}

	s, err = Open(ctx, profile, nil)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = s.Close() }()

	if current, e := s.UpstreamRevision(ctx); e != nil || current != revision {
		t.Fatal("restart changed authorization", e)
	}

	if err = s.Delay(ctx, "ibkr-flex", "requests", "scope", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	if err = s.Close(); err != nil {
		t.Fatal(err)
	}

	//nolint:gosec // The path belongs to the synthetic profile in t.TempDir.
	if err = os.WriteFile(profile.ConnectionsPath, olderAccess, 0o600); err != nil {
		t.Fatal(err)
	}

	if err = Backup(
		ctx,
		profile,
		filepath.Join(t.TempDir(), "stale-pair.tar"),
	); !errors.Is(
		err,
		ErrAssociation,
	) {
		t.Fatal("backup accepted mismatched witnesses", err)
	}

	s, err = Open(ctx, profile, nil)
	if err != nil {
		t.Fatal("older access copy blocked local startup", err)
	}

	if _, err = s.UpstreamRevision(ctx); !errors.Is(err, ErrAssociation) {
		t.Fatal("older access copy admitted upstream work", err)
	}

	if _, err = s.Snapshot(ctx, application.AnySnapshotVersion()); err != nil {
		t.Fatal(err)
	}
}

func TestOperationalWriteCannotRenewStaleGrant(t *testing.T) {
	s := newStoreWithSyntheticAccount(t)
	ctx := t.Context()
	if _, err := s.db.ExecContext(
		ctx,
		`UPDATE access.upstream_grant SET granted_revision='old-revision'`,
	); err != nil {
		t.Fatal(err)
	}

	if err := s.Delay(ctx, "ibkr-flex", "requests", "scope", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	if _, err := s.UpstreamRevision(ctx); !errors.Is(err, provider.ErrUnavailable) {
		t.Fatal("operational write revived stale authorization", err)
	}

	view, err := s.Dashboard(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if err = s.ResumeStorage(
		ctx,
		view.Storage.Revision,
		"admin/test",
		time.Now(),
	); !errors.Is(
		err,
		application.ErrAccessReview,
	) {
		t.Fatal("unpaused profile bypassed the required grant", err)
	}
}

func TestConfirmationRequiresCurrentConfigurationRevision(t *testing.T) {
	s, _ := testStore(t)
	ctx := t.Context()

	settings, err := provider.NewSettings(map[string]string{"query_id": "123"})
	if err != nil {
		t.Fatal(err)
	}

	connection := host.Connection{
		ID:       "connection",
		Provider: "ibkr-flex",
		Name:     "Synthetic",
		Settings: settings,
	}
	if err = s.Configure(ctx, connection, nil); err != nil {
		t.Fatal(err)
	}

	key := host.WorkKey{Connection: connection.ID, Stream: "confirmations", Operation: "discover"}

	cp, err := s.Checkpoint(ctx, key)
	if err != nil {
		t.Fatal(err)
	}

	ref, err := source.NewReference("ibkr", "account", "", "U1")
	if err != nil {
		t.Fatal(err)
	}

	candidate, err := provider.NewCandidate(ref, "Synthetic", financial.Currency{})
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now()

	artifact, err := provider.NewArtifact(
		"ibkr-flex-confirmation-xml",
		"application/xml",
		now,
		[]byte("<fixture/>"),
	)
	if err != nil {
		t.Fatal(err)
	}

	step, err := provider.NewStep(provider.State{}, artifact, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}

	id, err := s.Accept(
		ctx,
		key,
		cp.Revision,
		step,
		[]provider.Candidate{candidate},
		provider.Progress{},
		host.PollCycle{},
	)
	if err != nil {
		t.Fatal(err)
	}

	stale, err := s.Connection(ctx, connection.ID)
	if err != nil {
		t.Fatal(err)
	}
	// The settings bytes are unchanged, but the credential repair invalidates
	// the earlier confirmation snapshot.
	if _, err = s.ReplaceSecret(ctx, connection.ID, "token", []byte("synthetic")); err != nil {
		t.Fatal(err)
	}

	details, err := application.NewAccountDetails("Synthetic", "")
	if err != nil {
		t.Fatal(err)
	}

	if _, err = s.Confirm(ctx, stale, id, ref, details); !errors.Is(err, application.ErrConnectionChanged) {
		t.Fatal("stale target confirmed", err)
	}

	current, err := s.Connection(ctx, connection.ID)
	if err != nil {
		t.Fatal(err)
	}

	if _, err = s.Confirm(ctx, current, id, ref, details); err != nil {
		t.Fatal(err)
	}
}

func TestUpstreamAdmissionAndBackupRequireCurrentFinancialEpoch(t *testing.T) {
	profile, s := profileFixture(t)
	ctx := t.Context()
	// Simulate replacing the financial publication identity outside the restore workflow.
	if _, err := s.db.ExecContext(ctx, `UPDATE store_metadata SET id='different-epoch'`); err != nil {
		t.Fatal(err)
	}

	if _, err := s.UpstreamRevision(ctx); !errors.Is(err, ErrAssociation) {
		t.Fatal("admission accepted the old grant for a different epoch", err)
	}

	if err := s.Delay(
		ctx,
		"ibkr-flex",
		"requests",
		"scope",
		time.Now().Add(time.Minute),
	); !errors.Is(
		err,
		ErrAssociation,
	) {
		t.Fatal("write rotated witnesses after an epoch mismatch", err)
	}

	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	if err := Backup(
		ctx,
		profile,
		filepath.Join(t.TempDir(), "stale-epoch.tar"),
	); !errors.Is(
		err,
		ErrAssociation,
	) {
		t.Fatal("backup accepted mismatched epoch", err)
	}
}

func TestReplaceSecretChecksStoredProviderBinding(t *testing.T) {
	ctx := t.Context()
	s, _ := testStore(t)
	original := []byte("original synthetic ciphertext")
	if err := s.Configure(
		ctx,
		host.Connection{ID: "bound", Provider: "synthetic"},
		map[string][]byte{"token": original},
	); err != nil {
		t.Fatal(err)
	}

	_, err := s.ReplaceSecret(ctx, "unknown", "token", []byte("replacement"))
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("replacement accepted an unknown connection", err)
	}

	if _, err := s.db.ExecContext(
		ctx,
		`UPDATE access.identities SET provider='other' WHERE id='bound'`,
	); err != nil {
		t.Fatal(err)
	}

	_, err = s.ReplaceSecret(ctx, "bound", "token", []byte("replacement"))
	if !errors.Is(err, ErrAssociation) {
		t.Fatal("replacement accepted a provider mismatch", err)
	}

	stillStored, err := s.Secret(ctx, "bound", "token")
	if err != nil || !bytes.Equal(stillStored, original) {
		t.Fatal("provider mismatch changed encrypted credentials", err)
	}
}
