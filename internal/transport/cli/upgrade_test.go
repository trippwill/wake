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

	"go.trippwill.dev/wake/internal/config"
	"go.trippwill.dev/wake/internal/credential"
	"go.trippwill.dev/wake/internal/daemon"
)

func TestBackupCLIArchivesStoppedPair(t *testing.T) {
	cfg, path, _ := setupDaemonFixture(t)
	args := []string{"--json", "backup", "--config", path}

	var out, stderr bytes.Buffer
	if status := Run(args, &out, &stderr); status != exitFailure ||
		!strings.Contains(stderr.String(), "matching warehouse and access store") {
		t.Fatal(status, out.String(), stderr.String())
	}

	if _, err := os.Stat(cfg.Storage.WarehousePath); !os.IsNotExist(err) {
		t.Fatal("backup created missing database", err)
	}

	stop, _ := startShellDaemon(t, cfg)
	defer stop()

	out.Reset()
	stderr.Reset()

	if status := Run(args, &out, &stderr); status != exitFailure ||
		!strings.Contains(stderr.String(), "unsafe or already owned") {
		t.Fatal(status, out.String(), stderr.String())
	}

	stop()
	out.Reset()
	stderr.Reset()

	if status := Run(args, &out, &stderr); status != exitOK {
		t.Fatal(status, out.String(), stderr.String())
	}

	var result struct {
		BackupPath string `json:"backup_path"`
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil ||
		!strings.HasPrefix(result.BackupPath, cfg.Storage.WarehousePath+".backup-") {
		t.Fatal(out.String(), err)
	}

	if file, err := os.Stat(result.BackupPath); err != nil || file.Mode().Perm() != 0o600 {
		t.Fatal("backup archive missing or permissions changed", file, err)
	}

	out.Reset()
	stderr.Reset()

	if status := Run(args[1:], &out, &stderr); status != exitOK ||
		!strings.Contains(
			out.String(),
			"Backup written to "+cfg.Storage.WarehousePath+".backup-",
		) || strings.Contains(out.String(), result.BackupPath) {
		t.Fatal(status, out.String(), stderr.String())
	}
}

func TestBackupCLIRejectsArgumentsAndMissingConfig(t *testing.T) {
	for _, args := range [][]string{
		{"backup", "unexpected"},
		{"backup", "--config", filepath.Join(t.TempDir(), "missing.toml")},
	} {
		var out, stderr bytes.Buffer
		if status := Run(args, &out, &stderr); status == exitOK || out.Len() != 0 || stderr.Len() == 0 {
			t.Fatal(args, status, out.String(), stderr.String())
		}
	}
}

func TestUpgradeCLIPreviewCreatesNoFilesAndRejectsStaleConfirmation(t *testing.T) {
	dir := t.TempDir()
	warehouse := filepath.Join(dir, "missing", "warehouse.db")
	access := filepath.Join(dir, "missing", "access.db")

	args := []string{"--json", "upgrade", "--warehouse-path", warehouse, "--connections-path", access}

	var stdout, stderr bytes.Buffer
	if status := Run(args, &stdout, &stderr); status != 0 {
		t.Fatal(status, stderr.String())
	}

	var plan daemon.UpgradePlan
	if err := json.Unmarshal(stdout.Bytes(), &plan); err != nil || plan.ID != "" ||
		plan.Stores[0].Target != "wake-warehouse/1" || plan.Stores[1].Target != "wake-access/1" {
		t.Fatal(stdout.String(), err)
	}

	if _, err := os.Stat(filepath.Dir(warehouse)); !os.IsNotExist(err) {
		t.Fatal("preview prepared directories", err)
	}

	cfg, path, _ := setupDaemonFixture(t)

	stdout.Reset()
	stderr.Reset()

	if status := Run(
		[]string{"upgrade", "--config", path, "--confirm", "stale"},
		&stdout,
		&stderr,
	); status != exitFailure ||
		!strings.Contains(stderr.String(), "plan changed") {
		t.Fatal(status, stdout.String(), stderr.String())
	}

	if _, err := os.Stat(cfg.Storage.WarehousePath); !os.IsNotExist(err) {
		t.Fatal("stale confirmation created a warehouse", err)
	}
}

func TestUpgradeConfirmationCommandQuotesExplicitProfile(t *testing.T) {
	resolved := config.Resolution{
		ConfigPath:   "/tmp/Wake's config.toml",
		ConfigSource: "flag",
		Values: []config.Value{
			{Key: "storage.warehouse_path", Value: "/tmp/wake store.db", Source: "flag"},
		},
	}

	got := upgradeConfirmationCommand(resolved, "abc")
	want := `wake upgrade --config '/tmp/Wake'"'"'s config.toml' --warehouse-path='/tmp/wake store.db' --confirm abc`
	if got != want {
		t.Fatal(got)
	}
}

func TestUpgradeOutputIncludesApprovedPlanAndReleaseNotes(t *testing.T) {
	plan := daemon.UpgradePlan{
		ID: "fixture-plan", BackupPath: "/tmp/wake.upgrade-backup.tar",
		TargetRule: "retained-claims-and-decisions/2", Rebuild: "required",
		AffectedViews: []string{"activity", "positions"},
	}
	if err := json.Unmarshal(
		[]byte(
			`{"steps":[{"store":"warehouse","from":"wake-warehouse/1","to":"wake-warehouse/2"}],"release_notes":[{"version":2,"text":"Recalculate retained selections."}]}`,
		),
		&plan,
	); err != nil {
		t.Fatal(err)
	}

	for _, fixture := range []struct {
		name, expected                string
		recovery, approved, confirmed bool
	}{
		{name: "preview", expected: "wake upgrade --confirm fixture-plan"},
		{name: "interrupted", recovery: true, expected: "Start the matching Wake build to recover it."},
		{name: "approved", approved: true, expected: "Start Wake to resume the approved rebuild."},
		{name: "confirmed", confirmed: true, expected: "Upgrade applied."},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			preview := plan
			preview.RecoveryPending = fixture.recovery
			if fixture.approved {
				preview.ID = ""
				preview.Rebuild = "approved"
			}

			var out bytes.Buffer
			if err := writeUpgradeResult(&out, false, fixture.confirmed, config.Resolution{}, preview); err != nil {
				t.Fatal(err)
			}

			for _, want := range []string{fixture.expected, preview.BackupPath} {
				if !strings.Contains(out.String(), want) {
					t.Fatal(out.String())
				}
			}

			if !fixture.confirmed && !strings.Contains(out.String(), "Recalculate retained selections.") {
				t.Fatal(out.String())
			}

			out.Reset()

			if err := writeUpgradeResult(&out, true, fixture.confirmed, config.Resolution{}, preview); err != nil {
				t.Fatal(err)
			}

			var result struct {
				Confirmed bool `json:"confirmed"`
				daemon.UpgradePlan
			}
			if err := json.Unmarshal(out.Bytes(), &result); err != nil || result.Confirmed != fixture.confirmed ||
				result.BackupPath != preview.BackupPath || len(result.ReleaseNotes) != 1 {
				t.Fatal(out.String(), err)
			}
		})
	}
}

