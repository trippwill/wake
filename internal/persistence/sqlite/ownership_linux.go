// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
)

const (
	storeLockSuffix         = ".wake-lock"
	restoreSuffix           = ".restore-intent"
	ErrRestorePending Error = "restore pending; rerun wake restore with the original profile and archive"
)

// LockPaths serializes mutations even while a database inode is absent or replaced.
func LockPaths(paths [2]string, create bool, protected ...string) (func(), error) {
	if err := checkStorePaths(paths); err != nil {
		return nil, err
	}

	ordered := paths
	slices.Sort(ordered[:])

	var held []*os.File

	release := func() {
		for _, file := range slices.Backward(held) {
			_ = file.Close()
		}
	}
	for _, path := range ordered {
		anchor := path + storeLockSuffix
		for _, suffix := range []string{"", storeLockSuffix, restoreSuffix, UpgradeIntentSuffix, latestUpgradeSuffix} {
			if slices.Contains(protected, path+suffix) {
				release()
				return nil, ErrUnsafeStore
			}
		}

		if _, err := os.Lstat(filepath.Dir(path)); os.IsNotExist(err) {
			continue
		}

		if !create {
			if _, err := os.Lstat(anchor); os.IsNotExist(err) {
				continue
			}
		}

		file, err := lockStoreFile(anchor, create)
		if err != nil {
			release()
			return nil, err
		}

		held = append(held, file)
	}

	return release, nil
}

func CheckRestorePending(paths [2]string) error {
	for _, path := range paths {
		if _, err := os.Lstat(path + restoreSuffix); !os.IsNotExist(err) {
			return ErrRestorePending
		}
	}

	return nil
}

func claimFile(path string) (*os.File, error) {
	return lockStoreFile(path, true)
}

func lockStoreFile(path string, create bool) (*os.File, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, ErrUnsafeStore
	}

	dir, err := os.Lstat(filepath.Dir(path))
	if err != nil || !dir.IsDir() || !privateOwned(dir) {
		return nil, ErrUnsafeStore
	}

	flags := os.O_RDONLY | syscall.O_NOFOLLOW | syscall.O_NONBLOCK | syscall.O_CLOEXEC
	if create {
		flags = os.O_CREATE | os.O_RDWR | syscall.O_NOFOLLOW | syscall.O_NONBLOCK | syscall.O_CLOEXEC
	}
	//nolint:gosec // Absolute path in a private owned directory; reject final-component symlinks and special files.
	file, err := os.OpenFile(
		path,
		flags,
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

	for _, entry := range entries {
		candidate := filepath.Join(filepath.Dir(path), entry.Name())
		if isStoreSuperJournal(path, candidate) {
			files = append(files, candidate)
		}
	}

	return files, nil
}

func isStoreSuperJournal(store, path string) bool {
	suffix, ok := strings.CutPrefix(path, store+"-mj")
	return ok && len(suffix) == 9 && suffix[6] == '9' && strings.Trim(suffix, "0123456789ABCDEFabcdef") == ""
}

func singleLink(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Nlink == 1
}

func openUpgradeIntent(path string, expected os.FileInfo) (*os.File, error) {
	//nolint:gosec // The private parent is checked before access and the inode is verified below.
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, ErrUpgradeRecovery
	}

	actual, err := file.Stat()
	if err != nil || !os.SameFile(actual, expected) || !privateOwned(actual) || !singleLink(actual) {
		_ = file.Close()
		return nil, ErrUpgradeRecovery
	}

	return file, nil
}

func upgradeFiles(owners [2]*os.File) ([2]upgradeFile, error) {
	var files [2]upgradeFile
	for i, owner := range owners {
		if owner == nil {
			return files, ErrUpgradeRecovery
		}

		info, err := owner.Stat()
		if err != nil {
			return files, ErrUpgradeRecovery
		}

		stat := info.Sys().(*syscall.Stat_t)
		files[i] = upgradeFile{Device: stat.Dev, Inode: stat.Ino}
	}

	return files, nil
}
