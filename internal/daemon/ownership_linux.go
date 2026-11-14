// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"syscall"

	"go.trippwill.dev/wake/internal/config"
)

type OwnershipError string

func (e OwnershipError) Error() string { return string(e) }

const (
	ErrAlreadyOwned OwnershipError = "another process owns the Wake database or runtime directory"
	ErrOwnership    OwnershipError = "cannot claim private daemon paths"
)

type daemonOwnership struct{ files []*os.File }

func claimOwnershipFile(path string, flags int, private bool) (*os.File, error) {
	//nolint:gosec // Validated private process paths; final-component symlinks are rejected.
	file, err := os.OpenFile(path, flags, 0o600)
	if err != nil {
		return nil, err
	}

	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || (private && info.Mode().Perm()&0o077 != 0) {
		_ = file.Close()
		return nil, ErrOwnership
	}

	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int64(stat.Uid) != int64(os.Geteuid()) {
		_ = file.Close()
		return nil, ErrOwnership
	}

	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()

		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrAlreadyOwned
		}

		return nil, ErrOwnership
	}

	return file, nil
}

// WithStoppedProfile holds process and existing store ownership while applying
// an offline change. It shares the daemon's lock order and never opens SQLite.
func WithStoppedProfile(cfg config.Config, change func() error) error {
	if err := config.Prepare(cfg); err != nil {
		return err
	}

	owner, err := acquireProcessOwnership(cfg.Paths.RuntimeDir)
	if err != nil {
		return err
	}
	defer owner.close()

	for _, path := range []string{cfg.Storage.WarehousePath, cfg.Storage.ConnectionsPath} {
		file, err := claimOwnershipFile(
			path,
			os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK,
			true,
		)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}

		if err != nil {
			return err
		}

		owner.files = append(owner.files, file)
	}

	return change()
}

func (owner *daemonOwnership) close() {
	defer func() { owner.files = nil }()

	for _, v := range slices.Backward(owner.files) {
		_ = v.Close()
	}
}

func acquireProcessOwnership(runtime string) (*daemonOwnership, error) {
	file, err := claimOwnershipFile(
		filepath.Join(runtime, "wake.lock"),
		os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK,
		true,
	)
	if err != nil {
		return nil, err
	}

	return &daemonOwnership{files: []*os.File{file}}, nil
}
