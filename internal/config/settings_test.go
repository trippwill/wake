// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package config

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
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
	writeTestConfig(t, other, []byte("[storage]\nfinancial_path='data/history.sqlite'\n"))

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
	if err != nil || resolved.Config.API.Address() != "127.0.0.1:8788" {
		t.Fatal(resolved, err)
	}

	if _, err := resolveForTest(
		Overrides{Values: map[string]string{"api-listen": "127.0.0.1:9898"}},
	); err == nil {
		t.Fatal("accepted former instance flag")
	}

	for _, body := range []string{
		"[credentials]\nmaster_key_path='x'\n",
		"[storage]\nfinancial_path='auth.json'\n",
		"[storage]\ncredentials_path='wake.db-journal'\n",
		"[storage]\nsync_path='connections.db-journal'\n",
		"[storage]\nsync_path='auth.json'\n",
		"[Storage]\nfinancial_path='elsewhere'\n",
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

func TestConfiguredListenerAuthorities(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")

	const body = "[admin]\nallowed_origins=['HTTP://LOCALHOST:18787/', 'https://wake.example:443']\n[api]\nallowed_hosts=['LOCALHOST:18788','[::1]:80']"

	out, err := resolveSnapshot(path, "flag", []byte(body), false)
	if err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(out.Config.Admin.Origins(), []string{"http://localhost:18787", "https://wake.example"}) ||
		out.Config.Admin.Origin() != "http://localhost:18787" ||
		out.Config.Admin.Address() != "127.0.0.1:8787" {
		t.Fatal(out.Config.Admin)
	}

	if !slices.Equal(out.Config.API.AllowedHosts, []string{"localhost:18788", "[::1]:80"}) {
		t.Fatal(out.Config.API)
	}

	for _, body := range []string{
		"[admin]\nallowed_origins=[]", "[api]\nallowed_hosts=[]", "[api]\nallowed_hosts=['http://localhost']",
		"[api]\nallowed_hosts=['*:8788']", "[api]\nallowed_hosts=['localhost/path']",
	} {
		if _, err := resolveSnapshot(path, "flag", []byte(body), false); !errors.Is(err, ErrInvalid) {
			t.Errorf("accepted %s: %v", body, err)
		}
	}

	for _, raw := range []string{"http://*.example", "http://name@localhost", "http://localhost/path", "https://localhost?", "https://localhost#", "ftp://localhost", "localhost", "http://localhost:65536", "http://[localhost]"} {
		_, err := resolveSnapshot(path, "flag", []byte("[admin]\nallowed_origins=['"+raw+"']"), false)
		if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "admin.allowed_origins") {
			t.Errorf("%s: %v", raw, err)
		}
	}
}

func TestBrowserIPOriginSerialization(t *testing.T) {
	for raw, want := range map[string]string{
		"http://127.0.0.1.:18787/":        "http://127.0.0.1:18787",
		"http://[::ffff:127.0.0.1]:18787": "http://[::ffff:7f00:1]:18787",
		"http://[::ffff:7f00:1]:18787":    "http://[::ffff:7f00:1]:18787",
	} {
		got, err := BrowserOrigin(raw)
		if err != nil || got != want {
			t.Errorf("BrowserOrigin(%q) = %q, %v; want %q", raw, got, err, want)
		}
	}

	for _, raw := range []string{"http://127.1:18787", "http://0177.0.0.1", "http://0x7f000001", "http://host.123"} {
		if _, err := BrowserOrigin(raw); err == nil {
			t.Errorf("accepted ambiguous or invalid IPv4 origin %q", raw)
		}
	}
}
