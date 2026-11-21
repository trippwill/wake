// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"context"
	"crypto/rand"
	"database/sql"
	"log/slog"
	"os"
	"path/filepath"
	"slices"

	"go.trippwill.dev/wake/internal/logging"
	"go.trippwill.dev/wake/internal/persistence/sqlite/sqlc"
)

// OpenFinancial opens a clean warehouse without attaching upstream storage.
// Staged backups and interrupted installation are checked before allowing writes.
func OpenFinancial(ctx context.Context, profile Profile, logger *slog.Logger) (result *Store, _ error) {
	if err := registerCollations(); err != nil {
		return nil, err
	}

	path := profile.WarehousePath

	guard, err := lockStoreDirectory(path)
	if err != nil {
		return nil, err
	}

	defer func() { _ = guard.Close() }()

	if err = checkFinancialMaintenance(path); err != nil {
		return nil, err
	}

	_, err = os.Lstat(path)
	fresh := os.IsNotExist(err)

	s := &Store{
		logger:        logging.Component(logger, "storage"),
		registrations: slices.Clone(profile.Registrations),
		historyRule:   releaseBaseline().rule,
	}
	defer func() {
		if result == nil {
			_ = s.Close()
		}
	}()

	if !fresh && err != nil {
		return nil, err
	}

	if !fresh {
		s.owners[0], err = lockStoreFile(path, false)
		if err != nil {
			return nil, err
		}
	}

	if err = checkUpgradeJournals(path); err != nil {
		return nil, err
	}

	if fresh {
		if err = s.createFinancial(ctx, path); err != nil {
			return nil, err
		}
	} else if _, err = validateStorageCopy(ctx, []string{path}, []*os.File{s.owners[0]}); err != nil {
		return nil, err
	}

	s.connectFinancial(path)

	if !fresh {
		if err = s.initializeFinancial(ctx, false); err != nil {
			return nil, err
		}
	}

	if err = s.openActivityReader(ctx, path); err != nil {
		return nil, err
	}

	return s, nil
}

func (s *Store) connectFinancial(path string) {
	s.db = sql.OpenDB(&connector{paths: [storeCount]string{path}, owners: s.owners})
	s.db.SetMaxOpenConns(1)
	s.db.SetMaxIdleConns(1)
	s.queries = sqlc.New(s.db)
}

func (s *Store) createFinancial(ctx context.Context, path string) error {
	staged, err := os.CreateTemp(filepath.Dir(path), ".wake-create-")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(staged.Name()) }()
	defer func() { _ = staged.Close() }()

	s.owners[0], err = lockStoreFile(staged.Name(), false)
	if err != nil {
		return err
	}

	s.connectFinancial(staged.Name())

	if err = s.initializeFinancial(ctx, true); err != nil {
		return err
	}

	if err = s.db.Close(); err != nil {
		return err
	}

	if err = staged.Sync(); err != nil {
		return err
	}

	return activateFinancial(ctx, staged.Name(), path)
}

// The caller holds the destination directory lock until publication is durable.
func activateFinancial(ctx context.Context, stage, path string) error {
	if err := checkVacantStore(path, false); err != nil {
		return err
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	if err := renameNewFile(stage, path); err != nil {
		return err
	}

	return syncUpgradeDir(path)
}

func checkFinancialMaintenance(path string) error {
	if err := rejectPendingRestore(path); err != nil {
		return err
	}

	return checkNoUpgrade(path)
}

func (s *Store) initializeFinancial(ctx context.Context, fresh bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if fresh {
		if _, err = tx.ExecContext(ctx, schemaSQL); err != nil {
			return err
		}

		if _, err = tx.ExecContext(
			ctx,
			profileStatement("PutMetadata", "main"),
			storeKinds[0],
			schemaVersion,
			rand.Text(),
		); err != nil {
			return err
		}

		if _, err = tx.ExecContext(ctx, profileStatement("InitializeLineage", ""), rand.Text()); err != nil {
			return err
		}
	}

	if err = s.prepareHistoryUpdate(ctx, s.queries.WithTx(tx), fresh); err != nil {
		return err
	}

	if _, err = tx.ExecContext(ctx, activityDateIndexesSQL+"\n"+activitySearchIndexSQL); err != nil {
		return err
	}

	return s.commit(ctx, tx)
}

// PreviewFinancial examines the supported financial format without opening the
// optional stores. The first release has no incoming format migrations.
func PreviewFinancial(ctx context.Context, path, confirm string) (UpgradePlan, error) {
	var plan UpgradePlan
	if err := checkFinancialMaintenance(path); err != nil {
		return plan, err
	}

	baseline := releaseBaseline()
	files := [storeCount]string{path}

	owner, err := lockStoreFile(path, false)
	if err != nil {
		if _, e := os.Lstat(path); os.IsNotExist(e) {
			if confirm != "" {
				return plan, ErrUpgradePlanChanged
			}

			if e = checkUpgradeJournals(path); e != nil && !os.IsNotExist(e) {
				return plan, e
			}

			plan, _, e = baseline.plan(files, [storeCount]storeMetadata{}, [storeCount]bool{}, historyVersion{})
			plan.Stores = plan.Stores[:1]

			return plan, e
		}

		return plan, err
	}

	defer func() { _ = owner.Close() }()

	if err = checkUpgradeJournals(path); err != nil {
		return plan, err
	}

	plan, err = validateStorageCopy(ctx, []string{path}, []*os.File{owner})
	if err != nil {
		return plan, err
	}

	if confirm != "" {
		return UpgradePlan{}, ErrUpgradePlanChanged
	}

	return plan, nil
}
