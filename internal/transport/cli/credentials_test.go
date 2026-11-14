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

func TestGenerateKeyRequiresExistingConfig(t *testing.T) {
	dir := setupEnvironment(t)

	var stdout, stderr bytes.Buffer
	if code := run([]string{"credentials", "generate-key"}, &stdout, &stderr); code != exitFailure ||
		!strings.Contains(stderr.String(), "config.toml") {
		t.Fatal(code, stderr.String())
	}

	if _, err := os.Lstat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("generation created the instance directory without a config", err)
	}
}

func TestGenerateKeyIsNoninteractiveAndPreservesOperator(t *testing.T) {
	dir := setupEnvironment(t)
	configPath := filepath.Join(dir, "config.toml")
	writeTestConfig(t, configPath, nil)

	var stdout, stderr bytes.Buffer
	if code := run([]string{"credentials", "generate-key"}, &stdout, &stderr); code != exitOK {
		t.Fatal(code, stderr.String())
	}

	keyPath := filepath.Join(dir, "credentials.key")
	key := readTestMaterial(t, keyPath)
	configFile := readTestMaterial(t, configPath)

	if len(key) != 32 || len(configFile) != 0 {
		t.Fatal("expected a 32-byte key and empty config")
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

	if err := admin.SetUnlock(dir, []byte("synthetic operator unlock")); err != nil {
		t.Fatal(err)
	}

	unlock := readTestMaterial(t, unlockPath)
	if code := run([]string{"credentials", "generate-key"}, &stdout, &stderr); code != exitFailure ||
		!strings.Contains(stderr.String(), "already exists") {
		t.Fatal(code, stderr.String())
	}

	if !bytes.Equal(key, readTestMaterial(t, keyPath)) ||
		!bytes.Equal(configFile, readTestMaterial(t, configPath)) ||
		!bytes.Equal(unlock, readTestMaterial(t, unlockPath)) {
		t.Fatal("generation changed existing instance material")
	}
}

func TestGenerateKeyUsesConfiguredPath(t *testing.T) {
	dir := setupEnvironment(t)
	alt := filepath.Join(t.TempDir(), "private")
	path := filepath.Join(alt, "container.toml")
	t.Setenv("WAKE_CONFIG", path)

	keyDir := filepath.Join(t.TempDir(), "keys")
	if err := os.Mkdir(keyDir, 0o700); err != nil {
		t.Fatal(err)
	}

	for _, keyPath := range []string{"custom.key", filepath.Join(keyDir, "custom.key")} {
		configBytes := []byte("# preserve the comment\n[credentials]\nkey_path=" + strconv.Quote(keyPath) + "\n")
		writeTestConfig(t, path, configBytes)

		var stdout, stderr bytes.Buffer
		if code := run([]string{"--json", "credentials", "generate-key"}, &stdout, &stderr); code != exitOK {
			t.Fatal(code, stderr.String())
		}

		if !filepath.IsAbs(keyPath) {
			keyPath = filepath.Join(alt, keyPath)
		}

		var result struct {
			Generated  bool   `json:"generated"`
			ConfigPath string `json:"config_path"`
			KeyPath    string `json:"key_path"`
		}
		if err := json.Unmarshal(stdout.Bytes(), &result); err != nil ||
			!result.Generated || result.ConfigPath != path || result.KeyPath != keyPath {
			t.Fatal("unexpected generation result", stdout.String(), err)
		}

		if len(readTestMaterial(t, keyPath)) != 32 || !bytes.Equal(configBytes, readTestMaterial(t, path)) {
			t.Fatal("expected a key at the configured path and an unchanged TOML")
		}
	}

	if _, err := os.Lstat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("selected config relied on the default profile", err)
	}
}

func TestGenerateKeyRejectsRunningInstance(t *testing.T) {
	dir := setupEnvironment(t)
	path := filepath.Join(dir, "config.toml")
	adminAddress := testFreeAddress(t)
	apiAddress := testFreeAddress(t)
	data := "[admin]\nlisten=" + strconv.Quote(
		adminAddress,
	) + "\n[api]\nlisten=" + strconv.Quote(
		apiAddress,
	) + "\n"
	writeTestConfig(t, path, []byte(data))

	resolved, err := launchForTest("", true)
	if err != nil {
		t.Fatal(err)
	}

	stop, _ := startShellDaemon(t, resolved.Config)
	defer stop()

	var stdout, stderr bytes.Buffer
	if code := run(
		[]string{"credentials", "generate-key"},
		&stdout,
		&stderr,
	); code != exitFailure ||
		!strings.Contains(stderr.String(), "another process owns") {
		t.Fatal(code, stderr.String())
	}

	if _, err := os.Lstat(resolved.Config.Credentials.KeyPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("generation changed a running instance", err)
	}
}

func testFreeAddress(t *testing.T) string {
	t.Helper()

	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}

	return address
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

func testLogin(t *testing.T, dir, unlock string, wantStatus int) *http.Cookie {
	t.Helper()
	handler := testAdminHandler(t, dir)
	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8787/login",
		strings.NewReader(url.Values{"unlock": {unlock}}.Encode()))
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
