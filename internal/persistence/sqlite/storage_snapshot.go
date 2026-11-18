// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"time"

	recoveryarchive "go.trippwill.dev/wake/internal/recovery/archive"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// SnapshotWarehouse copies committed pages through SQLite's online backup API.
// Only the warehouse is opened. SQLite coordinates locks with the daemon.
func SnapshotWarehouse(ctx context.Context, profile Profile, output string) error {
	source := profile.WarehousePath
	if err := checkFinancialMaintenance(source); err != nil {
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

	input, err := backupInput(ctx, staged)
	if err != nil {
		return err
	}

	_, err = writeRecoveryArchive(ctx, output, false, func(output io.Writer) error {
		return recoveryarchive.WriteSnapshot(output, input)
	}, func(input io.Reader) error {
		return readWarehouseSnapshot(input, io.Discard)
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

func readWarehouseSnapshot(input io.Reader, destination io.Writer) error {
	return archiveReadError(recoveryarchive.ReadSnapshot(input, destination))
}

func restoreWarehouseSnapshot(ctx context.Context, warehouse, archivePath string) error {
	if err := checkVacantStore(warehouse, false); err != nil {
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

	err = readWarehouseSnapshot(backupReader{ctx, archive}, file)

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

	if err := renewRestoredStores(ctx, [storeCount]string{file.Name()}, recoveryPacing{}); err != nil {
		return err
	}

	guard, err := lockStoreDirectory(warehouse)
	if err != nil {
		return err
	}
	defer func() { _ = guard.Close() }()

	if err := checkFinancialMaintenance(warehouse); err != nil {
		return err
	}

	return activateFinancial(ctx, file.Name(), warehouse)
}
