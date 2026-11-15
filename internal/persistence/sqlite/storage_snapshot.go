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
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

const (
	snapshotWarehouseEntry = "wake-snapshot/v1/warehouse.db"
	snapshotDigestEntry    = "wake-snapshot/v1/warehouse.sha256"
)

// SnapshotWarehouse copies committed pages through SQLite's online backup API.
// Only the warehouse is opened. SQLite coordinates locks with the daemon.
func SnapshotWarehouse(ctx context.Context, profile Profile, output string) error {
	source := profile.WarehousePath
	if err := rejectPendingRestore(source); err != nil {
		return err
	}

	if err := checkNoUpgrade(source); err != nil {
		return err
	}

	owner, err := openStoreFile(source, false, false)
	if err != nil {
		return err
	}
	defer func() { _ = owner.Close() }()

	staged, err := os.CreateTemp(filepath.Dir(source), ".wake-snapshot-")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(staged.Name()) }()
	defer func() { _ = staged.Close() }()

	if err := copyWarehouse(ctx, source, owner, staged.Name()); err != nil {
		return err
	}

	if err := checkFile(source, owner); err != nil {
		return err
	}

	if err := checkUpgradeJournals(staged.Name()); err != nil {
		return err
	}

	if _, err := validateStorageCopy(ctx, []string{staged.Name()}, []*os.File{staged}); err != nil {
		return err
	}

	info, err := staged.Stat()
	if err != nil {
		return err
	}

	_, err = writeRecoveryArchive(ctx, output, false, func(archive *tar.Writer) error {
		hash := sha256.New()
		reader := backupReader{ctx, io.NewSectionReader(staged, 0, info.Size())}

		if err := writeRecoveryEntry(
			archive,
			snapshotWarehouseEntry,
			info.Size(),
			io.TeeReader(reader, hash),
		); err != nil {
			return err
		}

		digest := hex.EncodeToString(hash.Sum(nil))

		return writeRecoveryEntry(archive, snapshotDigestEntry, int64(len(digest)), strings.NewReader(digest))
	}, func(archive *tar.Reader) error {
		return readWarehouseSnapshot(archive, io.Discard)
	})

	return err
}

func copyWarehouse(ctx context.Context, source string, owner *os.File, destination string) error {
	if err := registerCollations(); err != nil {
		return err
	}

	db := sql.OpenDB(
		&connector{paths: [storeCount]string{source}, owners: [storeCount]*os.File{owner}, activityRead: true},
	)
	defer func() { _ = db.Close() }()

	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()

	uri := (&url.URL{Scheme: "file", Path: destination, RawQuery: "mode=rw&_pragma=synchronous(EXTRA)"}).String()

	return conn.Raw(func(driverConnection any) (result error) {
		source, ok := driverConnection.(interface {
			NewBackup(string) (*sqlite.Backup, error)
		})
		if !ok {
			return ErrUnsafeStore
		}

		backup, err := source.NewBackup(uri)
		if err != nil {
			return err
		}

		defer func() { result = errors.Join(result, backup.Finish()) }()

		return stepWarehouseBackup(ctx, backup)
	})
}

func stepWarehouseBackup(ctx context.Context, backup *sqlite.Backup) error {
	var busySince time.Time
	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		more, err := backup.Step(256)
		if err == nil {
			if !more {
				return nil
			}

			busySince = time.Time{}

			continue
		}

		sqliteErr, ok := errors.AsType[*sqlite.Error](err)
		if !ok || sqliteErr.Code()&0xff != sqlite3.SQLITE_BUSY && sqliteErr.Code()&0xff != sqlite3.SQLITE_LOCKED {
			return err
		}

		if busySince.IsZero() {
			busySince = time.Now()
		} else if time.Since(busySince) >= 5*time.Second {
			return fmt.Errorf("warehouse remained busy during snapshot; retry: %w", err)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func readWarehouseSnapshot(archive *tar.Reader, destination io.Writer) error {
	entry, err := archive.Next()
	if err == nil && (entry.Name == backupEntries[0] || entry.Name == legacyBackupEntries[0]) {
		return fmt.Errorf("%w; add --with-credentials to restore the backup", ErrBackupScope)
	}

	if err != nil || !isRegularRecoveryEntry(entry, snapshotWarehouseEntry) {
		return ErrBackupScope
	}

	hash := sha256.New()
	//nolint:gosec // The checked member is non-sparse and cannot expand beyond its archive bytes.
	if _, err := io.Copy(io.MultiWriter(destination, hash), archive); err != nil {
		return err
	}

	entry, err = archive.Next()
	if err != nil || !isRegularRecoveryEntry(entry, snapshotDigestEntry) || entry.Size != sha256.Size*2 {
		return ErrCorrupt
	}

	return verifyRecoveryDigest(archive, hex.EncodeToString(hash.Sum(nil)))
}

func restoreWarehouseSnapshot(ctx context.Context, warehouse, archivePath string) error {
	if err := checkVacantStores([]string{warehouse}); err != nil {
		return err
	}

	archive, err := openBackup(archivePath)
	if err != nil {
		return err
	}
	defer func() { _ = archive.Close() }()

	file, err := os.CreateTemp(filepath.Dir(warehouse), ".wake-restore-")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(file.Name()) }()

	err = readWarehouseSnapshot(tar.NewReader(backupReader{ctx, archive}), file)

	if err = errors.Join(err, file.Sync(), file.Close()); err != nil {
		return err
	}

	owner, err := lockStoreFile(file.Name(), false)
	if err != nil {
		return err
	}
	defer func() { _ = owner.Close() }()

	if _, err := validateStorageCopy(ctx, []string{file.Name()}, []*os.File{owner}); err != nil {
		return err
	}

	if err := pauseRestoredWarehouse(ctx, file.Name(), ""); err != nil {
		return err
	}

	guard, err := lockStoreDirectory(warehouse)
	if err != nil {
		return err
	}
	defer func() { _ = guard.Close() }()

	if err := rejectPendingRestore(warehouse); err != nil {
		return err
	}

	if err := checkNoUpgrade(warehouse); err != nil {
		return err
	}

	if err := checkVacantStores([]string{warehouse}); err != nil {
		return err
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	if err := renameNewFile(file.Name(), warehouse); err != nil {
		return err
	}

	return syncUpgradeDir(warehouse)
}
