// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"go.trippwill.dev/wake/internal/recoveryarchive"
)

const (
	upgradeBackupSuffix       = ".upgrade-backup.tar"
	ErrUpgradeBackup    Error = "pre-upgrade backup unavailable; check the backup path and free space or restore a verified pair"
)

type backupReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r backupReader) Read(data []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}

	return r.reader.Read(data)
}

func verifyBackupSource(ctx context.Context, path string, owner *os.File) error {
	db := sql.OpenDB(
		&connector{paths: [storeCount]string{path}, owners: [storeCount]*os.File{owner}, probe: true},
	)
	defer func() { _ = db.Close() }()

	var result string
	if err := db.QueryRowContext(ctx, profileStatement("Integrity", "main")).Scan(&result); err != nil {
		return err
	}

	if result != "ok" {
		return ErrCorrupt
	}

	return nil
}

// createUpgradeBackup copies both locked files before either writable SQLite
// connection opens. The archive replaces the preceding backup atomically.
func createUpgradeBackup(ctx context.Context, plan UpgradePlan, owners [storeCount]*os.File) (string, error) {
	for i, store := range plan.Stores {
		if err := checkUpgradeJournals(store.Path); err != nil {
			return "", err
		}

		if err := verifyBackupSource(ctx, store.Path, owners[i]); err != nil {
			return "", err
		}
	}

	if err := checkBackupDestination(plan.BackupPath); err != nil {
		return "", err
	}

	return writeBackupPair(ctx, plan.BackupPath, owners, true)
}

func writeBackupPair(
	ctx context.Context,
	path string,
	owners [storeCount]*os.File,
	replace bool,
) (string, error) {
	inputs, err := backupInputs(ctx, owners)
	if err != nil {
		return "", err
	}

	return writeRecoveryArchive(ctx, path, replace, func(output io.Writer) error {
		return recoveryarchive.WriteUpgrade(output, inputs)
	}, readBackupPair)
}

func backupInputs(
	ctx context.Context,
	owners [storeCount]*os.File,
) ([storeCount]recoveryarchive.SizedReader, error) {
	var inputs [storeCount]recoveryarchive.SizedReader
	for i, owner := range owners {
		input, err := backupInput(ctx, owner)
		if err != nil {
			return inputs, err
		}

		inputs[i] = input
	}

	return inputs, nil
}

func backupInput(ctx context.Context, owner *os.File) (recoveryarchive.SizedReader, error) {
	info, err := owner.Stat()
	if err != nil {
		return recoveryarchive.SizedReader{}, err
	}

	return recoveryarchive.SizedReader{
		Reader: backupReader{ctx, io.NewSectionReader(owner, 0, info.Size())},
		Size:   info.Size(),
	}, nil
}

func writeRecoveryArchive(
	ctx context.Context,
	path string,
	replace bool,
	write func(io.Writer) error,
	check func(io.Reader) error,
) (string, error) {
	temp, err := os.CreateTemp(filepath.Dir(path), ".wake-backup-")
	if err != nil {
		return "", err
	}

	defer func() { _ = os.Remove(temp.Name()) }()
	defer func() { _ = temp.Close() }()

	hash := sha256.New()

	if err := write(io.MultiWriter(temp, hash)); err != nil {
		return "", err
	}

	if err = temp.Sync(); err != nil {
		return "", err
	}

	if err = temp.Close(); err != nil {
		return "", err
	}

	expected := hex.EncodeToString(hash.Sum(nil))
	if err = verifyRecoveryArchive(ctx, temp.Name(), expected, check); err != nil {
		return "", err
	}

	if err = ctx.Err(); err != nil {
		return "", err
	}

	publish := renameNewFile
	if replace {
		publish = os.Rename
	}

	if err = publish(temp.Name(), path); err != nil {
		return "", err
	}

	return expected, syncUpgradeDir(path)
}

// checkBackupDestination protects existing files outside Wake's archive format.
func checkBackupDestination(path string) error {
	file, err := openBackup(path)
	if os.IsNotExist(err) {
		return nil
	}

	if err != nil {
		return err
	}

	defer func() { _ = file.Close() }()

	return readBackupPair(file)
}

func openBackup(path string) (*os.File, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}

	if !info.Mode().IsRegular() || !privateOwned(info) || !singleLink(info) {
		return nil, ErrUnsafeStore
	}

	return openUpgradeIntent(path, info)
}

func readBackupPair(archive io.Reader) error {
	err := recoveryarchive.VerifyUpgrade(archive)
	if errors.Is(err, recoveryarchive.ErrScope) {
		return ErrCorrupt
	}

	return archiveReadError(err)
}

func archiveReadError(err error) error {
	switch {
	case errors.Is(err, recoveryarchive.ErrScope):
		return ErrBackupScope
	case errors.Is(err, recoveryarchive.ErrPairedBackup):
		return fmt.Errorf("%w; use wake storage restore installation to restore the backup", ErrBackupScope)
	case errors.Is(err, recoveryarchive.ErrCorrupt):
		return ErrCorrupt
	}

	if checksum, ok := errors.AsType[recoveryarchive.ChecksumError](err); ok {
		return fmt.Errorf("%w: %s", ErrCorrupt, checksum.Error())
	}

	return err
}

func verifyUpgradeBackup(ctx context.Context, path, expected string) error {
	return verifyRecoveryArchive(ctx, path, expected, readBackupPair)
}

func verifyRecoveryArchive(ctx context.Context, path, expected string, check func(io.Reader) error) error {
	file, err := openBackup(path)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()

	hash := sha256.New()

	reader := io.TeeReader(backupReader{ctx, file}, hash)
	if err = check(reader); err != nil {
		return err
	}

	if _, err = io.Copy(io.Discard, reader); err != nil {
		return err
	}

	if hex.EncodeToString(hash.Sum(nil)) != expected {
		return fmt.Errorf("%w: archive checksum mismatch", ErrCorrupt)
	}

	return nil
}
