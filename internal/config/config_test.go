// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package config

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDefaults(t *testing.T) {
	roots := configureEnvironment(t, true)

	cfg, err := loadForTest(Overrides{})
	if err != nil {
		t.Fatalf("loadForTest() error = %v", err)
	}

	assertPath(t, cfg.Paths.ConfigDir, filepath.Join(roots.config, "wake"))
	assertPath(t, cfg.Storage.WarehousePath, filepath.Join(roots.data, "wake", "wake.db"))
	assertPath(t, cfg.Storage.ConnectionsPath, filepath.Join(roots.config, "wake", "connections.db"))
	assertPath(t, cfg.Paths.RuntimeDir, filepath.Join(roots.runtime, "wake"))

	if cfg.Log.Level != slog.LevelInfo {
		t.Errorf("Log.Level = %v, want %v", cfg.Log.Level, slog.LevelInfo)
	}

	if cfg.Admin.Port != 8787 || cfg.Admin.AuthMode != "local" || cfg.Admin.Address() != "127.0.0.1:8787" ||
		cfg.Admin.Origin() != "http://127.0.0.1:8787" {
		t.Errorf("Admin = %#v, address = %q, origin = %q", cfg.Admin, cfg.Admin.Address(), cfg.Admin.Origin())
	}

	for _, path := range []string{cfg.Paths.ConfigDir, filepath.Dir(cfg.Storage.WarehousePath), filepath.Dir(cfg.Storage.ConnectionsPath), cfg.Paths.RuntimeDir} {
		info, statErr := os.Stat(path)
		if statErr != nil {
			t.Errorf("Stat(%q) error = %v", path, statErr)
			continue
		}

		if got := info.Mode().Perm(); got != 0o700 {
			t.Errorf("mode for %q = %#o, want 0700", path, got)
		}
	}
}

func TestLoadFileAndOverrides(t *testing.T) {
	roots := configureEnvironment(t, true)
	fileData := filepath.Join(roots.base, "file-data", "history.sqlite")
	fileRuntime := filepath.Join(roots.base, "file-runtime")
	overrideRuntime := filepath.Join(roots.base, "override-runtime")

	configPath := filepath.Join(roots.base, "custom.toml")
	contents := "[storage]\nwarehouse_path = \"" + fileData + "\"\n[paths]\n" +
		"runtime_dir = \"" + fileRuntime + "\"\n" +
		"[log]\nlevel = \"debug\"\n" +
		"[credentials]\nmaster_key_path = \"" + filepath.Join(roots.base, "master.key") + "\"\n" +
		"[admin]\nport = 8790\nauth_mode = \"proxy\"\npublic_origin = \"https://wake.example.test\"\n"
	if err := os.WriteFile(configPath, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := loadForTest(Overrides{
		ConfigPath:         configPath,
		ConfigPathExplicit: true,

		Values: map[string]string{"runtime-dir": overrideRuntime, "log-level": "warn", "admin-port": "8791"},
	})
	if err != nil {
		t.Fatalf("loadForTest() error = %v", err)
	}

	assertPath(t, cfg.Storage.WarehousePath, fileData)
	assertPath(t, cfg.Paths.RuntimeDir, overrideRuntime)

	if cfg.Log.Level != slog.LevelWarn {
		t.Errorf("Log.Level = %v, want %v", cfg.Log.Level, slog.LevelWarn)
	}

	if cfg.Credentials.MasterKeyPath != filepath.Join(roots.base, "master.key") {
		t.Errorf("Credentials.MasterKeyPath = %q", cfg.Credentials.MasterKeyPath)
	}

	if cfg.Admin.Port != 8791 || cfg.Admin.AuthMode != "proxy" || cfg.Admin.Address() != "127.0.0.1:8791" ||
		cfg.Admin.Origin() != "https://wake.example.test" {
		t.Errorf("Admin = %#v, address = %q, origin = %q", cfg.Admin, cfg.Admin.Address(), cfg.Admin.Origin())
	}
}

func TestLoadRejectsInvalidConfiguration(t *testing.T) {
	tests := []struct {
		name     string
		contents string
		override Overrides
	}{
		{name: "syntax", contents: "[paths\n"},
		{name: "unknown key", contents: "surprise = true\n"},
		{
			name:     "relative warehouse database",
			contents: "[storage]\nwarehouse_path = \"relative\"\n",
		},
		{
			name:     "relative runtime directory",
			contents: "[paths]\nruntime_dir = \"relative\"\n",
		},
		{name: "removed operational setting", contents: "[storage]\noperational_path = \"relative\"\n"},
		{name: "unsupported log level", contents: "[log]\nlevel = \"verbose\"\n"},
		{name: "invalid resume backfills", contents: "[sync]\nresume_backfills = \"false\"\n"},
		{name: "relative master key", contents: "[credentials]\nmaster_key_path = \"master.key\"\n"},
		{name: "zero admin port", contents: "[admin]\nport = 0\n"},
		{name: "invalid admin auth mode", contents: "[admin]\nauth_mode = \"password\"\n"},
		{name: "proxy without origin", contents: "[admin]\nauth_mode = \"proxy\"\n"},
		{
			name:     "proxy with HTTP origin",
			contents: "[admin]\nauth_mode = \"proxy\"\npublic_origin = \"http://wake.example.test\"\n",
		},
		{name: "local with public origin", contents: "[admin]\npublic_origin = \"https://wake.example.test\"\n"},
		{
			name:     "invalid public origin",
			contents: "[admin]\npublic_origin = \"https://wake.example.test/path\"\n",
		},
		{
			name:     "public origin with default port",
			contents: "[admin]\nauth_mode = \"proxy\"\npublic_origin = \"https://wake.example.test:443\"\n",
		},
		{
			name:     "public origin with noncanonical default port",
			contents: "[admin]\nauth_mode = \"proxy\"\npublic_origin = \"https://wake.example.test:0443\"\n",
		},
		{
			name:     "public origin with empty query",
			contents: "[admin]\nauth_mode = \"proxy\"\npublic_origin = \"https://wake.example.test?\"\n",
		},
		{
			name:     "public origin with uppercase host",
			contents: "[admin]\nauth_mode = \"proxy\"\npublic_origin = \"https://WAKE.example.test\"\n",
		},
		{
			name:     "relative explicit config",
			override: Overrides{ConfigPath: "config.toml", ConfigPathExplicit: true},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			roots := configureEnvironment(t, true)
			override := test.override
			if test.contents != "" {
				path := filepath.Join(roots.base, "config.toml")
				if err := os.WriteFile(path, []byte(test.contents), 0o600); err != nil {
					t.Fatal(err)
				}

				override.ConfigPath = path
				override.ConfigPathExplicit = true
			}

			_, err := loadForTest(override)
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("loadForTest() error = %v, want ErrInvalid", err)
			}
		})
	}
}

