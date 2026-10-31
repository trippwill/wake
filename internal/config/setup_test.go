// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package config

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGuidedSetupPreservesAdvancedSettingsAndIgnoresLaunchOverrides(t *testing.T) {
	roots := configureEnvironment(t, true)
	dir := filepath.Join(roots.base, "custom")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(dir, "config.toml")
	original := "[storage]\nconnections_path='" + filepath.Join(
		roots.base,
		"access",
		"credentials.sqlite",
	) + "'\n[log]\nlevel='debug'\n[admin]\nport=8799\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("WAKE_WAREHOUSE_PATH", "/not-persisted")
	t.Setenv("WAKE_CONNECTIONS_PATH", "/not-persisted-access")
	t.Setenv("WAKE_ADMIN_PORT", "9999")
	t.Setenv("WAKE_LOG_LEVEL", "error")

	setup, err := ReadLocalSetup(path)
	if err != nil {
		t.Fatal(err)
	}

	guide, err := setup.Guidance()
	if err != nil {
		t.Fatal(err)
	}

	if guide.Storage.WarehousePath != filepath.Join(roots.data, "wake", "wake.db") || guide.AdminPort != 8799 {
		t.Fatalf("guide baked in launch overrides: %#v", guide)
	}

	setup, err = setup.WithLaunchSettings(
		Storage{
			WarehousePath:   filepath.Join(roots.base, "chosen-data", "history.sqlite"),
			ConnectionsPath: guide.Storage.ConnectionsPath,
		},
		filepath.Join(roots.base, "chosen-runtime"),
		8790,
	)
	if err != nil {
		t.Fatal(err)
	}

	if err := setup.Save(); err != nil {
		t.Fatal(err)
	}
	//nolint:gosec // Only a synthetic configuration selected above.
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	decoded, err := decodeFileConfig(content)
	if err != nil {
		t.Fatal(err)
	}

	if decoded.Storage.ConnectionsPath != filepath.Join(roots.base, "access", "credentials.sqlite") ||
		decoded.Log.Level != "debug" ||
		*decoded.Admin.Port != 8790 ||
		strings.Contains(string(content), "not-persisted") {
		t.Fatalf("guided save lost settings: %s", content)
	}
}

func TestLocalSetupPreservesConfiguration(t *testing.T) {
	for _, contents := range []string{
		"# operator configuration\n[log]\nlevel = 'debug'\n[admin]\nport = 8799\n",
		"log = { level = 'debug' }\nadmin.port = 8799\ncredentials = { master_key_path = '' }\n",
	} {
		t.Run(contents, func(t *testing.T) {
			roots := configureEnvironment(t, true)
			configDir := filepath.Join(roots.base, "custom")
			if err := os.Mkdir(configDir, 0o700); err != nil {
				t.Fatal(err)
			}

			path := filepath.Join(configDir, "custom.toml")
			if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}

			setup, err := ReadLocalSetup(path)
			if err != nil {
				t.Fatal(err)
			}

			if err := setup.Save(); err != nil {
				t.Fatal(err)
			}

			cfg, err := loadForTest(Overrides{ConfigPath: path, ConfigPathExplicit: true})
			if err != nil {
				t.Fatal(err)
			}

			if cfg.Log.Level.String() != "DEBUG" || cfg.Admin.Port != 8799 ||
				cfg.Credentials.MasterKeyPath != filepath.Join(roots.config, "wake", "master.key") {
				t.Fatalf("setup changed settings: %#v", cfg)
			}
		})
	}
}

func TestLocalSetupPreservesConfiguredKeyAndDetectsChanges(t *testing.T) {
	roots := configureEnvironment(t, false)
	path := filepath.Join(roots.base, "custom.toml")
	contents := []byte("# preserve exactly\n[credentials]\nmaster_key_path = '/external/master.key'\n")
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}

	setup, err := ReadLocalSetup(path)
	if err != nil {
		t.Fatal(err)
	}

	if !setup.KeyConfigured || setup.MasterKeyPath != "/external/master.key" {
		t.Fatalf("setup = %#v", setup)
	}

	if err := setup.Save(); err != nil {
		t.Fatal(err)
	}

	//nolint:gosec // Temporary test configuration.
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, contents) {
		t.Fatalf("configured file was rewritten: %v", err)
	}

	if err := os.WriteFile(path, []byte("[admin]\nauth_mode = 'proxy'\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := setup.Save(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Save() after edit = %v", err)
	}
}