func TestUpgradeCLIRejectsInvalidUsageAndMissingConfig(t *testing.T) {
	for _, args := range [][]string{
		{"upgrade", "unexpected"},
		{"upgrade", "--confirm="},
		{"upgrade", "--config", filepath.Join(t.TempDir(), "missing.toml")},
	} {
		var stdout, stderr bytes.Buffer
		if status := Run(args, &stdout, &stderr); status == exitOK || stdout.Len() != 0 || stderr.Len() == 0 {
			t.Fatal(args, status, stdout.String(), stderr.String())
		}
	}
}

func TestUpgradeConfirmationChecksConfiguredKey(t *testing.T) {
	cfg, path, _ := setupDaemonFixture(t)
	keyPath := filepath.Join(cfg.Paths.ConfigDir, "master.key")
	args := []string{"upgrade", "--config", path, "--master-key-path", keyPath}

	var stdout, stderr bytes.Buffer
	if status := Run(args, &stdout, &stderr); status != exitOK {
		t.Fatal("preview required a key", status, stderr.String())
	}

	args = append(args, "--confirm", "stale")
	for _, want := range []string{"inspect master key file", "plan changed"} {
		stdout.Reset()
		stderr.Reset()

		if status := Run(
			args,
			&stdout,
			&stderr,
		); status != exitFailure ||
			!strings.Contains(stderr.String(), want) {
			t.Fatal(status, stdout.String(), stderr.String())
		}

		if _, err := os.Stat(cfg.Storage.WarehousePath); !os.IsNotExist(err) {
			t.Fatal("confirmation refusal created a warehouse", err)
		}

		if want == "inspect master key file" {
			if err := credential.CreateMasterKey(keyPath); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestRestoreCLIPreviewConfirmAndRestart(t *testing.T) {
	cfg, configPath, _ := setupDaemonFixture(t)
	stop, _ := startShellDaemon(t, cfg)
	stop()

	var out, stderr bytes.Buffer
	if status := Run([]string{"--json", "backup", "--config", configPath}, &out, &stderr); status != exitOK {
		t.Fatal(status, stderr.String())
	}

	var backup struct {
		Path string `json:"backup_path"`
	}
	if err := json.Unmarshal(out.Bytes(), &backup); err != nil {
		t.Fatal(err)
	}

	original := filepath.Join(filepath.Dir(cfg.Storage.WarehousePath), "restored.db")
	access := filepath.Join(filepath.Dir(cfg.Storage.ConnectionsPath), "restored-access.db")
	args := []string{
		"--json", "restore", "--config", configPath, "--archive", backup.Path,
		"--warehouse-path", original, "--connections-path", access,
	}

	out.Reset()
	stderr.Reset()

	if status := Run(args, &out, &stderr); status != exitOK {
		t.Fatal(status, stderr.String())
	}

	var plan daemon.RestorePlan
	if err := json.Unmarshal(out.Bytes(), &plan); err != nil || plan.ID == "" || len(plan.Displaced) != 0 {
		t.Fatal(plan, err)
	}

	for _, path := range []string{original, access, original + ".wake-lock", access + ".wake-lock"} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatal("preview changed destination", path, err)
		}
	}

	out.Reset()
	stderr.Reset()

	if status := Run(append(args[1:], "--confirm", plan.ID), &out, &stderr); status != exitOK ||
		!strings.Contains(out.String(), "Restore complete") {
		t.Fatal(status, out.String(), stderr.String())
	}

	cfg.Storage.WarehousePath, cfg.Storage.ConnectionsPath = original, access
	stop, _ = startShellDaemon(t, cfg)
	stop()
}

func TestRestoreCLIRefusesInvalidArguments(t *testing.T) {
	for _, args := range [][]string{
		{"restore", "unexpected"},
		{"restore"},
		{"restore", "--archive", "/tmp/unused.tar", "--confirm="},
		{"restore", "--archive", "/tmp/unused.tar", "--config", filepath.Join(t.TempDir(), "missing.toml")},
	} {
		var out, stderr bytes.Buffer
		if status := Run(args, &out, &stderr); status == exitOK || out.Len() != 0 || stderr.Len() == 0 {
			t.Fatal(args, status, out.String(), stderr.String())
		}
	}
}

func TestRestoreProtectsConfiguredKeyAliases(t *testing.T) {
	cfg, configPath, _ := setupDaemonFixture(t)
	stop, _ := startShellDaemon(t, cfg)
	stop()

	var out, stderr bytes.Buffer
	if status := Run([]string{"--json", "backup", "--config", configPath}, &out, &stderr); status != exitOK {
		t.Fatal(status, stderr.String())
	}

	var backup struct {
		Path string `json:"backup_path"`
	}
	if err := json.Unmarshal(out.Bytes(), &backup); err != nil {
		t.Fatal(err)
	}

	dir := filepath.Dir(cfg.Storage.WarehousePath)

	key := filepath.Join(dir, "vault.key")
	original := bytes.Repeat([]byte{0x42}, 32)
	if err := os.WriteFile(key, original, 0o600); err != nil {
		t.Fatal(err)
	}

	alias := filepath.Join(cfg.Paths.ConfigDir, "vault-link.key")
	if err := os.Symlink(key, alias); err != nil {
		t.Fatal(err)
	}

	args := []string{
		"--json",
		"restore",
		"--config",
		configPath,
		"--archive",
		backup.Path,
		"--warehouse-path",
		key,
	}

	out.Reset()
	stderr.Reset()

	if status := Run(args, &out, &stderr); status != exitOK {
		t.Fatal(status, stderr.String())
	}

	var plan daemon.RestorePlan
	if err := json.Unmarshal(out.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}

	for _, configured := range []string{dir + "/./vault.key", alias} {
		out.Reset()
		stderr.Reset()

		attempt := append(append([]string(nil), args...), "--master-key-path", configured, "--confirm", plan.ID)
		if status := Run(attempt, &out, &stderr); status != exitFailure ||
			!strings.Contains(stderr.String(), "unsafe or already owned store path") {
			t.Fatal(status, out.String(), stderr.String())
		}
		//nolint:gosec // Verify the contents of the disposable test key.
		got, err := os.ReadFile(key)
		if err != nil || !bytes.Equal(got, original) {
			t.Fatal("restore changed the key", err)
		}
	}
}
