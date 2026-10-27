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
	"syscall"
	"testing"

	"go.trippwill.dev/wake/internal/config"
	"go.trippwill.dev/wake/internal/daemon"
)

var resetSuffixes = []string{"", "-journal", "-wal", "-shm", "-mjABCDEF912"}

func resetFixture(t *testing.T) (config.Config, string, map[string][]byte) {
	t.Helper()
	cfg, path, _ := setupDaemonFixture(t)
	cfg.Storage.WarehousePath = filepath.Join(filepath.Dir(cfg.Storage.WarehousePath), "history.sqlite")
	cfg.Storage.ConnectionsPath = filepath.Join(filepath.Dir(cfg.Storage.ConnectionsPath), "credentials.sqlite")
	cfg.Storage.OperationalPath = filepath.Join(filepath.Dir(cfg.Storage.OperationalPath), "execution.sqlite")
	cfg.Credentials.MasterKeyPath = filepath.Join(cfg.Paths.ConfigDir, "external-invalid.key")

	original := fmt.Appendf(
		nil,
		"[storage]\nwarehouse_path=%q\nconnections_path=%q\noperational_path=%q\n[paths]\nruntime_dir=%q\n[credentials]\nmaster_key_path=%q\n",
		cfg.Storage.WarehousePath,
		cfg.Storage.ConnectionsPath,
		cfg.Storage.OperationalPath,
		cfg.Paths.RuntimeDir,
		cfg.Credentials.MasterKeyPath,
	)
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}

	files := map[string][]byte{
		path:                          original,
		cfg.Credentials.MasterKeyPath: []byte("invalid key material must not be loaded"),
	}
	for _, name := range []string{"master.key", "admin-unlock", "admin-session.key", "admin-proxy.key", "unrelated.db"} {
		files[filepath.Join(cfg.Paths.ConfigDir, name)] = []byte("keep " + name)
	}

	for _, db := range []string{cfg.Storage.WarehousePath, cfg.Storage.ConnectionsPath, cfg.Storage.OperationalPath} {
		for _, suffix := range resetSuffixes {
			files[db+suffix] = []byte("invalid SQLite content: " + filepath.Base(db) + suffix)
		}

		files[db+"-mjnotes"] = []byte("unrelated file")
	}

	for file, content := range files {
		if err := os.WriteFile(file, content, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	return cfg, path, files
}

//nolint:gosec // Only synthetic files under this test's private directories.
func assertResetFiles(t *testing.T, files map[string][]byte, removed map[string]bool) {
	t.Helper()

	for path, before := range files {
		after, err := os.ReadFile(path)
		if removed[path] {
			if !os.IsNotExist(err) {
				t.Fatal("selected file retained", path, err)
			}

			continue
		}

		if err != nil || !bytes.Equal(before, after) {
			t.Fatal("unselected or previewed file changed", path, err)
		}
	}
}

func TestResetDryRunAndConfirmedTargetsIgnoreDatabaseValidity(t *testing.T) {
	for _, target := range []string{"warehouse", "credentials"} {
		t.Run(target, func(t *testing.T) {
			cfg, path, files := resetFixture(t)

			var stdout, stderr bytes.Buffer

			args := []string{"--json", "reset", target, "--config", path}
			for _, dryArgs := range [][]string{args, append(append([]string{}, args...), "--confirm=false")} {
				stdout.Reset()
				stderr.Reset()

				if code := run(dryArgs, &stdout, &stderr); code != exitOK {
					t.Fatal(code, stderr.String())
				}

				var result daemon.ResetResult
				if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
					t.Fatal(err)
				}

				count := 5
				if target == "warehouse" {
					count = 10
				}

				if !result.DryRun || string(result.Target) != target || len(result.Files) != count ||
					len(result.Removed) != 0 {
					t.Fatal("wrong preview", result)
				}

				assertResetFiles(t, files, nil)

				if _, err := os.Stat(filepath.Join(cfg.Paths.RuntimeDir, "wake.sock.lock")); !os.IsNotExist(err) {
					t.Fatal("dry run created ownership file", err)
				}
			}

			stdout.Reset()
			stderr.Reset()

			if code := run(append(args, "--confirm"), &stdout, &stderr); code != exitOK {
				t.Fatal(code, stderr.String())
			}

			var result daemon.ResetResult
			if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
				t.Fatal(err)
			}

			if result.DryRun || len(result.Removed) != len(result.Files) {
				t.Fatal("reset not executed", result)
			}

			removed := map[string]bool{}
			for _, db := range []struct{ name, path string }{{"warehouse", cfg.Storage.WarehousePath}, {"credentials", cfg.Storage.ConnectionsPath}, {"operational", cfg.Storage.OperationalPath}} {
				if (target == "credentials" && db.name != "credentials") ||
					(target == "warehouse" && db.name == "credentials") {
					continue
				}

				for _, suffix := range resetSuffixes {
					removed[db.path+suffix] = true
				}
			}

			for _, file := range result.Removed {
				if !removed[file] {
					t.Fatal("removed outside selection", file)
				}
			}

			assertResetFiles(t, files, removed)
			stdout.Reset()
			stderr.Reset()

			if code := run(append(args, "--confirm"), &stdout, &stderr); code != exitOK {
				t.Fatal("repeated reset failed", code, stderr.String())
			}
		})
	}
}

