// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package config

import (
	"crypto/sha256"
	"errors"
	"fmt"
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
	assertPath(t, cfg.Storage.WarehousePath, filepath.Join(roots.config, "wake", "wake.db"))
	assertPath(t, cfg.Storage.ConnectionsPath, filepath.Join(roots.config, "wake", "connections.db"))
	assertPath(
		t,
		cfg.Paths.RuntimeDir,
		filepath.Join(roots.runtime, "wake", fmt.Sprintf("%x", sha256.Sum256([]byte(cfg.Paths.ConfigDir)))),
	)

	if cfg.Log.Level != slog.LevelInfo {
		t.Errorf("Log.Level = %v, want %v", cfg.Log.Level, slog.LevelInfo)
	}

	if cfg.Admin.Listen != "127.0.0.1:8787" || cfg.Admin.Address() != "127.0.0.1:8787" ||
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

func TestLoadRelativeAndAbsolutePaths(t *testing.T) {
	roots := configureEnvironment(t, true)
	fileData := filepath.Join(roots.base, "file-data", "history.sqlite")
	fileRuntime := "file-runtime"

	configPath := filepath.Join(roots.base, "custom.toml")
	contents := "[storage]\nwarehouse_path = \"" + fileData + "\"\n[paths]\n" +
		"runtime_dir = \"" + fileRuntime + "\"\n" +
		"[log]\nlevel = \"debug\"\n" +
		"[credentials]\nkey_path = \"" + filepath.Join(roots.base, "master.key") + "\"\n" +
		"[admin]\nlisten = '127.0.0.1:8790'\npublic_origin = \"https://wake.example.test\"\n"
	if err := os.WriteFile(configPath, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := loadForTest(Overrides{
		ConfigPath:         configPath,
		ConfigPathExplicit: true,
	})
	if err != nil {
		t.Fatalf("loadForTest() error = %v", err)
	}

	assertPath(t, cfg.Storage.WarehousePath, fileData)
	assertPath(t, cfg.Paths.RuntimeDir, filepath.Join(roots.base, fileRuntime))

	if cfg.Log.Level != slog.LevelDebug {
		t.Errorf("Log.Level = %v, want %v", cfg.Log.Level, slog.LevelDebug)
	}

	if cfg.Credentials.KeyPath != filepath.Join(roots.base, "master.key") {
		t.Errorf("Credentials.KeyPath = %q", cfg.Credentials.KeyPath)
	}

	if cfg.Admin.Listen != "127.0.0.1:8790" || cfg.Admin.Address() != "127.0.0.1:8790" ||
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

		{name: "removed operational setting", contents: "[storage]\noperational_path = \"relative\"\n"},
		{name: "unsupported log level", contents: "[log]\nlevel = \"verbose\"\n"},
		{name: "invalid resume backfills", contents: "[sync]\nresume_backfills = \"false\"\n"},
		{name: "removed key setting", contents: "[credentials]\nmaster_key_path = \"master.key\"\n"},
		{name: "zero admin port", contents: "[admin]\nlisten = '127.0.0.1:0'\n"},
		{name: "invalid listener IP", contents: "[api]\nlisten = 'localhost:8788'\n"},
		{name: "network admin without origin", contents: "[admin]\nlisten = '0.0.0.0:8787'\n"},
		{
			name:     "invalid public origin",
			contents: "[admin]\npublic_origin = \"https://wake.example.test/path\"\n",
		},
		{
			name:     "public origin with default port",
			contents: "[admin]\npublic_origin = \"https://wake.example.test:443\"\n",
		},
		{
			name:     "public origin with noncanonical default port",
			contents: "[admin]\npublic_origin = \"https://wake.example.test:0443\"\n",
		},
		{
			name:     "public origin with empty query",
			contents: "[admin]\npublic_origin = \"https://wake.example.test?\"\n",
		},
		{
			name:     "public origin with uppercase host",
			contents: "[admin]\npublic_origin = \"https://WAKE.example.test\"\n",
		},
		{
			name:     "public origin with empty host",
			contents: "[admin]\npublic_origin = \"http://:8787\"\n",
		},
		{
			name:     "public origin with empty port",
			contents: "[admin]\npublic_origin = \"https://wake.example.test:\"\n",
		},
		{
			name:     "public origin with empty fragment",
			contents: "[admin]\npublic_origin = \"https://wake.example.test#\"\n",
		},
		{
			name:     "public origin with noncanonical IPv6",
			contents: "[admin]\npublic_origin = \"http://[0:0:0:0:0:0:0:1]:8787\"\n",
		},
		{
			name:     "public origin with unbracketed IPv6",
			contents: "[admin]\npublic_origin = \"http://::1\"\n",
		},
		{
			name:     "public origin with shortened IPv4",
			contents: "[admin]\npublic_origin = \"http://127.1:8787\"\n",
		},
		{
			name:     "public origin with hexadecimal IPv4",
			contents: "[admin]\npublic_origin = \"http://0x7f000001:8787\"\n",
		},
		{
			name:     "public origin with bracketed IPv4",
			contents: "[admin]\npublic_origin = \"http://[127.0.0.1]:8787\"\n",
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

func TestAdminLoopbackOriginUsesCanonicalAddress(t *testing.T) {
	for _, test := range []struct{ listen, origin string }{
		{"[0:0:0:0:0:0:0:1]:8787", "http://[::1]:8787"},
		{"[0:0:0:0:0:0:0:1]:80", "http://[::1]"},
		{"[::ffff:127.0.0.1]:8787", "http://127.0.0.1:8787"},
		{"127.0.0.1:80", "http://127.0.0.1"},
	} {
		t.Run(test.listen, func(t *testing.T) {
			admin, err := resolveAdmin(fileAdmin{Listen: test.listen})
			if err != nil || admin.Origin() != test.origin {
				t.Fatal(admin.Origin(), err)
			}
		})
	}
}

func TestLoadConfigFilePresence(t *testing.T) {
	t.Run("default is required", func(t *testing.T) {
		roots := configureEnvironment(t, true)
		if err := os.Remove(filepath.Join(roots.config, "wake", "config.toml")); err != nil {
			t.Fatal(err)
		}

		if _, err := loadForTest(Overrides{}); !errors.Is(err, ErrInvalid) {
			t.Fatal(err)
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

func TestLoadRetainsLoadedConfigurationFile(t *testing.T) {
	for _, selected := range []string{"default", "flag", "environment"} {
		t.Run(selected, func(t *testing.T) {
			roots := configureEnvironment(t, true)
			path := filepath.Join(roots.config, "wake", "config.toml")
			overrides := Overrides{}

			if selected == "flag" || selected == "environment" {
				path = filepath.Join(roots.base, "operator settings.toml")
			}

			if err := os.WriteFile(path, nil, 0o600); err != nil {
				t.Fatal(err)
			}

			switch selected {
			case "flag":
				overrides.ConfigPath, overrides.ConfigPathExplicit = path, true
			case "environment":
				t.Setenv("WAKE_CONFIG", path)
			}

			cfg, err := loadForTest(overrides)
			if err != nil {
				t.Fatal(err)
			}

			if cfg.Paths.ConfigFile != path {
				t.Fatalf("loaded configuration file = %q, want %q", cfg.Paths.ConfigFile, path)
			}
		})
	}
}

func TestLoadRequiresRuntimeDirectory(t *testing.T) {
	configureEnvironment(t, false)

	_, err := loadForTest(Overrides{})
	if !errors.Is(err, ErrRuntimeDirRequired) {
		t.Fatalf("loadForTest() error = %v, want ErrRuntimeDirRequired", err)
	}
}

func TestLoadUsesHomeFallbacks(t *testing.T) {
	base := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(base, 0o700); err != nil {
		t.Fatal(err)
	}

	home := filepath.Join(base, "home")
	runtimeDir := filepath.Join(base, "runtime")

	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("XDG_RUNTIME_DIR", "")

	path := filepath.Join(home, ".config", "wake", "config.toml")
	writeTestConfig(t, path, fmt.Appendf(nil, "[paths]\nruntime_dir=%q\n", runtimeDir))

	cfg, err := loadForTest(Overrides{})
	if err != nil {
		t.Fatalf("loadForTest() error = %v", err)
	}

	assertPath(t, cfg.Paths.ConfigDir, filepath.Join(home, ".config", "wake"))
	assertPath(
		t,
		cfg.Storage.WarehousePath,
		filepath.Join(home, ".config", "wake", "wake.db"),
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

	_, err := loadWarehouseForTest(t, roots, filepath.Join(insecure, "wake.db"))
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

			_, err := loadWarehouseForTest(t, roots, filepath.Join(path, "wake.db"))
			if !errors.Is(err, ErrInsecurePath) {
				t.Fatalf("loadForTest() error = %v, want ErrInsecurePath", err)
			}
		})
	}
}

func loadWarehouseForTest(t *testing.T, roots environmentRoots, path string) (Config, error) {
	t.Helper()

	configPath := filepath.Join(roots.config, "wake", "config.toml")
	if err := os.WriteFile(
		configPath,
		fmt.Appendf(nil, "[storage]\nwarehouse_path=%q\n", path),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	return loadForTest(Overrides{})
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

	base := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(base, 0o700); err != nil {
		t.Fatal(err)
	}

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

	writeTestConfig(t, filepath.Join(roots.config, "wake", "config.toml"), nil)

	return roots
}

func writeTestConfig(t *testing.T, path string, content []byte) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertPath(t *testing.T, got, want string) {
	t.Helper()

	if got != want {
		t.Errorf("path = %q, want %q", got, want)
	}
}
