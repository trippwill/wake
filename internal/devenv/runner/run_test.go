// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package runner

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestDaemonChildRetainsWorkspaceLock(t *testing.T) {
	const flags = syscall.LOCK_EX | syscall.LOCK_NB

	workspace := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(workspace, "child"),
		[]byte("read -r ignored || true\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	// #nosec G304 -- This directory was created by the test.
	lock, err := os.Open(workspace)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()

	if err = syscall.Flock(int(lock.Fd()), flags); err != nil {
		t.Fatal(err)
	}

	cmd := daemonCommand("/bin/sh", filepath.Join(workspace, ".wake-dev"), "new", lock)
	cmd.Dir = workspace

	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stdin.Close() }()

	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cmd.ProcessState == nil {
			_ = stdin.Close()
			_ = cmd.Wait()
		}
	}()

	if err = lock.Close(); err != nil {
		t.Fatal(err)
	}

	// #nosec G304 -- Reopen the test directory through a separate file description.
	probe, err := os.Open(workspace)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = probe.Close() }()

	if err = syscall.Flock(int(probe.Fd()), flags); !errors.Is(err, syscall.EWOULDBLOCK) {
		t.Fatalf("lock while the child is alive returned %v, want EWOULDBLOCK", err)
	}

	if err = stdin.Close(); err != nil {
		t.Fatal(err)
	}

	if err = cmd.Wait(); err != nil {
		t.Fatal(err)
	}

	if err = syscall.Flock(int(probe.Fd()), flags); err != nil {
		t.Fatalf("lock after the child exited: %v", err)
	}
}
