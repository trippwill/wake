// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

//go:build linux

package credential

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

const masterKeyFileReadLimit = dataKeySize + 1

// CreateMasterKey creates and verifies a new private AES-256 master-key file.
// The parent must already be a private directory owned by root or the effective
// user. Existing key material is never overwritten.
func CreateMasterKey(path string) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("%w: path must be absolute", ErrInvalidMasterKeyPath)
	}

	if err := validateMasterKeyDirectory(filepath.Dir(path)); err != nil {
		return err
	}

	file, err := os.OpenFile(
		path,
		os.O_WRONLY|os.O_CREATE|os.O_EXCL,
		0o400,
	) // #nosec G304,G703 -- absolute path and private parent are validated above.
	if errors.Is(err, os.ErrExist) {
		return ErrMasterKeyExists
	}

	if err != nil {
		return fmt.Errorf("create master key file: %w", err)
	}

	created := true
	defer func() {
		if created {
			_ = os.Remove(path)
		}
	}()

	material := make([]byte, dataKeySize)
	defer clear(material)

	if _, err = io.ReadFull(rand.Reader, material); err == nil {
		_, err = file.Write(material)
	}

	if err == nil {
		err = file.Sync()
	}

	if closeErr := file.Close(); err == nil {
		err = closeErr
	}

	if err != nil {
		return fmt.Errorf("write master key file: %w", err)
	}

	key, err := LoadMasterKey(path)
	if err != nil {
		return fmt.Errorf("verify master key file: %w", err)
	}

	key.Clear()

	created = false

	return nil
}

// LoadMasterKey reads an AES-256 master key from a private Linux regular file.
// The file must be addressed by an absolute path, contain exactly 32 raw bytes,
// have no group or other permissions, and be owned by root or the effective user.
func LoadMasterKey(path string) (*MasterKey, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("%w: path must be absolute", ErrInvalidMasterKeyPath)
	}

	info, err := os.Lstat(path) // #nosec G304,G703 -- the caller supplies an absolute path.
	if err != nil {
		return nil, fmt.Errorf("inspect master key file: %w", err)
	}

	if err := validateMasterKeyFile(info); err != nil {
		return nil, err
	}

	fd, err := syscall.Open(
		path,
		syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK,
		0,
	) // #nosec G304,G703 -- the caller supplies an absolute path; O_NOFOLLOW protects the final component.
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return nil, fmt.Errorf("%w: symbolic links are not allowed", ErrInsecureMasterKeyFile)
		}

		return nil, fmt.Errorf("open master key file: %w", err)
	}

	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = syscall.Close(fd)
		return nil, fmt.Errorf("%w: create file handle", ErrInsecureMasterKeyFile)
	}
	defer func() { _ = file.Close() }()

	info, err = file.Stat()
	if err != nil {
		return nil, fmt.Errorf("inspect master key file: %w", err)
	}

	if err := validateMasterKeyFile(info); err != nil {
		return nil, err
	}

	material, err := io.ReadAll(io.LimitReader(file, masterKeyFileReadLimit))
	if err != nil {
		return nil, fmt.Errorf("read master key file: %w", err)
	}
	defer clear(material)

	if len(material) != dataKeySize {
		return nil, fmt.Errorf(
			"%w: expected exactly %d raw bytes",
			ErrInvalidMasterKey,
			dataKeySize,
		)
	}

	return NewMasterKey(material)
}

func validateMasterKeyFile(info os.FileInfo) error {
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%w: file is not regular", ErrInsecureMasterKeyFile)
	}

	if info.Mode().Perm()&0o077 != 0 || info.Mode().Perm()&0o400 == 0 {
		return fmt.Errorf(
			"%w: file must be owner-readable and inaccessible to group and other users",
			ErrInsecureMasterKeyFile,
		)
	}

	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || (int(stat.Uid) != 0 && int(stat.Uid) != os.Geteuid()) {
		return fmt.Errorf(
			"%w: file must be owned by root or the effective user",
			ErrInsecureMasterKeyFile,
		)
	}

	return nil
}

func validateMasterKeyDirectory(path string) error {
	info, err := os.Lstat(path) // #nosec G304,G703 -- parent of validated absolute key path.
	if err != nil {
		return fmt.Errorf("inspect master key directory: %w", err)
	}

	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%w: parent directory must be private", ErrInsecureMasterKeyFile)
	}

	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || (int(stat.Uid) != 0 && int(stat.Uid) != os.Geteuid()) {
		return fmt.Errorf(
			"%w: parent directory must be owned by root or the effective user",
			ErrInsecureMasterKeyFile,
		)
	}

	return nil
}
