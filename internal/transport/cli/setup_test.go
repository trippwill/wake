// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package cli

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.trippwill.dev/wake/internal/config"
	"go.trippwill.dev/wake/internal/credential"
	"go.trippwill.dev/wake/internal/transport/admin"
)

func TestSetupRerunPreservesKeyAndRotatesUnlock(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	setup, err := config.ReadLocalSetup("")
	if err != nil {
		t.Fatal(err)
	}

	if err := finishLocalSetup(setup, []byte("synthetic-first-unlock")); err != nil {
		t.Fatal(err)
	}

	key := readSetupMaterial(t, setup.MasterKeyPath)
	session := readSetupMaterial(t, filepath.Join(setup.ConfigDir, "admin-session.key"))
	verifier := readSetupMaterial(t, filepath.Join(setup.ConfigDir, "admin-unlock"))
	cookie := setupLogin(t, setup.ConfigDir, "synthetic-first-unlock", http.StatusSeeOther)

	setup, err = config.ReadLocalSetup("")
	if err != nil {
		t.Fatal(err)
	}

	if err := finishLocalSetup(setup, []byte("synthetic-second-unlock")); err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(key, readSetupMaterial(t, setup.MasterKeyPath)) {
		t.Fatal("rerun replaced the credential key")
	}

	if bytes.Equal(session, readSetupMaterial(t, filepath.Join(setup.ConfigDir, "admin-session.key"))) ||
		bytes.Equal(verifier, readSetupMaterial(t, filepath.Join(setup.ConfigDir, "admin-unlock"))) {
		t.Fatal("rerun did not rotate browser authentication")
	}

	setupLogin(t, setup.ConfigDir, "synthetic-first-unlock", http.StatusUnauthorized)
	setupLogin(t, setup.ConfigDir, "synthetic-second-unlock", http.StatusSeeOther)
	handler := setupHandler(t, setup.ConfigDir)
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8787/", nil)
	request.AddCookie(cookie)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/login" {
		t.Fatal("old session survived setup and authenticator reload")
	}
}

func TestSetupRetainsKeyAfterPartialFailure(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	setup, err := config.ReadLocalSetup("")
	if err != nil {
		t.Fatal(err)
	}

	blocked := filepath.Join(setup.ConfigDir, "admin-unlock")
	if err := os.Mkdir(blocked, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := finishLocalSetup(setup, []byte("synthetic-operator-unlock")); err == nil {
		t.Fatal("accepted blocked verifier")
	}

	key := readSetupMaterial(t, setup.MasterKeyPath)
	if err := os.Remove(blocked); err != nil {
		t.Fatal(err)
	}

	setup, err = config.ReadLocalSetup("")
	if err != nil {
		t.Fatal(err)
	}

	if err := finishLocalSetup(setup, []byte("synthetic-operator-unlock")); err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(key, readSetupMaterial(t, setup.MasterKeyPath)) {
		t.Fatal("retry replaced retained key")
	}

	auth, enabled, err := admin.Open(setup.ConfigDir, "local", "http://127.0.0.1:8787")
	if err != nil || !enabled {
		t.Fatalf("retry did not enable local authentication: %v", err)
	}

	auth.Close()
}

func TestSetupReusesUnconfiguredKey(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	setup, err := config.ReadLocalSetup("")
	if err != nil {
		t.Fatal(err)
	}

	if err := credential.CreateMasterKey(setup.MasterKeyPath); err != nil {
		t.Fatal(err)
	}

	key := readSetupMaterial(t, setup.MasterKeyPath)
	if err := finishLocalSetup(setup, []byte("synthetic-operator-unlock")); err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(key, readSetupMaterial(t, setup.MasterKeyPath)) {
		t.Fatal("replaced unconfigured key")
	}
}

func TestSetupRejectsBrokenKeysBeforePrompt(t *testing.T) {
	for _, kind := range []string{"configured missing", "invalid", "insecure", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())

			setup, err := config.ReadLocalSetup("")
			if err != nil {
				t.Fatal(err)
			}

			switch kind {
			case "configured missing":
				if err := setup.Save(); err != nil {
					t.Fatal(err)
				}
			case "invalid":
				if err := os.WriteFile(setup.MasterKeyPath, []byte("invalid"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "insecure":
				if err := os.WriteFile(setup.MasterKeyPath, make([]byte, 32), 0o600); err != nil {
					t.Fatal(err)
				}
				//nolint:gosec // Deliberately insecure synthetic key, independent of umask.
				if err := os.Chmod(setup.MasterKeyPath, 0o644); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink(filepath.Join(t.TempDir(), "missing"), setup.MasterKeyPath); err != nil {
					t.Fatal(err)
				}
			}

			var stdout, stderr bytes.Buffer
			if code := run(
				[]string{"setup", "--config", setup.ConfigPath},
				&stdout,
				&stderr,
			); code != exitFailure ||
				!strings.Contains(stderr.String(), "verify master key") ||
				stdout.Len() != 0 {
				t.Fatalf("code/output = %d, %q, %q", code, stdout.String(), stderr.String())
			}

			if _, err := os.Stat(filepath.Join(setup.ConfigDir, "admin-unlock")); !os.IsNotExist(err) {
				t.Fatal("broken key changed operator access")
			}
		})
	}
}

