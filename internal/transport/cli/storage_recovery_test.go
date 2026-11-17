// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.trippwill.dev/wake/internal/clientaccess"
	"go.trippwill.dev/wake/internal/config"
	"go.trippwill.dev/wake/internal/credential"
	persistence "go.trippwill.dev/wake/internal/persistence/sqlite"
	"go.trippwill.dev/wake/internal/recovery"
)

func TestInstallationBackupAndRestorePreserveCurrentLocalAuthority(t *testing.T) {
	ctx := t.Context()
	cfg, configPath, _ := setupDaemonFixture(t)
	if err := config.Prepare(cfg); err != nil {
		t.Fatal(err)
	}

	profile := persistence.Profile{
		WarehousePath: cfg.Storage.WarehousePath, ConnectionsPath: cfg.Storage.ConnectionsPath,
		SyncPath: cfg.Storage.SyncPath,
	}

	store, err := persistence.Open(ctx, profile, nil)
	if err != nil {
		t.Fatal(err)
	}

	seedRecoveryCredential(t, cfg.Credentials.KeyPath, store)

	auth := clientaccess.New(clientaccess.NewFileStore(cfg.Paths.ConfigDir))

	client, token, err := auth.Create(ctx, "synthetic backup client")
	if err != nil {
		t.Fatal(err)
	}
	defer token.Clear()

	run := func(want int, command ...string) string {
		t.Helper()

		var stdout, stderr bytes.Buffer

		args := append([]string{"--json", "storage"}, command...)
		args = append(args, "--config", configPath)

		if status := Run(args, &stdout, &stderr); status != want {
			t.Fatal(status, stdout.String(), stderr.String())
		}

		if want == exitOK {
			return stdout.String()
		}

		return stderr.String()
	}

	archive := filepath.Join(cfg.Paths.ConfigDir, "offline.tar")
	run(exitFailure, "backup", archive)

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	localAuthPath := filepath.Join(cfg.Paths.ConfigDir, clientaccess.FileName)
	//nolint:gosec // Deliberately loosen permissions on the disposable authentication fixture.
	if err := os.Chmod(localAuthPath, 0o640); err != nil {
		t.Fatal(err)
	}

	if out := run(
		exitFailure,
		"backup",
		archive,
	); !strings.Contains(
		out,
		"invalid private authentication state",
	) {
		t.Fatal(out)
	}

	if err := os.Chmod(localAuthPath, 0o600); err != nil {
		t.Fatal(err)
	}

	var result storageResult
	if err := json.Unmarshal([]byte(run(exitOK, "backup", archive)), &result); err != nil ||
		result.Event != "backup" || result.Archive != archive || result.Scope != installationScope {
		t.Fatal(result, err)
	}

	if err := auth.Revoke(ctx, client.ID()); err != nil {
		t.Fatal(err)
	}

	if out := run(exitFailure, "restore", "installation", archive); !strings.Contains(out, "occupied") {
		t.Fatal(out)
	}

	key := cfg.Credentials.KeyPath
	if err := os.Rename(key, key+".original"); err != nil {
		t.Fatal(err)
	}

	if err := credential.CreateMasterKey(key); err != nil {
		t.Fatal(err)
	}

	if out := run(
		exitFailure,
		"restore",
		"installation",
		"--replace",
		archive,
	); !strings.Contains(
		out,
		"matching credential encryption key",
	) {
		t.Fatal(out)
	}

	if _, err := os.Stat(cfg.Storage.WarehousePath + persistence.RestoreIntentSuffix); !os.IsNotExist(err) {
		t.Fatal("wrong credential key published a restore intent", err)
	}

	if err := os.Rename(key+".original", key); err != nil {
		t.Fatal(err)
	}

	//nolint:gosec // Deliberately loosen permissions on the disposable credential key.
	if err := os.Chmod(key, 0o640); err != nil {
		t.Fatal(err)
	}

	run(exitFailure, "restore", "installation", "--replace", archive)

	if err := os.Chmod(key, 0o600); err != nil {
		t.Fatal(err)
	}

	run(exitUsage, "restore", "financial", "--replace", archive)

	if err := json.Unmarshal(
		[]byte(run(exitOK, "restore", "installation", "--replace", archive)),
		&result,
	); err != nil ||
		result.Event != "restore" || result.Scope != installationScope ||
		len(result.Destinations) != 3 {
		t.Fatal(result, err)
	}

	clients, err := auth.Clients(ctx)
	if err != nil || len(clients) != 0 {
		t.Fatal("restore resurrected a revoked client", clients, err)
	}

	reopened, err := persistence.Open(ctx, profile, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()

	if _, err := reopened.UpstreamRevision(ctx); err == nil {
		t.Fatal("restore carried forward upstream authority")
	}
}

func TestInstallationBackupReportsDestinationWithoutLocalAuth(t *testing.T) {
	cfg, configPath, _ := setupDaemonFixture(t)
	if err := config.Prepare(cfg); err != nil {
		t.Fatal(err)
	}

	store, err := persistence.Open(t.Context(), persistence.Profile{
		WarehousePath:   cfg.Storage.WarehousePath,
		ConnectionsPath: cfg.Storage.ConnectionsPath,
		SyncPath:        cfg.Storage.SyncPath,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(cfg.Paths.RuntimeDir); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer

	archive := filepath.Join(cfg.Paths.ConfigDir, "offline.tar")
	if status := Run(
		[]string{"storage", "backup", "--config", configPath, archive},
		&stdout,
		&stderr,
	); status != exitOK ||
		!strings.Contains(stdout.String(), "Saved an installation backup to "+archive) {
		t.Fatal(status, stdout.String(), stderr.String())
	}
}

func seedRecoveryCredential(t *testing.T, path string, store *persistence.Store) {
	t.Helper()

	if err := credential.CreateMasterKey(path); err != nil {
		t.Fatal(err)
	}

	key, err := credential.LoadMasterKey(path)
	if err != nil {
		t.Fatal(err)
	}
	defer key.Clear()

	envelope, err := credential.NewEnvelope(key)
	if err != nil {
		t.Fatal(err)
	}
	defer envelope.Clear()

	secret := credential.NewSecret([]byte("synthetic backup fixture"))
	defer secret.Clear()

	encrypted, err := envelope.Encrypt(secret, credential.ReferenceKeyAAD())
	if err != nil {
		t.Fatal(err)
	}

	body, err := encrypted.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}

	if err := store.SetReferenceKey(t.Context(), body); err != nil {
		t.Fatal(err)
	}
}

func TestStorageFinancialSnapshotCLI(t *testing.T) {
	cfg, configPath, _ := setupDaemonFixture(t)
	if err := config.Prepare(cfg); err != nil {
		t.Fatal(err)
	}

	store, err := persistence.OpenFinancial(t.Context(), persistence.Profile{
		WarehousePath: cfg.Storage.WarehousePath, ConnectionsPath: cfg.Storage.ConnectionsPath,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	archive := filepath.Join(cfg.Paths.ConfigDir, "history.tar")

	var stdout, stderr bytes.Buffer

	status := Run(
		[]string{"--json", "storage", "snapshot", "--config", configPath, archive},
		&stdout,
		&stderr,
	)

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	if status != exitOK {
		t.Fatal(status, stdout.String(), stderr.String())
	}

	var result storageResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil || result.Event != "snapshot" ||
		result.Archive != archive || result.Scope != financialScope {
		t.Fatal(stdout.String(), err)
	}

	stdout.Reset()
	stderr.Reset()

	if status := Run(
		[]string{"storage", "snapshot", "--config", configPath, archive + ".other"},
		&stdout,
		&stderr,
	); status != exitOK ||
		!strings.Contains(stdout.String(), "Saved a financial snapshot") {
		t.Fatal(status, stdout.String(), stderr.String())
	}

	if err := os.Rename(cfg.Storage.WarehousePath, cfg.Storage.WarehousePath+".original"); err != nil {
		t.Fatal(err)
	}

	stdout.Reset()
	stderr.Reset()

	restore := []string{"storage", "restore", "financial", "--config", configPath, archive}
	if status := Run(restore, &stdout, &stderr); status != exitOK ||
		!strings.Contains(stdout.String(), "Start Wake to read retained history") {
		t.Fatal(status, stdout.String(), stderr.String())
	}

	stdout.Reset()
	stderr.Reset()

	if status := Run(restore, &stdout, &stderr); status == exitOK ||
		!strings.Contains(stderr.String(), cfg.Storage.WarehousePath) {
		t.Fatal(status, stdout.String(), stderr.String())
	}
}

func TestRecoverSyncCLIRequiresReviewedPlan(t *testing.T) {
	cfg, configPath, _ := setupDaemonFixture(t)
	if err := config.Prepare(cfg); err != nil {
		t.Fatal(err)
	}

	store, err := persistence.OpenFinancial(
		t.Context(),
		persistence.Profile{WarehousePath: cfg.Storage.WarehousePath},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer

	args := []string{"--json", "storage", "recover-sync", "--config", configPath}
	if status := Run(args, &stdout, &stderr); status != exitOK {
		t.Fatal(status, stdout.String(), stderr.String())
	}

	var plan recovery.SyncPlan
	if err := json.Unmarshal(
		stdout.Bytes(),
		&plan,
	); err != nil || plan.ID == "" || plan.Applied ||
		plan.RetainAccess {
		t.Fatal(plan, err)
	}

	if _, err := os.Lstat(cfg.Storage.SyncPath); !os.IsNotExist(err) {
		t.Fatal("preview created optional files", err)
	}

	stdout.Reset()
	stderr.Reset()

	if status := Run(append(args, "--confirm", plan.ID), &stdout, &stderr); status != exitOK {
		t.Fatal(status, stdout.String(), stderr.String())
	}

	if err := json.Unmarshal(stdout.Bytes(), &plan); err != nil || !plan.Applied {
		t.Fatal(plan, err)
	}
}

func TestStorageRecoveryProtectsInstancePaths(t *testing.T) {
	cfg, configPath, _ := setupDaemonFixture(t)
	if err := config.Prepare(cfg); err != nil {
		t.Fatal(err)
	}

	existing := filepath.Join(cfg.Paths.ConfigDir, "existing.tar")
	if err := os.WriteFile(existing, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name string
		args []string
		code int
		want string
	}{
		{"configuration", []string{"snapshot", configPath}, exitFailure, "unsafe"},
		{"backup configuration", []string{"backup", configPath}, exitFailure, "unsafe"},
		{"key", []string{"snapshot", cfg.Credentials.KeyPath}, exitFailure, "unsafe"},
		{"sync sidecar", []string{"snapshot", cfg.Storage.SyncPath + "-journal"}, exitFailure, "unsafe"},
		{"sidecar", []string{"snapshot", cfg.Storage.WarehousePath + "-wal"}, exitFailure, "unsafe"},
		{"existing archive", []string{"snapshot", existing}, exitFailure, "output already exists"},
		{"missing output directory", []string{"snapshot", filepath.Join(cfg.Paths.ConfigDir, "missing", "archive")}, exitFailure, "no such file"},
		{"protected restore source", []string{"restore", "financial", configPath}, exitFailure, "unsafe"},
		{"missing restore archive", []string{"restore", "financial", filepath.Join(cfg.Paths.ConfigDir, "absent.tar")}, exitFailure, "no such file"},
		{"missing snapshot output", []string{"snapshot"}, exitUsage, "requires one archive file path"},
		{"protected synchronization archive", []string{"recover-sync", configPath}, exitFailure, "unsafe"},
		{"multiple synchronization archives", []string{"recover-sync", "first.tar", "second.tar"}, exitUsage, "at most one installation backup path"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer

			args := append([]string{"storage"}, test.args...)
			args = append(args, "--config", configPath)
			if result := Run(args, &stdout, &stderr); result != test.code ||
				!strings.Contains(stderr.String(), test.want) || stdout.Len() != 0 {
				t.Fatal(result, stdout.String(), stderr.String())
			}
		})
	}

	//nolint:gosec // Read the synthetic archive created by this test.
	data, err := os.ReadFile(existing)
	if err != nil || string(data) != "keep" {
		t.Fatal("existing output changed", err)
	}
}
