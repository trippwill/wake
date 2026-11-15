// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.trippwill.dev/wake/internal/clientaccess"
	"go.trippwill.dev/wake/internal/transport/admin"
)

func TestRevokeSessionsCommandKeepsPasswordAndAPIGrants(t *testing.T) {
	cfg, path, _ := setupDaemonFixture(t)

	const password = "synthetic browser password"

	if err := admin.SetPassword(cfg.Paths.ConfigDir, []byte(password)); err != nil {
		t.Fatal(err)
	}

	clients := clientaccess.New(clientaccess.NewFileStore(cfg.Paths.ConfigDir))

	_, token, err := clients.Create(t.Context(), "Synthetic reader")
	if err != nil {
		t.Fatal(err)
	}
	defer token.Clear()

	value, err := token.Copy()
	if err != nil {
		t.Fatal(err)
	}
	defer clear(value)

	handler := testAdminHandler(t, cfg.Paths.ConfigDir)
	cookie := testLogin(t, handler, password, http.StatusSeeOther)

	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8787/system/contact", nil)
	request.AddCookie(cookie)

	before := httptest.NewRecorder()
	handler.ServeHTTP(before, request)

	if before.Code != http.StatusOK {
		t.Fatal("original session rejected", before.Code)
	}

	for _, jsonMode := range []bool{false, true} {
		args := []string{"access", "revoke", "sessions", "--config", path}
		if jsonMode {
			args = append([]string{"--json"}, args...)
		}

		var stdout, stderr bytes.Buffer

		if code := run(args, &stdout, &stderr); code != exitOK {
			t.Fatal(code, stderr.String())
		}

		if jsonMode {
			var result map[string]bool
			if err := json.Unmarshal(stdout.Bytes(), &result); err != nil || !result["sessions_revoked"] {
				t.Fatal("missing session revocation result", stdout.String(), err)
			}
		} else if !strings.Contains(stdout.String(), "Revoked all browser sessions") {
			t.Fatal("missing session revocation result", stdout.String())
		}
	}

	after := httptest.NewRecorder()
	handler.ServeHTTP(after, request)

	if after.Code != http.StatusUnauthorized {
		t.Fatal("session survived revocation", after.Code)
	}

	testLogin(t, testAdminHandler(t, cfg.Paths.ConfigDir), password, http.StatusSeeOther)

	_, release, err := clients.Admit(t.Context(), string(value))
	if err != nil {
		t.Fatal("financial API grant was revoked", err)
	}

	release()
}

func TestBrowserAccessRejectsInvalidCommandsAndProfiles(t *testing.T) {
	cfg, path, _ := setupDaemonFixture(t)

	assertFailure := func(args []string, want int, diagnostic string) {
		t.Helper()

		var stdout, stderr bytes.Buffer

		if code := run(args, &stdout, &stderr); code != want || !strings.Contains(stderr.String(), diagnostic) {
			t.Fatal("unexpected command status", code, stderr.String())
		}
	}

	assertFailure([]string{"access", "revoke"}, exitUsage, "specify a command after wake access revoke")
	assertFailure([]string{"access", "reset", "extra"}, exitUsage, "unexpected argument extra")
	assertFailure([]string{"access", "revoke", "sessions", "extra"}, exitUsage, "unexpected argument extra")

	missing := filepath.Join(cfg.Paths.ConfigDir, "missing.toml")
	assertFailure(
		[]string{"access", "set", "password", "--config", missing},
		exitFailure,
		"configure browser password",
	)
	assertFailure(
		[]string{"access", "revoke", "sessions", "--config", missing},
		exitFailure,
		"configure browser session revocation",
	)

	authPath := filepath.Join(cfg.Paths.ConfigDir, clientaccess.FileName)
	if err := os.WriteFile(authPath, []byte("invalid authentication data"), 0o600); err != nil {
		t.Fatal(err)
	}

	assertFailure(
		[]string{"access", "revoke", "sessions", "--config", path},
		exitFailure,
		"invalid private authentication state",
	)

	if !bytes.Equal(readTestMaterial(t, authPath), []byte("invalid authentication data")) {
		t.Fatal("overwrote invalid authentication data")
	}
}

func TestResetAccessCommandRevokesClientWithoutDatabases(t *testing.T) {
	cfg, path, _ := setupDaemonFixture(t)
	if err := admin.SetPassword(cfg.Paths.ConfigDir, []byte("synthetic operator unlock")); err != nil {
		t.Fatal(err)
	}

	store := clientaccess.NewFileStore(cfg.Paths.ConfigDir)
	clients := clientaccess.New(store)

	_, token, err := clients.Create(t.Context(), "Synthetic reader")
	if err != nil {
		t.Fatal(err)
	}
	defer token.Clear()

	value, err := token.Copy()
	if err != nil {
		t.Fatal(err)
	}
	defer clear(value)

	before, keyBefore, err := store.Browser()
	if err != nil || before == "" {
		t.Fatal("missing browser unlock before reset", err)
	}

	var stdout, stderr bytes.Buffer
	if code := run(
		[]string{"--json", "access", "reset", "--config", path},
		&stdout,
		&stderr,
	); code != exitOK {
		t.Fatal(code, stderr.String())
	}

	var result map[string]bool
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil || !result["access_reset"] {
		t.Fatal("missing access reset result", stdout.String(), err)
	}

	if _, _, err := clients.Admit(t.Context(), string(value)); !errors.Is(err, clientaccess.ErrUnauthorized) {
		t.Fatal("reset left client access in place", err)
	}

	after, keyAfter, err := store.Browser()
	if err != nil || before != after || keyBefore == keyAfter {
		t.Fatal("reset did not preserve the unlock and rotate the session key", err)
	}

	for _, path := range []string{cfg.Storage.WarehousePath, cfg.Storage.ConnectionsPath} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("reset created a database", filepath.Base(path), err)
		}
	}
}