func TestResetRefusesAnyOwnedStoreBeforeDeletingAnything(t *testing.T) {
	for _, locked := range []string{"warehouse", "connections", "operational"} {
		t.Run(locked, func(t *testing.T) {
			cfg, path, files := resetFixture(t)
			database := map[string]string{"warehouse": cfg.Storage.WarehousePath, "connections": cfg.Storage.ConnectionsPath, "operational": cfg.Storage.OperationalPath}[locked]
			//nolint:gosec // Synthetic database selected from the fixture above.
			owner, err := os.OpenFile(database, os.O_RDWR, 0)
			if err != nil {
				t.Fatal(err)
			}

			if err = syscall.Flock(int(owner.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
				_ = owner.Close()

				t.Fatal(err)
			}

			for _, target := range []string{"warehouse", "credentials"} {
				var stdout, stderr bytes.Buffer
				if code := run(
					[]string{"reset", target, "--confirm", "--config", path},
					&stdout,
					&stderr,
				); code != exitFailure ||
					!strings.Contains(stderr.String(), "owns the Wake database") {
					t.Fatal("owner not detected", code, stderr.String())
				}

				if stdout.Len() != 0 {
					t.Fatal("failed preflight emitted a completed result", stdout.String())
				}

				assertResetFiles(t, files, nil)
			}

			if err = owner.Close(); err != nil {
				t.Fatal(err)
			}

			var stdout, stderr bytes.Buffer
			if code := run([]string{"reset", "warehouse", "--config", path}, &stdout, &stderr); code != exitOK {
				t.Fatal("failed reset leaked a lock", code, stderr.String())
			}
		})
	}
}

func TestResetRefusesLinksAndProtectedFilesBeforeDeletion(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink", "sidecar", "superjournal", "configuration", "master-key", "browser-key"} {
		t.Run(kind, func(t *testing.T) {
			cfg, path, files := resetFixture(t)
			args := []string{"reset", "warehouse", "--confirm", "--config", path}
			switch kind {
			case "symlink", "hardlink":
				alias := filepath.Join(cfg.Paths.ConfigDir, "alias")
				if kind == "hardlink" {
					if err := os.Link(cfg.Storage.OperationalPath, alias); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := os.Rename(cfg.Storage.OperationalPath, alias); err != nil {
						t.Fatal(err)
					}

					if err := os.Symlink(alias, cfg.Storage.OperationalPath); err != nil {
						t.Fatal(err)
					}
				}
			case "sidecar", "superjournal":
				sidecar := cfg.Storage.OperationalPath + "-wal"
				if kind == "superjournal" {
					sidecar = cfg.Storage.OperationalPath + "-mjABCDEF912"
				}

				if err := os.Remove(sidecar); err != nil {
					t.Fatal(err)
				}

				if err := os.Symlink(cfg.Storage.WarehousePath, sidecar); err != nil {
					t.Fatal(err)
				}
				// Readback follows this synthetic link only to verify no selected data was deleted.
				files[sidecar] = files[cfg.Storage.WarehousePath]
			case "configuration":
				args[1] = "credentials"
				args = append(args, "--connections-path", path)
			case "master-key":
				args[1] = "credentials"
				args = append(args, "--connections-path", cfg.Credentials.MasterKeyPath)
			case "browser-key":
				args[1] = "credentials"
				args = append(args, "--connections-path", filepath.Join(cfg.Paths.ConfigDir, "admin-session.key"))
			}

			var stdout, stderr bytes.Buffer
			if code := run(args, &stdout, &stderr); code != exitFailure {
				t.Fatal("unsafe reset accepted", code, stderr.String())
			}

			assertResetFiles(t, files, nil)
		})
	}
}

