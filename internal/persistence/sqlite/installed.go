// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
)

func checkInstalledStores(ctx context.Context, paths [storeCount]string, owners [storeCount]*os.File) error {
	var hot bool
	for _, path := range paths {
		err := checkUpgradeJournals(path)
		if err != nil && !errors.Is(err, ErrUpgradeRecovery) {
			return err
		}

		hot = hot || err != nil
	}

	if hot {
		if err := recoverInstalledJournals(ctx, paths, owners); err != nil {
			return err
		}
	}

	_, err := validateStorageCopy(ctx, paths[:], owners[:])
	if err != nil || !hot {
		return err
	}

	for _, path := range paths {
		if err := discardColdJournal(path + "-journal"); err != nil {
			return err
		}

		if err := checkUpgradeJournals(path); err != nil {
			return err
		}
	}

	return nil
}

func recoverInstalledJournals(
	ctx context.Context,
	paths [storeCount]string,
	owners [storeCount]*os.File,
) error {
	// The stable identities survive partially flushed progress and authority
	// writes. The rotating witness is checked only after SQLite has recovered.
	probe, err := openInstallationCopy(ctx, paths, owners[0])
	if err != nil {
		return err
	}

	a, err := readAssociation(ctx, probe, false)
	if err := errors.Join(err, probe.Close()); err != nil {
		return err
	}

	if !a.epochMatches || a.restoring {
		return ErrAssociation
	}

	var journals [storeCount]string

	maximumSize := 0
	for i, path := range paths {
		journals[i] = path + "-journal"
		maximumSize += len(journals[i]) + 1
	}

	for _, path := range paths {
		sidecars, err := storeSidecars(path)
		if err != nil {
			return err
		}

		for _, super := range sidecars[3:] {
			if err := checkSuperJournal(super, journals, maximumSize); err != nil {
				return err
			}
		}
	}

	db := sql.OpenDB(&connector{paths: paths, owners: owners})
	defer func() { _ = db.Close() }()

	db.SetMaxOpenConns(1)

	for _, schema := range storeSchemas {
		var kind, version, id string
		if err := db.QueryRowContext(ctx, profileStatement("Metadata", schema)).
			Scan(&kind, &version, &id); err != nil {
			return err
		}
	}

	return nil
}

// SQLite ignores a rollback journal whose header was never made hot. Remove
// those remnants only after SQLite has opened and validated the entire set.
func discardColdJournal(path string) error {
	file, err := openBackup(path)
	if os.IsNotExist(err) {
		return nil
	}

	if err != nil {
		return err
	}

	var magic [8]byte

	_, err = io.ReadFull(file, magic[:])
	if errors.Is(err, io.EOF) {
		err = nil
	}

	if err = errors.Join(err, file.Close()); err != nil {
		return errors.Join(ErrUpgradeRecovery, err)
	}

	if magic != ([8]byte{}) {
		return ErrUpgradeRecovery
	}

	if err := os.Remove(path); err != nil {
		return err
	}

	return syncUpgradeDir(path)
}

func checkSuperJournal(path string, journals [storeCount]string, limit int) error {
	file, err := openBackup(path)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()

	data, err := io.ReadAll(io.LimitReader(file, int64(limit)+1))
	if err != nil || len(data) == 0 || len(data) > limit || data[len(data)-1] != 0 {
		return ErrUpgradeRecovery
	}

	seen := make(map[string]bool, storeCount)
	for _, member := range bytes.Split(data[:len(data)-1], []byte{0}) {
		name := string(member)
		if name != journals[0] && name != journals[1] && name != journals[2] || seen[name] {
			return ErrUpgradeRecovery
		}

		info, err := os.Lstat(name)
		if err != nil || !info.Mode().IsRegular() || !privateOwned(info) || !singleLink(info) {
			return ErrUpgradeRecovery
		}

		seen[name] = true
	}

	return nil
}
