// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	upgradeBackupSuffix       = ".upgrade-backup.tar"
	backupDigestEntry         = "wake-backup/v1/sha256"
	ErrUpgradeBackup    Error = "pre-upgrade backup unavailable; check the backup path and free space or restore a verified pair"
)

var (
	backupEntries = [storeCount]string{
		"wake-backup/v2/warehouse.db",
		"wake-backup/v2/access.db",
		"wake-backup/v2/sync.db",
	}
	legacyBackupEntries = [storeCount]string{
		"wake-upgrade/warehouse.db",
		"wake-upgrade/access.db",
		"wake-upgrade/sync.db",
	}
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
	return writeRecoveryArchive(ctx, path, replace, func(archive *tar.Writer) error {
		var digests strings.Builder
		for i, owner := range owners {
			info, err := owner.Stat()
			if err != nil {
				return err
			}

			hash := sha256.New()
			reader := backupReader{ctx, io.NewSectionReader(owner, 0, info.Size())}
			if err := writeRecoveryEntry(
				archive,
				backupEntries[i],
				info.Size(),
				io.TeeReader(reader, hash),
			); err != nil {
				return err
			}

			digests.WriteString(hex.EncodeToString(hash.Sum(nil)))
		}

		return writeRecoveryEntry(
			archive,
			backupDigestEntry,
			int64(digests.Len()),
			strings.NewReader(digests.String()),
		)
	}, readBackupPair)
}

func writeRecoveryEntry(archive *tar.Writer, name string, size int64, reader io.Reader) error {
	if err := archive.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: size}); err != nil {
		return err
	}

	_, err := io.Copy(archive, reader)

	return err
}

func writeRecoveryArchive(
	ctx context.Context,
	path string,
	replace bool,
	write func(*tar.Writer) error,
	check func(*tar.Reader) error,
) (string, error) {
	temp, err := os.CreateTemp(filepath.Dir(path), ".wake-backup-")
	if err != nil {
		return "", err
	}

	defer func() { _ = os.Remove(temp.Name()) }()
	defer func() { _ = temp.Close() }()

	hash := sha256.New()

	archive := tar.NewWriter(io.MultiWriter(temp, hash))
	if err := write(archive); err != nil {
		return "", err
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

func isRegularRecoveryEntry(entry *tar.Header, name string) bool {
	if entry.Name != name || entry.Typeflag != tar.TypeReg || entry.Size <= 0 {
		return false
	}

	// Go's tar reader can expand GNU sparse PAX members with type TypeReg.
	for key := range entry.PAXRecords {
		if strings.HasPrefix(key, "GNU.sparse.") {
			return false
		}
	}

	return true
}

func readBackupPair(archive *tar.Reader) error {
	err := consumeBackupPair(archive, func(_ int, member io.Reader) error {
		_, err := io.Copy(io.Discard, member)

		return err
	})
	if errors.Is(err, ErrBackupScope) {
		return ErrCorrupt
	}

	return err
}

// Versioned member names require a digest record. Only the original member
// names can use the legacy format, which has no embedded checksums.
func consumeBackupPair(archive *tar.Reader, consume func(int, io.Reader) error) error {
	entry, err := archive.Next()
	if err != nil {
		return ErrBackupScope
	}

	return consumeBackupStores(archive, entry, consume)
}

func consumeBackupStores(archive *tar.Reader, entry *tar.Header, consume func(int, io.Reader) error) error {
	var err error

	legacy := entry.Name == legacyBackupEntries[0]
	names := backupEntries
	if legacy {
		names = legacyBackupEntries
	}

	var digests strings.Builder
	for i, name := range names {
		if err != nil || !isRegularRecoveryEntry(entry, name) {
			return ErrBackupScope
		}

		hash := sha256.New()
		if err := consume(i, io.TeeReader(archive, hash)); err != nil {
			return err
		}

		digests.WriteString(hex.EncodeToString(hash.Sum(nil)))

		entry, err = archive.Next()
	}

	if legacy {
		if err != io.EOF {
			return ErrBackupScope
		}

		return nil
	}

	if err != nil || !isRegularRecoveryEntry(entry, backupDigestEntry) || entry.Size != int64(digests.Len()) {
		return ErrCorrupt
	}

	return verifyRecoveryDigest(archive, digests.String())
}

func verifyRecoveryDigest(archive *tar.Reader, expected string) error {
	actual := make([]byte, len(expected))
	if _, err := io.ReadFull(archive, actual); err != nil {
		return err
	}

	if string(actual) != expected {
		return fmt.Errorf("%w: archive checksum mismatch", ErrCorrupt)
	}

	if _, err := archive.Next(); err != io.EOF {
		return ErrCorrupt
	}

	return nil
}

func verifyUpgradeBackup(ctx context.Context, path, expected string) error {
	return verifyRecoveryArchive(ctx, path, expected, readBackupPair)
}

func verifyRecoveryArchive(ctx context.Context, path, expected string, check func(*tar.Reader) error) error {
	file, err := openBackup(path)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()

	hash := sha256.New()

	reader := io.TeeReader(backupReader{ctx, file}, hash)
	if err = check(tar.NewReader(reader)); err != nil {
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
