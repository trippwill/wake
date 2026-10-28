// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"os"
	"path/filepath"
	"strings"
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

	sidecars, err := storeSidecars(path)
	if err != nil {
		_ = file.Close()
		return nil, err
	}

	for _, sidecar := range sidecars {
		info, statErr := os.Lstat(sidecar)
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

// Attached transactions can leave a super-journal alongside the warehouse.
func storeSidecars(path string) ([]string, error) {
	files := []string{path + "-journal", path + "-wal", path + "-shm"}

	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		return nil, err
	}

	prefix := filepath.Base(path) + "-mj"
	for _, entry := range entries {
		suffix, ok := strings.CutPrefix(entry.Name(), prefix)
		if ok && len(suffix) == 9 && suffix[6] == '9' && strings.Trim(suffix, "0123456789ABCDEFabcdef") == "" {
			files = append(files, filepath.Join(filepath.Dir(path), entry.Name()))
		}
	}

	return files, nil
}
