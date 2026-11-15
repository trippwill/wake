// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"go.trippwill.dev/wake/internal/clientaccess"
	"go.trippwill.dev/wake/internal/credential"
	"go.trippwill.dev/wake/internal/transport/admin"
)

func setupEnvironment(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(root, "run"))

	return filepath.Join(root, "config", "wake")
}

func TestConfigInitCreatesOnlyTOMLByDefault(t *testing.T) {
	dir := setupEnvironment(t)
	t.Setenv("XDG_RUNTIME_DIR", "")

	var stdout, stderr bytes.Buffer
	if code := run([]string{"--json", "config", "init"}, &stdout, &stderr); code != exitOK {
		t.Fatal(code, stderr.String())
	}

	var result struct {
		ConfigPath   string `json:"config_path"`
		KeyGenerated bool   `json:"key_generated"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil ||
		result.ConfigPath != filepath.Join(dir, "config.toml") || result.KeyGenerated {
		t.Fatal("unexpected init result", stdout.String(), err)
	}

	info, err := os.Stat(result.ConfigPath)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("expected private TOML", info, err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != "config.toml" {
		t.Fatal("expected only the TOML", entries, err)
	}
}

func TestConfigInitGeneratesKeyAndRefusesExistingTOML(t *testing.T) {
	dir := setupEnvironment(t)
	configPath := filepath.Join(dir, "config.toml")

	var stdout, stderr bytes.Buffer
	if code := run([]string{"config", "init", "--generate-key"}, &stdout, &stderr); code != exitOK {
		t.Fatal(code, stderr.String())
	}

	keyPath := filepath.Join(dir, "credentials.key")

	key := readTestMaterial(t, keyPath)
	if len(key) != 32 {
		t.Fatal("expected a 32-byte key")
	}

	info, err := os.Stat(keyPath)
	if err != nil || info.Mode().Perm() != 0o400 {
		t.Fatal("expected an owner-read-only key", info, err)
	}

	unlockPath := filepath.Join(dir, clientaccess.FileName)
	if _, err := os.Lstat(unlockPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("generation created an unlock", err)
	}

	for _, name := range []string{"wake.db", "connections.db"} {
		if _, err := os.Lstat(filepath.Join(dir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("generation created a database", name, err)
		}
	}

	if err := admin.SetPassword(dir, []byte("synthetic operator unlock")); err != nil {
		t.Fatal(err)
	}

	unlock := readTestMaterial(t, unlockPath)
	invalid := []byte("unparsed TOML [")
	writeTestConfig(t, configPath, invalid)

	for _, opts := range [][]string{nil, {"--set", "admin.port=8887"}, {"--generate-key"}} {
		stderr.Reset()

		if code := run(append([]string{"config", "init"}, opts...), &stdout, &stderr); code != exitFailure ||
			!strings.Contains(stderr.String(), "configuration already exists at "+configPath) ||
			!strings.Contains(stderr.String(), "wake config check") {
			t.Fatal(code, stderr.String())
		}
	}

	if !bytes.Equal(key, readTestMaterial(t, keyPath)) ||
		!bytes.Equal(invalid, readTestMaterial(t, configPath)) ||
		!bytes.Equal(unlock, readTestMaterial(t, unlockPath)) {
		t.Fatal("generation changed existing instance material")
	}
}

func TestConfigInitUsesSettingsAndSelectedPath(t *testing.T) {
	dir := setupEnvironment(t)
	alt := filepath.Join(t.TempDir(), "private")
	path := filepath.Join(alt, "container.toml")
	t.Setenv("WAKE_CONFIG", path)

	keyDir := filepath.Join(t.TempDir(), "keys")
	if err := os.Mkdir(keyDir, 0o700); err != nil {
		t.Fatal(err)
	}

	for _, keyPath := range []string{"custom, key=1.key", filepath.Join(keyDir, "custom.key")} {
		args := []string{
			"--json", "config", "init", "--generate-key",
			"--set", "admin.port=8887", "--set", "api.port=8888",
			"--set", "admin.currency_refresh=true", "--set", "credentials.key_path=" + keyPath,
		}
		if filepath.IsAbs(keyPath) {
			path = filepath.Join(alt, "other.toml")
			args = append(args, "--config", path)
		}

		var stdout, stderr bytes.Buffer
		if code := run(args, &stdout, &stderr); code != exitOK {
			t.Fatal(code, stderr.String())
		}

		if !filepath.IsAbs(keyPath) {
			keyPath = filepath.Join(alt, keyPath)
		}

		var result struct {
			KeyGenerated bool   `json:"key_generated"`
			ConfigPath   string `json:"config_path"`
			KeyPath      string `json:"key_path"`
		}
		if err := json.Unmarshal(stdout.Bytes(), &result); err != nil ||
			!result.KeyGenerated || result.ConfigPath != path || result.KeyPath != keyPath {
			t.Fatal("unexpected generation result", stdout.String(), err)
		}

		resolved, err := launchForTest(path, false)
		if err != nil || resolved.Config.Admin.Port != 8887 || resolved.Config.API.Port != 8888 ||
			!resolved.Config.Admin.CurrencyRefresh || resolved.Config.Credentials.KeyPath != keyPath ||
			len(readTestMaterial(t, keyPath)) != 32 {
			t.Fatal("expected a key and a TOML with the overrides", err)
		}
	}

	if _, err := os.Lstat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("selected config relied on the default profile", err)
	}
}

func TestConfigInitChecksPrivacyOfDirectoryAliasTarget(t *testing.T) {
	setupEnvironment(t)

	for _, test := range []struct {
		name string
		mode os.FileMode
		want int
	}{
		{"private", 0o700, exitOK},
		{"group-accessible", 0o750, exitFailure},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "profile")
			alias := filepath.Join(root, "alias")

			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}

			if err := os.Chmod(dir, test.mode); err != nil {
				t.Fatal(err)
			}

			if err := os.Symlink(dir, alias); err != nil {
				t.Fatal(err)
			}

			var stdout, stderr bytes.Buffer
			if code := run(
				[]string{"config", "init", "--config", filepath.Join(alias, "config.toml")},
				&stdout,
				&stderr,
			); code != test.want {
				t.Fatal(code, stdout.String(), stderr.String())
			}

			path := filepath.Join(dir, "config.toml")
			if test.want == exitOK {
				if !strings.Contains(stdout.String(), path) || len(readTestMaterial(t, path)) == 0 {
					t.Fatal("expected a TOML at the canonical path", stdout.String())
				}
			} else if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) ||
				!strings.Contains(stderr.String(), "must not be accessible by group or other users") {
				t.Fatal("expected the nonprivate target to be rejected", err, stderr.String())
			}
		})
	}
}

func TestConfigInitRejectsInvalidSettingsBeforeCreatingFiles(t *testing.T) {
	dir := setupEnvironment(t)

	for _, opts := range [][]string{
		{"--set", "admin.port"},
		{"--set", "admin.unknown=1"},
		{"--set", "admin.currency_refresh=maybe"},
		{"--set", "admin.port=abc"},
		{"--set", "admin.port=65536"},
		{"--set", "admin.port=8788"},
		{"--set", "admin.port=8887", "--set", "admin.port=8889"},
		{"--set", "credentials.key_path=config.toml"},
	} {
		var stdout, stderr bytes.Buffer
		if code := run(
			append([]string{"config", "init", "--generate-key"}, opts...),
			&stdout,
			&stderr,
		); code != exitFailure {
			t.Fatal(opts, code, stderr.String())
		}

		if _, err := os.Lstat(dir); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("invalid init created the instance directory", err)
		}
	}
}

func TestConfigInitReusesExistingKey(t *testing.T) {
	dir := setupEnvironment(t)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	keyPath := filepath.Join(dir, "credentials.key")
	if err := credential.CreateMasterKey(keyPath); err != nil {
		t.Fatal(err)
	}

	key := readTestMaterial(t, keyPath)

	var stdout, stderr bytes.Buffer
	if code := run([]string{"config", "init", "--generate-key"}, &stdout, &stderr); code != exitOK ||
		!strings.Contains(stdout.String(), "Using existing credential encryption key") {
		t.Fatal(code, stdout.String(), stderr.String())
	}

	if !bytes.Equal(key, readTestMaterial(t, keyPath)) {
		t.Fatal("init changed the existing key")
	}
}

func TestConfigInitCannotGenerateKeyForRunningInstance(t *testing.T) {
	dir := setupEnvironment(t)
	path := filepath.Join(dir, "config.toml")

	adminPort := testFreePort(t)
	apiPort := testFreePort(t)
	for apiPort == adminPort {
		apiPort = testFreePort(t)
	}

	data := "[admin]\nport=" + strconv.Itoa(adminPort) + "\n[api]\nport=" + strconv.Itoa(apiPort) + "\n"
	writeTestConfig(t, path, []byte(data))

	resolved, err := launchForTest("", true)
	if err != nil {
		t.Fatal(err)
	}

	stop, _ := startShellDaemon(t, resolved.Config)
	defer stop()

	var stdout, stderr bytes.Buffer
	if code := run(
		[]string{"config", "init", "--generate-key", "--config", filepath.Join(dir, "other.toml")},
		&stdout,
		&stderr,
	); code != exitFailure ||
		!strings.Contains(stderr.String(), "key provisioning failed") ||
		!strings.Contains(stderr.String(), "another process owns") ||
		!strings.Contains(stderr.String(), "wake access provision credential-key --config") {
		t.Fatal(code, stderr.String())
	}

	stderr.Reset()

	if code := run([]string{"access", "provision", "credential-key"}, &stdout, &stderr); code != exitFailure ||
		!strings.Contains(stderr.String(), "another process owns") {
		t.Fatal(code, stderr.String())
	}

	if _, err := os.Lstat(resolved.Config.Credentials.KeyPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("generation changed a running instance", err)
	}
}

func testFreePort(t *testing.T) int {
	t.Helper()

	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}

	return port
}

func testAdminHandler(t *testing.T, dir string) http.Handler {
	t.Helper()

	auth, enabled, err := admin.Open(dir, "http://127.0.0.1:8787")
	if err != nil || !enabled {
		t.Fatalf("open local auth: enabled=%t, err=%v", enabled, err)
	}

	handler, err := admin.NewHandler(auth, admin.Dependencies{})
	if err != nil {
		t.Fatal(err)
	}

	return handler
}

func testLogin(t *testing.T, handler http.Handler, unlock string, wantStatus int) *http.Cookie {
	t.Helper()

	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8787/login",
		strings.NewReader(url.Values{"password": {unlock}}.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Origin", "http://127.0.0.1:8787")

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != wantStatus {
		t.Fatalf("login status = %d, want %d", response.Code, wantStatus)
	}

	result := response.Result()
	defer func() { _ = result.Body.Close() }()

	if wantStatus == http.StatusSeeOther {
		cookies := result.Cookies()
		if len(cookies) != 1 {
			t.Fatal("missing login cookie")
		}

		return cookies[0]
	}

	return nil
}

func readTestMaterial(t *testing.T, path string) []byte {
	t.Helper()
	//nolint:gosec // Only synthetic material from a temporary test directory.
	value, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { clear(value) })

	return value
}
