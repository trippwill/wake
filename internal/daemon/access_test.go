// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package daemon

import (
	"os"
	"testing"

	"go.trippwill.dev/wake/internal/clientaccess"
	persistence "go.trippwill.dev/wake/internal/persistence/sqlite"
	"go.trippwill.dev/wake/internal/transport/admin"
)

func TestResetAccessRequiresStoppedProfile(t *testing.T) {
	cfg := shellConfig(t)
	if err := admin.SetUnlock(cfg.Paths.ConfigDir, []byte("synthetic operator unlock")); err != nil {
		t.Fatal(err)
	}

	tokenFile := seedAPIClient(t, cfg)

	//nolint:gosec // Disposable test profile path.
	token, err := os.ReadFile(tokenFile)
	if err != nil {
		t.Fatal(err)
	}

	authStore := clientaccess.NewFileStore(cfg.Paths.ConfigDir)

	//nolint:gosec // Disposable test profile path.
	_, before, err := authStore.Browser()
	if err != nil {
		t.Fatal(err)
	}

	owner, err := acquireProcessOwnership(cfg.Paths.RuntimeDir)
	if err != nil {
		t.Fatal(err)
	}

	if err := ResetAccess(t.Context(), cfg); err == nil {
		t.Fatal("reset accepted active runtime owner")
	}

	owner.close()

	profile := persistence.Profile{
		WarehousePath:   cfg.Storage.WarehousePath,
		ConnectionsPath: cfg.Storage.ConnectionsPath,
	}

	store, err := persistence.Open(t.Context(), profile, nil)
	if err != nil {
		t.Fatal(err)
	}

	if err := ResetAccess(t.Context(), cfg); err == nil {
		t.Fatal("reset accepted active store owner")
	}

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{cfg.Storage.WarehousePath, cfg.Storage.ConnectionsPath} {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}

	if err := os.RemoveAll(cfg.Paths.RuntimeDir); err != nil {
		t.Fatal(err)
	}

	if err := ResetAccess(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}

	//nolint:gosec // Disposable test profile path.
	_, after, err := authStore.Browser()
	if err != nil || before == after {
		t.Fatal("session key was not rotated", err)
	}

	if _, _, err := clientaccess.New(authStore).Admit(t.Context(), string(token)); err == nil {
		t.Fatal("restored token still works")
	}

	_, enabled, err := admin.Open(cfg.Paths.ConfigDir, cfg.Admin.Origin())
	if err != nil || !enabled {
		t.Fatal("unlock lost", enabled, err)
	}
}
