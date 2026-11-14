// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package clientaccess

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"testing/synctest"
)

type revokeSyncFailure struct{ FileStore }

func (s revokeSyncFailure) RevokeClient(ctx context.Context, id string) error {
	if err := s.FileStore.RevokeClient(ctx, id); err != nil {
		return err
	}

	return fmt.Errorf("sync auth directory: %w", syscall.EIO)
}

func TestRevokeCancelsAdmittedRequestAfterAmbiguousWrite(t *testing.T) {
	m := New(revokeSyncFailure{privateTestStore(t)})

	client, secret, err := m.Create(t.Context(), "Synthetic client")
	if err != nil {
		t.Fatal(err)
	}
	defer secret.Clear()

	token, err := secret.Copy()
	if err != nil {
		t.Fatal(err)
	}
	defer clear(token)

	active, release, err := m.Admit(t.Context(), string(token))
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	if err := m.Revoke(t.Context(), client.ID()); !errors.Is(err, syscall.EIO) {
		t.Fatal("revocation did not report the write error", err)
	}

	if active.Err() != context.Canceled {
		t.Fatal("revoked request remained active")
	}

	if _, _, err := m.Admit(t.Context(), string(token)); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("revocation did not remove the grant", err)
	}
}

func TestAuthUpdateBoundsDirectoryLockWait(t *testing.T) {
	store := privateTestStore(t)

	dir, err := os.Open(store.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dir.Close() }()

	if err := syscall.Flock(int(dir.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		done := make(chan error, 1)
		go func() { done <- store.RevokeClient(ctx, "synthetic client") }()

		synctest.Wait()
		cancel()

		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatal("lock wait ignored cancellation", err)
		}
	})

	synctest.Test(t, func(t *testing.T) {
		if err := store.Reset(t.Context()); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal("lock wait exceeded its own deadline", err)
		}
	})

	if _, err := os.Lstat(filepath.Join(store.dir, FileName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("waiting for the lock mutated auth state", err)
	}
}

func privateTestStore(t *testing.T) FileStore {
	t.Helper()

	dir := filepath.Join(t.TempDir(), "instance")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	return NewFileStore(dir)
}