func TestLocalSetupRejectsUnsafeConfiguration(t *testing.T) {
	for _, contents := range []string{
		"[admin]\nauth_mode = 'proxy'\npublic_origin = 'https://wake.example.test'\n",
		"[admin]\nauth_mode = 'unknown'\n", "[admin]\nport = 0\n",
		"[admin]\npublic_origin = 'http://localhost:8787'\n",
		"[credentials]\nmaster_key_path = 'relative'\n",
		"[unknown]\nvalue = 1\n", "malformed = [",
	} {
		t.Run(contents, func(t *testing.T) {
			roots := configureEnvironment(t, false)
			path := filepath.Join(roots.base, "custom.toml")
			if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}

			if _, err := ReadLocalSetup(path); !errors.Is(err, ErrInvalid) {
				t.Fatalf("ReadLocalSetup() = %v", err)
			}
		})
	}
}

func TestLocalSetupRejectsUnsafePaths(t *testing.T) {
	roots := configureEnvironment(t, false)
	for _, path := range []string{"relative", roots.base} {
		if _, err := ReadLocalSetup(path); err == nil {
			t.Fatalf("accepted %q", path)
		}
	}

	target := filepath.Join(roots.base, "target.toml")
	link := filepath.Join(roots.base, "link.toml")

	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	if _, err := ReadLocalSetup(link); !errors.Is(err, ErrInsecurePath) {
		t.Fatalf("accepted config symlink: %v", err)
	}
}

func TestSetupCreatesAlternativeConfigurationWithoutChangingDefault(t *testing.T) {
	roots := configureEnvironment(t, true)
	defaultPath := filepath.Join(roots.config, "wake", "config.toml")
	if err := os.MkdirAll(filepath.Dir(defaultPath), 0o700); err != nil {
		t.Fatal(err)
	}

	original := []byte("[log]\nlevel='debug'\n")
	if err := os.WriteFile(defaultPath, original, 0o600); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(roots.base, "new-private", "alternative.toml")

	setup, err := ReadLocalSetup(path)
	if err != nil {
		t.Fatal(err)
	}

	guide, err := setup.Guidance()
	if err != nil {
		t.Fatal(err)
	}

	guide.Storage.WarehousePath = filepath.Join(roots.base, "warehouse.db")

	setup, err = setup.WithLaunchSettings(
		guide.Storage,
		filepath.Join(roots.base, "runtime"),
		8790,
	)
	if err != nil {
		t.Fatal(err)
	}

	if err := setup.Save(); err != nil {
		t.Fatal(err)
	}

	resolved, err := resolveForTest(Overrides{ConfigPath: path, ConfigPathExplicit: true})
	if err != nil || resolved.Config.Storage.WarehousePath != filepath.Join(roots.base, "warehouse.db") {
		t.Fatal("new destination did not resolve", err)
	}
	// #nosec G304 -- Synthetic default config selected by this test.
	unchanged, err := os.ReadFile(defaultPath)
	if err != nil || !bytes.Equal(unchanged, original) {
		t.Fatal("default config changed", err)
	}
	// #nosec G703 -- Synthetic newly created config selected by this test.
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("new config permissions", err)
	}
}

func TestNewSetupDestinationDetectsConcurrentCreation(t *testing.T) {
	roots := configureEnvironment(t, true)
	path := filepath.Join(roots.base, "new-private", "new.toml")

	setup, err := ReadLocalSetup(path)
	if err != nil {
		t.Fatal(err)
	}

	original := []byte("[log]\nlevel='debug'\n")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := setup.Save(); !errors.Is(err, ErrInvalid) {
		t.Fatal("overwrote concurrent creation", err)
	}
	// #nosec G304 -- Synthetic concurrently created config selected by this test.
	unchanged, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(unchanged, original) {
		t.Fatal("concurrent config changed", err)
	}
}
