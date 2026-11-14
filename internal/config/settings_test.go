// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestConfigSelectionAndRuntimeIdentity(t *testing.T) {
	roots := configureEnvironment(t, true)

	first, err := resolveForTest(Overrides{})
	if err != nil {
		t.Fatal(err)
	}

	dir := filepath.Join(roots.base, "alternate")

	other := filepath.Join(dir, "config.toml")
	writeTestConfig(t, other, []byte("[storage]\nwarehouse_path='data/history.sqlite'\n"))

	t.Setenv("WAKE_CONFIG", other)

	second, err := resolveForTest(Overrides{})
	if err != nil || second.Config.Paths.RuntimeDir == first.Config.Paths.RuntimeDir {
		t.Fatal(second, err)
	}

	assertPath(t, second.Config.Storage.WarehousePath, filepath.Join(dir, "data/history.sqlite"))
	assertPath(t, second.Config.Credentials.KeyPath, filepath.Join(dir, "credentials.key"))

	alias := filepath.Join(roots.base, "alias")
	if err := os.Symlink(dir, alias); err != nil {
		t.Fatal(err)
	}

	throughLink, err := resolveForTest(Overrides{
		ConfigPath: filepath.Join(alias, "config.toml"), ConfigPathExplicit: true,
	})
	if err != nil || throughLink.Config.Paths != second.Config.Paths {
		t.Fatal(throughLink, err)
	}

	t.Setenv("WAKE_CONFIG", filepath.Join(dir, "missing.toml"))

	if _, err := resolveForTest(Overrides{}); !errors.Is(err, ErrInvalid) {
		t.Fatal("missing selected config", err)
	}

	if _, err := resolveForTest(Overrides{ConfigPath: first.ConfigPath, ConfigPathExplicit: true}); err != nil {
		t.Fatal("flag did not override environment", err)
	}
}

func TestConfigRejectsFormerOverridesAndUnsafeFileAliases(t *testing.T) {
	roots := configureEnvironment(t, true)
	dir := filepath.Join(roots.config, "wake")
	path := filepath.Join(dir, "config.toml")

	t.Setenv("WAKE_API_LISTEN", "127.0.0.1:9898")

	resolved, err := resolveForTest(Overrides{})
	if err != nil || resolved.Config.API.Listen != "127.0.0.1:8788" {
		t.Fatal(resolved, err)
	}

	if _, err := resolveForTest(
		Overrides{Values: map[string]string{"api-listen": "127.0.0.1:9898"}},
	); err == nil {
		t.Fatal("accepted former instance flag")
	}

	for _, body := range []string{
		"[credentials]\nmaster_key_path='x'\n",
		"[storage]\nwarehouse_path='auth.json'\n",
		"[storage]\nconnections_path='wake.db-journal'\n",
		"[Storage]\nwarehouse_path='elsewhere'\n",
		"[admin]\ncurrency_refresh='invalid'\n",
	} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}

		if _, err := resolveForTest(Overrides{}); !errors.Is(err, ErrInvalid) {
			t.Fatal(body, err)
		}
	}
}
