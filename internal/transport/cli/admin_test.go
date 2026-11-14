// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"go.trippwill.dev/wake/internal/clientaccess"
	"go.trippwill.dev/wake/internal/transport/admin"
)

func TestResetAccessCommandRevokesClientWithoutDatabases(t *testing.T) {
	cfg, path, _ := setupDaemonFixture(t)
	if err := admin.SetUnlock(cfg.Paths.ConfigDir, []byte("synthetic operator unlock")); err != nil {
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
		[]string{"--json", "admin", "reset-access", "--config", path},
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
