// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const (
	upgradeBackupSuffix       = ".upgrade-backup.tar"
	ErrUpgradeBackup    Error = "pre-upgrade backup unavailable; check the backup path and free space or restore a verified pair"
)

var backupEntries = [2]string{"wake-upgrade/warehouse.db", "wake-upgrade/access.db"}

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
	db := sql.OpenDB(&connector{paths: [2]string{path}, owners: [2]*os.File{owner}, probe: true})
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
func createUpgradeBackup(ctx context.Context, plan UpgradePlan, owners [2]*os.File) (string, error) {
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

	temp, err := os.CreateTemp(filepath.Dir(plan.BackupPath), ".wake-backup-")
	if err != nil {
		return "", err
	}

	defer func() { _ = os.Remove(temp.Name()) }()
	defer func() { _ = temp.Close() }()

	hash := sha256.New()

	archive := tar.NewWriter(io.MultiWriter(temp, hash))
	for i, owner := range owners {
		info, err := owner.Stat()
		if err != nil {
			return "", err
		}

		if err = archive.WriteHeader(
			&tar.Header{Name: backupEntries[i], Mode: 0o600, Size: info.Size()},
		); err != nil {
			return "", err
		}

		reader := io.NewSectionReader(owner, 0, info.Size())
		if _, err = io.Copy(archive, backupReader{ctx, reader}); err != nil {
			return "", err
		}
	}

	if err = archive.Close(); err != nil {
		return "", err
	}

	if err = temp.Sync(); err != nil {
		return "", err
	}

	if err = temp.Close(); err != nil {
		return "", err
	}

	expected := hex.EncodeToString(hash.Sum(nil))
	if err = verifyUpgradeBackup(ctx, temp.Name(), expected); err != nil {
		return "", err
	}

	if err = ctx.Err(); err != nil {
		return "", err
	}

	if err = os.Rename(temp.Name(), plan.BackupPath); err != nil {
		return "", err
	}

	return expected, syncUpgradeDir(plan.BackupPath)
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

	return readBackupPair(tar.NewReader(file))
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

func readBackupPair(archive *tar.Reader) error {
	for _, name := range backupEntries {
		entry, err := archive.Next()
		if err != nil || entry.Name != name || entry.Typeflag != tar.TypeReg || entry.Size <= 0 {
			return ErrCorrupt
		}
	}

	if _, err := archive.Next(); err != io.EOF {
		return ErrCorrupt
	}

	return nil
}

func verifyUpgradeBackup(ctx context.Context, path, expected string) error {
	file, err := openBackup(path)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()

	hash := sha256.New()

	reader := io.TeeReader(backupReader{ctx, file}, hash)
	if err = readBackupPair(tar.NewReader(reader)); err != nil {
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