func TestLoadConfigFilePresence(t *testing.T) {
	t.Run("default is optional", func(t *testing.T) {
		configureEnvironment(t, true)

		if _, err := loadForTest(Overrides{}); err != nil {
			t.Fatalf("loadForTest() error = %v", err)
		}
	})

	t.Run("explicit is required", func(t *testing.T) {
		roots := configureEnvironment(t, true)
		_, err := loadForTest(Overrides{
			ConfigPath:         filepath.Join(roots.base, "missing.toml"),
			ConfigPathExplicit: true,
		})
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("loadForTest() error = %v, want ErrInvalid", err)
		}
	})
}

func TestLoadRequiresRuntimeDirectory(t *testing.T) {
	configureEnvironment(t, false)

	_, err := loadForTest(Overrides{})
	if !errors.Is(err, ErrRuntimeDirRequired) {
		t.Fatalf("loadForTest() error = %v, want ErrRuntimeDirRequired", err)
	}
}

func TestLoadUsesHomeFallbacks(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	runtimeDir := filepath.Join(base, "runtime")

	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("XDG_RUNTIME_DIR", "")

	cfg, err := loadForTest(Overrides{Values: map[string]string{"runtime-dir": runtimeDir}})
	if err != nil {
		t.Fatalf("loadForTest() error = %v", err)
	}

	assertPath(t, cfg.Paths.ConfigDir, filepath.Join(home, ".config", "wake"))
	assertPath(
		t,
		cfg.Storage.WarehousePath,
		filepath.Join(home, ".local", "share", "wake", "wake.db"),
	)
	assertPath(t, cfg.Storage.ConnectionsPath, filepath.Join(home, ".config", "wake", "connections.db"))
	assertPath(t, cfg.Paths.RuntimeDir, runtimeDir)
}

func TestLoadRejectsInsecureDirectory(t *testing.T) {
	roots := configureEnvironment(t, true)
	insecure := filepath.Join(roots.base, "insecure")
	if err := os.Mkdir(insecure, 0o700); err != nil {
		t.Fatal(err)
	}
	//nolint:gosec // This test deliberately creates an insecure directory.
	if err := os.Chmod(insecure, 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := loadForTest(
		Overrides{Values: map[string]string{"warehouse-path": filepath.Join(insecure, "wake.db")}},
	)
	if !errors.Is(err, ErrInsecurePath) {
		t.Fatalf("loadForTest() error = %v, want ErrInsecurePath", err)
	}
}

func TestLoadRejectsNonDirectoryAndSymlink(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, string)
	}{
		{
			name: "file",
			setup: func(t *testing.T, path string) {
				t.Helper()

				if err := os.WriteFile(path, nil, 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "symlink",
			setup: func(t *testing.T, path string) {
				t.Helper()

				target := path + "-target"
				if err := os.Mkdir(target, 0o700); err != nil {
					t.Fatal(err)
				}

				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			roots := configureEnvironment(t, true)
			path := filepath.Join(roots.base, test.name)
			test.setup(t, path)

			_, err := loadForTest(
				Overrides{Values: map[string]string{"warehouse-path": filepath.Join(path, "wake.db")}},
			)
			if !errors.Is(err, ErrInsecurePath) {
				t.Fatalf("loadForTest() error = %v, want ErrInsecurePath", err)
			}
		})
	}
}

type environmentRoots struct {
	base    string
	config  string
	data    string
	state   string
	runtime string
}

func configureEnvironment(t *testing.T, withRuntime bool) environmentRoots {
	t.Helper()
	base := t.TempDir()
	roots := environmentRoots{
		base:    base,
		config:  filepath.Join(base, "config"),
		data:    filepath.Join(base, "data"),
		state:   filepath.Join(base, "state"),
		runtime: filepath.Join(base, "runtime"),
	}
	t.Setenv("HOME", filepath.Join(base, "home"))
	t.Setenv("XDG_CONFIG_HOME", roots.config)
	t.Setenv("XDG_DATA_HOME", roots.data)
	t.Setenv("XDG_STATE_HOME", roots.state)

	if withRuntime {
		t.Setenv("XDG_RUNTIME_DIR", roots.runtime)
	} else {
		t.Setenv("XDG_RUNTIME_DIR", "")
	}

	return roots
}

func assertPath(t *testing.T, got, want string) {
	t.Helper()

	if got != want {
		t.Errorf("path = %q, want %q", got, want)
	}
}
