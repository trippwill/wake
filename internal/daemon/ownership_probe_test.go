// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package daemon

import (
	"bytes"
	"errors"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"go.trippwill.dev/wake/internal/config"
)

func TestCheckStoppedDoesNotCreateOrChangeFiles(t *testing.T) {
	root := resetPrivateDir(t)
	paths := config.Paths{
		DataDir:    filepath.Join(root, "new-data"),
		RuntimeDir: filepath.Join(root, "new-runtime"),
	}
	for _, target := range []config.Paths{paths, {DataDir: paths.DataDir}} {
		if err := CheckStopped(target); err != nil {
			t.Fatal(err)
		}
	}

	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatal("probe created paths", entries, err)
	}

	paths = config.Paths{DataDir: root, RuntimeDir: resetPrivateDir(t)}
	database := filepath.Join(root, "wake.db")
	content := []byte("not SQLite; detection must not open a store")
	if err := os.WriteFile(database, content, 0o600); err != nil {
		t.Fatal(err)
	}

	before, err := os.Stat(database)
	if err != nil {
		t.Fatal(err)
	}

	if err := CheckStopped(paths); err != nil {
		t.Fatal(err)
	}

	// #nosec G304 -- Synthetic database within this test's private directory.
	after, err := os.ReadFile(database)
	if err != nil || !bytes.Equal(content, after) {
		t.Fatal("probe changed database", err)
	}

	info, err := os.Stat(database)
	if err != nil || !os.SameFile(before, info) {
		t.Fatal("probe changed database inode", err)
	}

	entries, err = os.ReadDir(paths.RuntimeDir)
	if err != nil || len(entries) != 0 {
		t.Fatal("probe created runtime artifacts", entries, err)
	}
}

func TestCheckStoppedDetectsOwnersAndReleasesProbes(t *testing.T) {
	paths := config.Paths{DataDir: resetPrivateDir(t), RuntimeDir: resetPrivateDir(t)}

	owner, err := acquireOwnership(filepath.Join(paths.DataDir, "wake.db"), paths.RuntimeDir)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.close()

	alias := resetPrivateDir(t)
	if err := os.Link(filepath.Join(paths.DataDir, "wake.db"), filepath.Join(alias, "wake.db")); err != nil {
		t.Fatal(err)
	}

	for _, target := range []config.Paths{
		paths,
		{DataDir: paths.DataDir},
		{DataDir: alias, RuntimeDir: resetPrivateDir(t)},
		{DataDir: resetPrivateDir(t), RuntimeDir: paths.RuntimeDir},
	} {
		if err := CheckStopped(target); !errors.Is(err, ErrAlreadyOwned) {
			t.Fatal("occupied paths accepted", target, err)
		}
	}

	owner.close()

	if err := os.Remove(filepath.Join(alias, "wake.db")); err != nil {
		t.Fatal(err)
	}

	if err := CheckStopped(paths); err != nil {
		t.Fatal("released owner still detected", err)
	}

	owner, err = acquireOwnership(filepath.Join(paths.DataDir, "wake.db"), paths.RuntimeDir)
	if err != nil {
		t.Fatal("probe retained a lock", err)
	}

	owner.close()
}

func TestCheckStoppedDetectsActiveSocketAndRetainsStaleSocket(t *testing.T) {
	paths := config.Paths{DataDir: resetPrivateDir(t), RuntimeDir: resetPrivateDir(t)}
	socket := filepath.Join(paths.RuntimeDir, "wake.sock")

	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = listener.Close() }()

	listener.SetUnlinkOnClose(false)

	if err := CheckStopped(paths); !errors.Is(err, ErrAlreadyOwned) {
		t.Fatal("active socket accepted", err)
	}

	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}

	if err := CheckStopped(paths); err != nil {
		t.Fatal("stale socket treated as a running daemon", err)
	}

	if info, err := os.Lstat(socket); err != nil || info.Mode()&os.ModeSocket == 0 {
		t.Fatal("probe removed stale socket", err)
	}
}

func TestCheckStoppedRejectsUnsafePaths(t *testing.T) {
	for _, kind := range []string{"relative-data", "relative-runtime", "public-directory", "symlink", "fifo", "public-file", "non-socket"} {
		t.Run(kind, func(t *testing.T) {
			paths := config.Paths{DataDir: resetPrivateDir(t), RuntimeDir: resetPrivateDir(t)}
			database := filepath.Join(paths.DataDir, "wake.db")
			switch kind {
			case "relative-data":
				paths.DataDir = ""
			case "relative-runtime":
				paths.RuntimeDir = "relative"
			case "public-directory":
				// #nosec G302 -- Deliberately insecure directory must be rejected.
				//nolint:gosec // Private test directory or synthetic database.
				if err := os.Chmod(paths.DataDir, 0o755); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink(filepath.Join(paths.DataDir, "missing"), database); err != nil {
					t.Fatal(err)
				}
			case "fifo":
				if err := syscall.Mkfifo(database, 0o600); err != nil {
					t.Fatal(err)
				}
			case "public-file":
				// #nosec G306 -- Deliberately insecure database must be rejected.
				if err := os.WriteFile(database, nil, 0o644); err != nil {
					t.Fatal(err)
				}
				// #nosec G302 -- This fixture must stay public under a restrictive umask.
				//nolint:gosec // Private test directory or synthetic database.
				if err := os.Chmod(database, 0o644); err != nil {
					t.Fatal(err)
				}
			case "non-socket":
				if err := os.WriteFile(filepath.Join(paths.RuntimeDir, "wake.sock"), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}

			if err := CheckStopped(paths); err == nil {
				t.Fatal("unsafe paths accepted")
			}
		})
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
