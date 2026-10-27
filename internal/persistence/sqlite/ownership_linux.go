// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"os"
	"path/filepath"
	"syscall"
)

func claimFile(path string) (*os.File, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, ErrUnsafeStore
	}

	dir, err := os.Lstat(filepath.Dir(path))
	if err != nil || !dir.IsDir() || !privateOwned(dir) {
		return nil, ErrUnsafeStore
	}
	//nolint:gosec // Absolute path in a private owned directory; reject final-component symlinks and special files.
	file, err := os.OpenFile(
		path,
		os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC,
		0o600,
	)
	if err != nil {
		return nil, ErrUnsafeStore
	}

	if err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, ErrUnsafeStore
	}

	if err = checkFile(path, file); err != nil {
		_ = file.Close()
		return nil, err
	}

	for _, suffix := range []string{"-journal", "-wal", "-shm"} {
		info, statErr := os.Lstat(path + suffix)
		if os.IsNotExist(statErr) {
			continue
		}

		if statErr != nil || !info.Mode().IsRegular() || !privateOwned(info) ||
			info.Sys().(*syscall.Stat_t).Nlink != 1 {
			_ = file.Close()
			return nil, ErrUnsafeStore
		}
	}

	return file, nil
}

func privateOwned(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int64(stat.Uid) == int64(os.Geteuid()) && info.Mode().Perm()&0o077 == 0
}

func checkFile(path string, owner *os.File) error {
	opened, err := owner.Stat()
	if err != nil {
		return ErrUnsafeStore
	}

	current, err := os.Lstat(path)
	if err != nil || !current.Mode().IsRegular() || !privateOwned(current) ||
		!os.SameFile(opened, current) || current.Sys().(*syscall.Stat_t).Nlink != 1 {
		return ErrUnsafeStore
	}

	return nil
}