func TestResetMissingPathsNeedsNoRuntimeOrKeysAndCreatesNothing(t *testing.T) {
	_, path, files := resetFixture(t)
	missing := filepath.Join(t.TempDir(), "missing")
	args := []string{
		"--json",
		"reset",
		"warehouse",
		"--config",
		path,
		"--warehouse-path",
		filepath.Join(missing, "warehouse", "history.sqlite"),
		"--connections-path",
		filepath.Join(missing, "access", "credentials.sqlite"),
		"--operational-path",
		filepath.Join(missing, "state", "execution.sqlite"),
		"--master-key-path",
		filepath.Join(missing, "key"),
		"--runtime-dir",
		"",
	}
	for _, confirm := range []bool{false, true} {
		current := append([]string{}, args...)
		if confirm {
			current = append(current, "--confirm")
		}

		var stdout, stderr bytes.Buffer
		if code := run(current, &stdout, &stderr); code != exitOK {
			t.Fatal(code, stderr.String())
		}

		var result daemon.ResetResult
		if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
			t.Fatal(err)
		}

		if len(result.Files) != 0 || len(result.Removed) != 0 || len(result.Databases) != 2 {
			t.Fatal(result)
		}

		if _, err := os.Stat(missing); !os.IsNotExist(err) {
			t.Fatal("reset created missing paths", err)
		}

		assertResetFiles(t, files, nil)
	}
}

func TestResetUsageAndHumanPreview(t *testing.T) {
	_, path, files := resetFixture(t)
	for _, args := range [][]string{{"reset"}, {"reset", "--confirm"}, {"reset", "invalid"}, {"reset", "all"}, {"reset", "connections"}, {"reset", "operational"}, {"reset", "warehouse", "warehouse"}} {
		var stdout, stderr bytes.Buffer
		if code := run(args, &stdout, &stderr); code != exitUsage {
			t.Fatal("invalid selection accepted", code, stderr.String())
		}

		assertResetFiles(t, files, nil)
	}

	var stdout, stderr bytes.Buffer
	if code := run([]string{"reset", "warehouse", "--config", path}, &stdout, &stderr); code != exitOK {
		t.Fatal(code, stderr.String())
	}

	if !strings.Contains(stdout.String(), "Dry run") || !strings.Contains(stdout.String(), "--confirm") ||
		!strings.Contains(stdout.String(), "Credentials are retained") {
		t.Fatal(stdout.String())
	}

	assertResetFiles(t, files, nil)
}

func TestResetRefusesRunningDaemon(t *testing.T) {
	_, path, _ := setupDaemonFixture(t)

	resolved, err := config.Resolve(config.Overrides{ConfigPath: path, ConfigPathExplicit: true})
	if err != nil {
		t.Fatal(err)
	}

	stop, _ := startShellDaemon(t, resolved.Config)
	defer stop()

	var stdout, stderr bytes.Buffer
	if code := run(
		[]string{"reset", "warehouse", "--confirm", "--config", path},
		&stdout,
		&stderr,
	); code != exitFailure ||
		!strings.Contains(stderr.String(), "owns the Wake database") {
		t.Fatal("running daemon reset", code, stderr.String())
	}
}

func TestResetRemovesOrphanSidecarsWithoutCreatingDatabase(t *testing.T) {
	cfg, path, files := resetFixture(t)
	if err := os.Remove(cfg.Storage.ConnectionsPath); err != nil {
		t.Fatal(err)
	}

	delete(files, cfg.Storage.ConnectionsPath)

	var stdout, stderr bytes.Buffer
	if code := run(
		[]string{"--json", "reset", "credentials", "--confirm", "--config", path},
		&stdout,
		&stderr,
	); code != exitOK {
		t.Fatal(code, stderr.String())
	}

	var result daemon.ResetResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}

	if result.Databases[0].Exists || len(result.Removed) != 4 {
		t.Fatal(result)
	}

	removed := map[string]bool{}
	for _, file := range result.Removed {
		removed[file] = true
	}

	assertResetFiles(t, files, removed)

	if _, err := os.Stat(cfg.Storage.ConnectionsPath); !os.IsNotExist(err) {
		t.Fatal("reset created a missing database", err)
	}
}

func TestResetReportsPartialRemovalAndRefusesChangedFile(t *testing.T) {
	cfg, path, files := resetFixture(t)
	journal := cfg.Storage.WarehousePath + "-journal"
	backup := journal + ".saved"
	replacement := []byte("replacement file must survive")

	result, err := daemon.ResetStorage(t.Context(), cfg, path, daemon.ResetWarehouse, true, func(phase string) {
		if phase != "removing selected databases and sidecars" {
			return
		}

		if err := os.Rename(journal, backup); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(journal, replacement, 0o600); err != nil {
			t.Fatal(err)
		}
	})
	if err == nil || len(result.Removed) != 1 || result.Removed[0] != cfg.Storage.WarehousePath+"-mjABCDEF912" {
		t.Fatal("partial reset not reported", result, err)
	}

	files[backup] = files[journal]
	files[journal] = replacement
	assertResetFiles(t, files, map[string]bool{result.Removed[0]: true})
}
