// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package daemon

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"go.trippwill.dev/wake/internal/config"
)

type OwnershipError string

func (e OwnershipError) Error() string { return string(e) }

const (
	ErrAlreadyOwned OwnershipError = "another process owns the Wake database or socket"
	ErrOwnership    OwnershipError = "cannot claim private daemon paths"
)

type daemonOwnership struct{ files []*os.File }

func acquireOwnership(database, runtime string) (*daemonOwnership, error) {
	owner, err := acquireSocketOwnership(runtime)
	if err != nil {
		return nil, err
	}

	file, err := claimOwnershipFile(
		database,
		os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK,
		true,
	)
	if err != nil {
		owner.close()
		return nil, err
	}

	owner.files = append(owner.files, file)

	return owner, nil
}

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

// CheckStopped probes existing kernel ownership and the Unix socket without
// creating paths or opening SQLite. An unknown runtime path is allowed during
// initial setup. This is a point-in-time check; it does not reserve the paths.
func CheckStopped(cfg config.Config) error {
	files := []string{}
	dirs := []string{}
	for _, path := range []string{cfg.Storage.WarehousePath, cfg.Storage.ConnectionsPath} {
		if path == "" {
			continue
		}

		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return ErrOwnership
		}

		files = append(files, path)
		dirs = append(dirs, filepath.Dir(path))
	}

	if cfg.Paths.RuntimeDir != "" {
		files = append(files, filepath.Join(cfg.Paths.RuntimeDir, "wake.sock.lock"))
		dirs = append(dirs, cfg.Paths.RuntimeDir)
	}

	for _, path := range dirs {
		if err := config.CheckPrivateDir(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}

	for _, path := range files {
		file, err := claimOwnershipFile(
			path,
			os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK,
			true,
		)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}

		if err != nil {
			if errors.Is(err, ErrAlreadyOwned) {
				return ErrAlreadyOwned
			}

			return ErrOwnership
		}

		_ = file.Close()
	}

	if cfg.Paths.RuntimeDir == "" {
		return nil
	}

	return checkOfflineSocket(filepath.Join(cfg.Paths.RuntimeDir, "wake.sock"))
}

func (owner *daemonOwnership) close() {
	defer func() { owner.files = nil }()

	for i := len(owner.files) - 1; i >= 0; i-- {
		_ = owner.files[i].Close()
	}
}

func prepareSocket(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}

	if err != nil || info.Mode()&os.ModeSocket == 0 {
		return ErrOwnership
	}

	connection, err := net.DialTimeout("unix", path, 250*time.Millisecond)
	if err == nil {
		_ = connection.Close()
		return ErrAlreadyOwned
	}

	if !errors.Is(err, syscall.ECONNREFUSED) {
		return ErrAlreadyOwned
	}

	current, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}

	if err != nil || !os.SameFile(info, current) {
		return ErrOwnership
	}

	return os.Remove(path)
}

func checkOfflineSocket(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}

	if err != nil || info.Mode()&os.ModeSocket == 0 {
		return ErrOwnership
	}

	connection, err := net.DialTimeout("unix", path, 250*time.Millisecond)
	if err == nil {
		_ = connection.Close()
		return ErrAlreadyOwned
	}

	if !errors.Is(err, syscall.ECONNREFUSED) {
		return ErrAlreadyOwned
	}

	return nil
}

func acquireSocketOwnership(runtime string) (*daemonOwnership, error) {
	file, err := claimOwnershipFile(
		filepath.Join(runtime, "wake.sock.lock"),
		os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK,
		true,
	)
	if err != nil {
		return nil, err
	}

	return &daemonOwnership{files: []*os.File{file}}, nil
}
