// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.trippwill.dev/wake/internal/config"
)

func setupDaemonFixture(t *testing.T) (config.Config, string, []byte) {
	t.Helper()

	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(root, "runtime"))

	cfg, err := config.Load(config.Overrides{})
	if err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(cfg.Paths.ConfigDir, "config.toml")
	content := fmt.Appendf(
		nil,
		"[storage]\nwarehouse_path=%q\nconnections_path=%q\n[paths]\nruntime_dir=%q\n",
		cfg.Storage.WarehousePath, cfg.Storage.ConnectionsPath,
		cfg.Paths.RuntimeDir,
	)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}

	return cfg, path, content
}

func assertSetupNotProvisioned(t *testing.T, cfg config.Config, path string, original []byte) {
	t.Helper()

	// #nosec G304 -- Synthetic configuration within this test's private directory.
	current, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(current, original) {
		t.Fatal("detected daemon changed configuration", err)
	}

	for _, name := range []string{"master.key", "admin-unlock", "admin-session.key"} {
		if _, err := os.Lstat(filepath.Join(cfg.Paths.ConfigDir, name)); !os.IsNotExist(err) {
			t.Fatal("detected daemon provisioned local access", name, err)
		}
	}
}

func TestSetupDetectsRunningDaemonBeforeSettingsPrompts(t *testing.T) {
	cfg, path, original := setupDaemonFixture(t)
	stop, _ := startShellDaemon(t, cfg)

	for _, jsonMode := range []bool{false, true} {
		args := []string{"setup", "--config", path}
		if jsonMode {
			args = append(args, "--json")
		}

		var stdout, stderr bytes.Buffer
		if code := run(args, &stdout, &stderr); code != exitFailure || stdout.Len() != 0 ||
			!strings.Contains(stderr.String(), "Wake is running or its paths are in use") ||
			!strings.Contains(stderr.String(), "stop it and rerun setup") ||
			strings.Contains(stderr.String(), "Stop Wake before setup.") {
			t.Fatal("running daemon was not detected", code, stdout.String(), stderr.String())
		}

		if jsonMode {
			decoder := json.NewDecoder(bytes.NewReader(stderr.Bytes()))
			sawError := false
			for decoder.More() {
				var event struct {
					Event string `json:"event"`
					Code  int    `json:"exit_status"`
				}
				if err := decoder.Decode(&event); err != nil {
					t.Fatal("invalid JSON status", err)
				}

				if event.Event == "input_required" {
					t.Fatal("running daemon reached a prompt")
				}

				if event.Event == "error" && event.Code == exitFailure {
					sawError = true
				}
			}

			if !sawError {
				t.Fatal("running daemon did not emit a structured error")
			}
		}
	}

	assertSetupNotProvisioned(t, cfg, path, original)
	stop()

	if err := checkSetupStopped(
		config.SetupGuidance{Storage: cfg.Storage, RuntimeDir: cfg.Paths.RuntimeDir},
	); err != nil {
		t.Fatal("stopped daemon still detected", err)
	}
}

func TestSetupDetectsEnvironmentLaunchPathsWithoutPersistingThem(t *testing.T) {
	cfg, path, original := setupDaemonFixture(t)
	_, _ = startShellDaemon(t, cfg)
	t.Setenv("WAKE_WAREHOUSE_PATH", cfg.Storage.WarehousePath)
	t.Setenv("WAKE_RUNTIME_DIR", cfg.Paths.RuntimeDir)

	guide := config.SetupGuidance{
		Storage: config.Storage{WarehousePath: filepath.Join(t.TempDir(), "new-data", "history.sqlite")},
	}
	if err := checkSetupStopped(guide); err == nil || !strings.Contains(err.Error(), "Wake is running") {
		t.Fatal("environment-selected running daemon not detected", err)
	}

	assertSetupNotProvisioned(t, cfg, path, original)
}

func TestSetupRechecksDaemonAfterTerminalInput(t *testing.T) {
	for _, stage := range []string{"paths", "proposed", "unlock"} {
		t.Run(stage, func(t *testing.T) {
			cfg, path, originalConfig := setupDaemonFixture(t)
			proposed := cfg
			if stage == "proposed" {
				proposed.Storage.WarehousePath = filepath.Join(t.TempDir(), "new-data", "history.sqlite")

				proposed.Storage.ConnectionsPath = filepath.Join(t.TempDir(), "new-access", "credentials.sqlite")
				proposed.Paths.RuntimeDir = filepath.Join(t.TempDir(), "new-runtime")
				if err := config.Prepare(proposed); err != nil {
					t.Fatal(err)
				}

				_, _ = startShellDaemon(t, proposed)
			}

			master, slave := openUnlockTerminal(t)

			originalStdin := os.Stdin
			os.Stdin = slave
			defer func() { os.Stdin = originalStdin }()

			var stdout bytes.Buffer

			stderr := &unlockPrompts{prompt: make(chan struct{}, 8)}
			done := make(chan int, 1)
			go func() { done <- run([]string{"setup", "--config", path}, &stdout, stderr) }()

			defer func() {
				_ = master.Close()

				select {
				case <-done:
				case <-time.After(time.Second):
					t.Error("setup did not stop")
				}
			}()

			inputs := []string{"", "", "", "", "y", "synthetic-unlock", "synthetic-unlock"}
			if stage == "paths" || stage == "proposed" {
				inputs = inputs[:4]
			}

			if stage == "proposed" {
				inputs[0], inputs[1], inputs[2] = proposed.Storage.WarehousePath, proposed.Storage.ConnectionsPath, proposed.Paths.RuntimeDir
			}

			for index, value := range inputs {
				select {
				case <-stderr.prompt:
				case <-time.After(time.Second):
					t.Fatal("setup prompt missing")
				}

				if (stage == "paths" && index == 0) || (stage == "unlock" && index == 6) {
					_, _ = startShellDaemon(t, cfg)
				}

				if _, err := master.Write([]byte(value + "\n")); err != nil {
					t.Fatal(err)
				}
			}

			var code int
			select {
			case code = <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("setup did not detect daemon")
			}

			done <- code

			if code != exitFailure || stdout.Len() != 0 || !strings.Contains(stderr.String(), "Wake is running") {
				t.Fatal("daemon started during prompts was not detected", code, stderr.String())
			}

			assertSetupNotProvisioned(t, cfg, path, originalConfig)
		})
	}
}

func TestSetupDetectsIndependentlyConfiguredConnectionsDatabase(t *testing.T) {
	cfg, path, original := setupDaemonFixture(t)
	cfg.Storage.ConnectionsPath = filepath.Join(t.TempDir(), "access", "credentials.sqlite")
	if err := config.Prepare(cfg); err != nil {
		t.Fatal(err)
	}

	stop, _ := startShellDaemon(t, cfg)
	defer stop()

	guide := config.SetupGuidance{
		Storage: config.Storage{
			WarehousePath:   filepath.Join(t.TempDir(), "unrelated-data", "history.sqlite"),
			ConnectionsPath: cfg.Storage.ConnectionsPath,
		},
	}
	if err := checkSetupStopped(guide); err == nil || !strings.Contains(err.Error(), "Wake is running") {
		t.Fatal("configured credential store ownership ignored", err)
	}

	guide.Storage.ConnectionsPath = ""
	t.Setenv("WAKE_CONNECTIONS_PATH", cfg.Storage.ConnectionsPath)

	if err := checkSetupStopped(guide); err == nil || !strings.Contains(err.Error(), "Wake is running") {
		t.Fatal("environment credential store ownership ignored", err)
	}

	assertSetupNotProvisioned(t, cfg, path, original)
}
