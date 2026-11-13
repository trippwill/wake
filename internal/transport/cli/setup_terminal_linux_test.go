// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.trippwill.dev/wake/internal/config"
	"go.trippwill.dev/wake/internal/credential"
	"go.trippwill.dev/wake/internal/transport/admin"
	"golang.org/x/sys/unix"
)

type unlockPrompts struct {
	bytes.Buffer
	prompt chan struct{}
}

func (output *unlockPrompts) Write(value []byte) (int, error) {
	n, err := output.Buffer.Write(value)
	if strings.HasSuffix(string(value), ": ") {
		output.prompt <- struct{}{}
	}

	return n, err
}

func (output *unlockPrompts) WriteString(value string) (int, error) {
	return output.Write([]byte(value))
}

func TestSetupThroughTerminal(t *testing.T) {
	// Stdin and XDG configuration are process globals; these cases run serially.
	for _, test := range []struct {
		name, first, second string
		want                int
		diagnostic          string
		selection           string
		refresh             string
		wantRefresh         bool
	}{
		{"matching", "synthetic-unlock", "synthetic-unlock", exitOK, "local setup complete", "", "", false},
		{"mismatch", "synthetic-unlock", "different-unlock", exitFailure, "do not match", "", "", false},
		{"weak", "short", "short", exitFailure, "at least 12 bytes", "", "", false},
		{"alternate flag", "synthetic-unlock", "synthetic-unlock", exitOK, "local setup complete", "flag", "true", true},
		{"alternate environment", "synthetic-unlock", "synthetic-unlock", exitOK, "local setup complete", "environment", "", false},
		{"prompted new path", "synthetic-unlock", "synthetic-unlock", exitOK, "local setup complete", "prompt", "", false},
		{"overwrite accepted", "synthetic-unlock", "synthetic-unlock", exitOK, "local setup complete", "overwrite", "", true},
		{"disable refresh", "synthetic-unlock", "synthetic-unlock", exitOK, "local setup complete", "overwrite", "false", false},
		{"overwrite refused", "", "", exitFailure, "setup canceled", "refuse-overwrite", "", false},
		{"prepared config", "synthetic-unlock", "synthetic-unlock", exitOK, "local setup complete", "prepared", "", true},
		{"prepared config with existing key", "synthetic-unlock", "synthetic-unlock", exitOK, "local setup complete", "reuse-prepared", "", true},
		{"prepared config refused", "", "", exitFailure, "setup canceled", "refuse-prepared", "", false},
		{"prepared config missing", "", "", exitFailure, "requires an existing configuration file", "missing-prepared", "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			t.Setenv("XDG_RUNTIME_DIR", "")
			t.Setenv("XDG_DATA_HOME", t.TempDir())
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			master, slave := openUnlockTerminal(t)

			original := os.Stdin
			os.Stdin = slave
			defer func() { os.Stdin = original }()

			args := []string{"setup"}
			prepared := strings.HasSuffix(test.selection, "prepared")
			if prepared {
				args = append(args, "--use-config")
			}

			configPath := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "wake", "config.toml")
			if test.selection != "" {
				configPath = filepath.Join(t.TempDir(), "private", "new.toml")
				switch test.selection {
				case "flag", "prepared", "reuse-prepared", "refuse-prepared", "missing-prepared":
					args = append(args, "--config", configPath)
				case "environment":
					t.Setenv("WAKE_CONFIG", configPath)
				}
			}

			originalConfig := []byte("[log]\nlevel='debug'\n[admin]\ncurrency_refresh=true\n")
			existing := test.selection != "missing-prepared" &&
				(prepared || strings.HasSuffix(test.selection, "overwrite"))
			if prepared {
				originalConfig = fmt.Appendf(
					originalConfig,
					"[paths]\nruntime_dir=%q\n",
					filepath.Join(t.TempDir(), "run"),
				)
			}

			if test.selection == "reuse-prepared" {
				keyDir, err := config.ConfigDir()
				if err != nil {
					t.Fatal(err)
				}

				keyPath := filepath.Join(keyDir, "master.key")
				if err = credential.CreateMasterKey(keyPath); err != nil {
					t.Fatal(err)
				}

				originalConfig = fmt.Appendf(originalConfig, "[credentials]\nmaster_key_path=%q\n", keyPath)
			}

			if existing {
				// #nosec G703 -- Private parent of a synthetic config selected by this test.
				if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
					t.Fatal(err)
				}

				// #nosec G703 -- Synthetic existing config selected by this test.
				if err := os.WriteFile(configPath, originalConfig, 0o600); err != nil {
					t.Fatal(err)
				}
			}

			var stdout bytes.Buffer

			stderr := &unlockPrompts{prompt: make(chan struct{}, 8)}
			done := make(chan int, 1)
			go func() { done <- run(args, &stdout, stderr) }()
			// Closing the pseudo-terminal on a failed assertion releases ReadPassword;
			// join it before restoring Stdin or removing the test's directories.
			defer func() {
				_ = master.Close()

				select {
				case <-done:
				case <-time.After(time.Second):
					t.Error("terminal command did not finish")
				}
			}()

			confirmation := "y"
			if strings.HasPrefix(test.selection, "refuse-") {
				confirmation = "n"
			}

			inputs := []string{"", "", filepath.Join(t.TempDir(), "runtime"), "", test.refresh, confirmation}
			if confirmation == "y" {
				inputs = append(inputs, test.first, test.second)
			}

			switch {
			case test.selection == "missing-prepared":
				inputs = nil
			case prepared:
				inputs = inputs[5:]
			case test.selection != "flag" && test.selection != "environment":
				destination := ""
				if test.selection != "" {
					destination = configPath
				}

				inputs = append([]string{destination}, inputs...)
			}

			for _, value := range inputs {
				select {
				case <-stderr.prompt:
				case <-time.After(time.Second):
					t.Fatal("terminal unlock prompt missing")
				}

				if _, err := master.Write([]byte(value + "\n")); err != nil {
					t.Fatal(err)
				}
			}

			var code int
			select {
			case code = <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("terminal command did not return")
			}
			// Leave the completion available for deferred cleanup.
			done <- code

			if code != test.want || !strings.Contains(stdout.String()+stderr.String(), test.diagnostic) {
				t.Fatalf("code = %d, stdout = %q, stderr = %q", code, stdout.String(), stderr.String())
			}

			if prepared && existing &&
				!strings.Contains(stderr.String(), "Provision local access using "+configPath) {
				t.Fatal("prepared setup did not confirm provisioning", stderr.String())
			}

			if !prepared && existing &&
				!strings.Contains(stderr.String(), "Overwrite existing configuration at "+configPath) {
				t.Fatal("overwrite not explicitly confirmed", stderr.String())
			}

			if !prepared && !existing &&
				!strings.Contains(stderr.String(), "Save new configuration to "+configPath) {
				t.Fatal("save not explicitly confirmed", stderr.String())
			}

			if strings.HasPrefix(test.selection, "refuse-") || test.selection == "reuse-prepared" {
				// #nosec G304,G703 -- Synthetic existing config selected by this terminal case.
				current, err := os.ReadFile(configPath)
				if err != nil || !bytes.Equal(current, originalConfig) {
					t.Fatal("setup changed the existing configuration", err)
				}
			}

			if test.first != "" && strings.Contains(stdout.String()+stderr.String(), test.first) {
				t.Fatal("terminal unlock was echoed into output")
			}

			auth, enabled, err := admin.Open(
				filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "wake"),
				"local",
				"http://127.0.0.1:8787",
			)
			if err != nil {
				t.Fatal(err)
			}

			if enabled {
				defer auth.Close()
			}

			if enabled != (test.want == exitOK) {
				t.Fatalf("administrator enabled = %t", enabled)
			}

			configDir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "wake")
			if test.want == exitOK {
				saved, err := config.ReadLocalSetup(configPath)
				if err != nil {
					t.Fatal(err)
				}

				guide, err := saved.Guidance()
				if err != nil || guide.CurrencyRefresh != test.wantRefresh ||
					!strings.Contains(stderr.String(), fmt.Sprintf("currency list refresh: %t", test.wantRefresh)) {
					t.Fatal(
						"setup summary or saved currency refresh did not match the choice",
						guide.CurrencyRefresh,
						err,
					)
				}

				t.Setenv("XDG_DATA_HOME", t.TempDir())
				t.Setenv("XDG_STATE_HOME", t.TempDir())
				t.Setenv("XDG_RUNTIME_DIR", t.TempDir())

				resolved, err := launchForTest(configPath, true)
				cfg := resolved.Config

				if err != nil {
					t.Fatal(err)
				}

				key, err := credential.LoadMasterKey(cfg.Credentials.MasterKeyPath)
				if err != nil {
					t.Fatal(err)
				}

				key.Clear()

				for name, mode := range map[string]os.FileMode{
					"master.key": 0o400, "config.toml": 0o600,
					"admin-unlock": 0o600, "admin-session.key": 0o600,
				} {
					path := filepath.Join(configDir, name)
					if name == "config.toml" {
						path = configPath
					}
					//nolint:gosec // Fixed filenames and selected config under synthetic temporary directories.
					info, err := os.Stat(path)
					if err != nil {
						t.Fatal(err)
					}

					if info.Mode().Perm() != mode {
						t.Errorf("%s mode = %o", name, info.Mode().Perm())
					}
				}
			} else {
				entries, err := os.ReadDir(configDir)
				if err != nil {
					t.Fatal(err)
				}

				if len(entries) != 0 {
					t.Fatal("failed prompt persisted setup material")
				}
			}
		})
	}
}

func openUnlockTerminal(t *testing.T) (*os.File, *os.File) {
	t.Helper()

	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = master.Close() })

	if err = unix.IoctlSetPointerInt(int(master.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		t.Fatal(err)
	}

	number, err := unix.IoctlGetInt(int(master.Fd()), unix.TIOCGPTN)
	if err != nil {
		t.Fatal(err)
	}
	//nolint:gosec // The kernel supplies the terminal number, never user input.
	slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", number), os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = slave.Close() })

	return master, slave
}
