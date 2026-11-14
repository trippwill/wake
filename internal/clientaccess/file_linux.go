// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package clientaccess

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"go.trippwill.dev/wake/internal/credential"
)

const ErrTokenFile Error = "token file must be an absolute, private, owner-readable regular file containing one client token"

func ReadTokenFile(path string) (*credential.Secret, error) {
	if !filepath.IsAbs(path) {
		return nil, ErrTokenFile
	}

	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, ErrTokenFile
	}

	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = syscall.Close(fd)
		return nil, ErrTokenFile
	}

	defer func() { _ = file.Close() }()

	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 ||
		info.Mode().Perm()&0o400 == 0 {
		return nil, ErrTokenFile
	}

	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() && stat.Uid != 0 {
		return nil, ErrTokenFile
	}

	value, err := io.ReadAll(io.LimitReader(file, 69))
	defer clear(value)

	if err != nil || len(value) == 69 {
		return nil, ErrTokenFile
	}

	text := strings.TrimSuffix(strings.TrimSuffix(string(value), "\n"), "\r")
	if _, err := TokenID(text); err != nil {
		return nil, ErrTokenFile
	}

	return credential.NewSecret([]byte(text)), nil
}
