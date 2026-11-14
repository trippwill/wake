// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"go.trippwill.dev/wake/internal/config"
)

func TestStoppedProfileRetainsOwnershipDuringChange(t *testing.T) {
	root := t.TempDir()
	cfg := config.Config{
		Paths: config.Paths{ConfigDir: filepath.Join(root, "config"), RuntimeDir: filepath.Join(root, "run")},
		Storage: config.Storage{
			WarehousePath:   filepath.Join(root, "data", "wake.db"),
			ConnectionsPath: filepath.Join(root, "access", "connections.db"),
		},
	}
	if err := config.Prepare(cfg); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(cfg.Storage.ConnectionsPath, []byte("kept"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := WithStoppedProfile(cfg, func() error {
		if owner, err := acquireProcessOwnership(cfg.Paths.RuntimeDir); !errors.Is(err, ErrAlreadyOwned) {
			if owner != nil {
				owner.close()
			}

			t.Fatal("process lock not held during change", err)
		}

		alternative := cfg
		alternative.Paths.RuntimeDir = filepath.Join(root, "other-run")
		if err := WithStoppedProfile(alternative, func() error {
			t.Fatal("accepted a busy store")
			return nil
		}); !errors.Is(err, ErrAlreadyOwned) {
			t.Fatal("store lock not held during change", err)
		}

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := WithStoppedProfile(cfg, func() error { return nil }); err != nil {
		t.Fatal("lock retained after change", err)
	}

	if _, err := os.Stat(cfg.Storage.WarehousePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("offline change opened a database", err)
	}
}

func resetPrivateDir(t *testing.T) string {
	t.Helper()
	p := t.TempDir()
	//nolint:gosec // Private test directory or synthetic database.
	if err := os.Chmod(p, 0o700); err != nil {
		t.Fatal(err)
	}

	return p
}
