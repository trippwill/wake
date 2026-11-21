// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
)

const initializeIntentFormat = "wake-release-initialize/1"

func ensureInstalled(ctx context.Context, profile Profile, logger *slog.Logger) error {
	paths := profile.paths()
	intentPath := paths[0] + RestoreIntentSuffix

	_, warehouseErr := os.Lstat(paths[0])
	_, intentErr := os.Lstat(intentPath)
	if !os.IsNotExist(warehouseErr) && os.IsNotExist(intentErr) {
		return nil
	}

	if err := validateStorePaths(paths, false); err != nil {
		return err
	}

	guard, err := lockStoreDirectory(paths[0])
	if err != nil {
		return err
	}
	defer func() { _ = guard.Close() }()

	if err := checkNoUpgrade(paths[0]); err != nil {
		return err
	}

	intent, err := readPublicationIntent(intentPath, initializeIntentFormat)
	if err != nil {
		return err
	}

	if intent == nil {
		if err := checkVacantStores(paths[:]); err != nil {
			if errors.Is(err, ErrRestoreOccupied) {
				return ErrAssociation
			}

			return err
		}

		intent, err = prepareInitialization(ctx, profile, intentPath, logger)
		if err != nil {
			return err
		}
	}

	if intent.Replace || intent.SyncOnly {
		return ErrRestorePending
	}

	if err := matchRestorePaths(intent, paths); err != nil {
		return err
	}

	return installRestore(ctx, intentPath, intent, nil)
}

// The destination directory stays locked until the initialized set is installed.
func prepareInitialization(
	ctx context.Context, profile Profile, intentPath string, logger *slog.Logger,
) (*restoreIntent, error) {
	paths := profile.paths()

	var staged [storeCount]string

	published := false
	defer func() {
		if !published {
			for _, path := range staged {
				if path != "" {
					_ = os.Remove(path)
				}
			}
		}
	}()

	for i, path := range paths {
		file, err := os.CreateTemp(filepath.Dir(path), ".wake-restore-")
		if err != nil {
			return nil, err
		}

		staged[i] = file.Name()
		if err := file.Close(); err != nil {
			return nil, err
		}
	}

	profile.WarehousePath, profile.ConnectionsPath, profile.SyncPath = staged[0], staged[1], staged[2]

	store, err := Open(ctx, profile, logger)
	if err != nil {
		return nil, err
	}

	defer func() { _ = store.Close() }()

	intent := restoreIntent{Format: initializeIntentFormat}
	for i, owner := range store.owners {
		if err := owner.Sync(); err != nil {
			return nil, err
		}

		intent.Stores[i], err = describeRestoreStore(ctx, paths[i], staged[i], owner, nil, i)
		if err != nil {
			return nil, err
		}
	}

	if err := errors.Join(ctx.Err(), store.Close()); err != nil {
		return nil, err
	}

	published, err = publishRestoreIntent(intentPath, intent)

	return &intent, err
}
