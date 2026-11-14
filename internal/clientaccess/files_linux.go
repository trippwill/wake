// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package clientaccess

import (
	"io"
	"os"
	"path/filepath"
	"syscall"
)

func readPrivateFile(path string, limit int64) ([]byte, error) {
	if !filepath.IsAbs(path) {
		return nil, ErrInvalidSecretFile
	}

	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}

	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = syscall.Close(fd)
		return nil, ErrInvalidSecretFile
	}
	defer func() { _ = file.Close() }()

	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 ||
		info.Mode().Perm()&0o400 == 0 {
		return nil, ErrInvalidSecretFile
	}

	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || (int(stat.Uid) != 0 && int(stat.Uid) != os.Geteuid()) {
		return nil, ErrInvalidSecretFile
	}

	value, err := io.ReadAll(io.LimitReader(file, limit))
	if err != nil {
		return nil, err
	}

	if len(value) == int(limit) {
		clear(value)
		return nil, ErrInvalidSecretFile
	}

	return value, nil
}

func writePrivateFile(path string, value []byte) error {
	if !filepath.IsAbs(path) || len(value) == 0 {
		return ErrInvalidSecretFile
	}

	file, err := os.CreateTemp(filepath.Dir(path), ".wake-auth-")
	if err != nil {
		return err
	}

	temporary := file.Name()
	defer func() { _ = os.Remove(temporary) }()

	if err = file.Chmod(0o600); err == nil {
		_, err = file.Write(value)
	}

	if err == nil {
		err = file.Sync()
	}

	if closeErr := file.Close(); err == nil {
		err = closeErr
	}

	if err != nil {
		return err
	}

	if err := os.Rename(temporary, path); err != nil {
		return err
	}

	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()

	return dir.Sync()
}