func TestSetupPreservesExternalKey(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	keyDir := filepath.Join(t.TempDir(), "external")
	if err := os.Mkdir(keyDir, 0o700); err != nil {
		t.Fatal(err)
	}

	keyPath := filepath.Join(keyDir, "external.key")
	if err := credential.CreateMasterKey(keyPath); err != nil {
		t.Fatal(err)
	}

	key := readSetupMaterial(t, keyPath)
	path := filepath.Join(t.TempDir(), "custom.toml")
	contents := []byte("# keep this configuration\n[credentials]\nmaster_key_path = '" + keyPath + "'\n")
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}

	setup, err := config.ReadLocalSetup(path)
	if err != nil {
		t.Fatal(err)
	}

	if err := checkSetupKey(setup); err != nil {
		t.Fatal(err)
	}

	if err := finishLocalSetup(setup, []byte("synthetic-operator-unlock")); err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(key, readSetupMaterial(t, keyPath)) || !bytes.Equal(contents, readSetupMaterial(t, path)) {
		t.Fatal("setup changed external key or existing configuration")
	}

	if _, err := os.Stat(filepath.Join(setup.ConfigDir, "master.key")); !os.IsNotExist(err) {
		t.Fatal("setup created an unused default key")
	}
}

func setupHandler(t *testing.T, dir string) http.Handler {
	t.Helper()

	auth, enabled, err := admin.Open(dir, "local", "http://127.0.0.1:8787")
	if err != nil || !enabled {
		t.Fatalf("open local auth: enabled=%t, err=%v", enabled, err)
	}

	t.Cleanup(auth.Close)

	handler, err := admin.NewHandler(auth, admin.Dependencies{})
	if err != nil {
		t.Fatal(err)
	}

	return handler
}

func setupLogin(t *testing.T, dir, unlock string, wantStatus int) *http.Cookie {
	t.Helper()
	handler := setupHandler(t, dir)
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

func TestSetupRejectsPipedUnlockAndOldCommands(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	stdin, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = stdin.Close() }()

	original := os.Stdin
	os.Stdin = stdin
	defer func() { os.Stdin = original }()

	var stdout, stderr bytes.Buffer
	if code := run([]string{"setup"}, &stdout, &stderr); code != exitFailure ||
		!strings.Contains(stderr.String(), "not a terminal") {
		t.Fatalf("piped setup = %d, %q", code, stderr.String())
	}

	entries, err := os.ReadDir(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "wake"))
	if (err != nil && !os.IsNotExist(err)) || len(entries) != 0 {
		t.Fatalf("piped setup persisted files: %v", err)
	}

	for _, command := range []string{"init-credentials", "set-unlock"} {
		if code := run([]string{"admin", command}, &stdout, &stderr); code != exitUsage {
			t.Fatalf("old command %s accepted", command)
		}
	}
}

func readSetupMaterial(t *testing.T, path string) []byte {
	t.Helper()
	//nolint:gosec // Only synthetic material from a temporary test directory.
	value, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { clear(value) })

	return value
}
