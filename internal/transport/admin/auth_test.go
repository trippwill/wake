// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package admin

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestOpenReportsUnprovisionedLocalMode(t *testing.T) {
	auth, enabled, err := Open(t.TempDir(), "local", "http://127.0.0.1:8787")
	if err != nil {
		t.Fatal(err)
	}

	if auth != nil || enabled {
		t.Fatalf("Open() = (%#v, %t), want (nil, false)", auth, enabled)
	}
}

func TestOpenRejectsNamedPipes(t *testing.T) {
	if directory := os.Getenv("WAKE_TEST_ADMIN_FIFO_DIR"); directory != "" {
		auth, _, err := Open(directory, os.Getenv("WAKE_TEST_ADMIN_FIFO_MODE"), "http://127.0.0.1:8787")
		if auth != nil {
			auth.Close()
		}

		if !errors.Is(err, ErrInvalidSecretFile) {
			t.Fatalf("Open() error = %v, want ErrInvalidSecretFile", err)
		}

		return
	}

	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct{ name, mode string }{
		{sessionKeyFile, "local"},
		{unlockFile, "local"},
		{proxyGuardFile, "proxy"},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			if test.mode == "local" {
				if err := SetUnlock(directory, []byte("correct horse battery staple")); err != nil {
					t.Fatal(err)
				}
			} else if _, err := SetProxyGuard(directory); err != nil {
				t.Fatal(err)
			}

			path := filepath.Join(directory, test.name)
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}

			if err := syscall.Mkfifo(path, 0o600); err != nil {
				t.Fatal(err)
			}

			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()

			// A blocking open must fail this test without hanging the parent process.
			command := exec.CommandContext(ctx, executable, "-test.run=^TestOpenRejectsNamedPipes$")

			command.Env = append(os.Environ(),
				"WAKE_TEST_ADMIN_FIFO_DIR="+directory,
				"WAKE_TEST_ADMIN_FIFO_MODE="+test.mode,
			)
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("secret FIFO was not rejected: %v\n%s", err, output)
			}
		})
	}
}

func TestSetUnlockRejectsWeakValue(t *testing.T) {
	if err := SetUnlock(t.TempDir(), []byte("too short")); !errors.Is(err, ErrWeakUnlock) {
		t.Fatalf("SetUnlock() error = %v, want ErrWeakUnlock", err)
	}
}

func TestOpenRejectsInsecureSecretFiles(t *testing.T) {
	configDir := t.TempDir()
	if err := SetUnlock(configDir, []byte("correct horse battery staple")); err != nil {
		t.Fatal(err)
	}
	//nolint:gosec // This test must create an insecure file to verify rejection.
	if err := os.Chmod(configDir+"/"+sessionKeyFile, 0o644); err != nil {
		t.Fatal(err)
	}

	if _, _, err := Open(configDir, "local", "http://127.0.0.1:8787"); !errors.Is(err, ErrInvalidSecretFile) {
		t.Fatalf("Open() error = %v", err)
	}
}
