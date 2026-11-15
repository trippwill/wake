// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"context"
	"crypto/rand"
	"database/sql"
	"log/slog"
	"os"
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

	if err = rejectPendingRestore(path); err != nil {
		return nil, err
	}

	if err = checkNoUpgrade(path); err != nil {
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
		s.owners[0], err = claimFile(path)
	} else {
		err = validateStorageCopy(ctx, []string{path}, []*os.File{s.owners[0]})
	}

	if err != nil {
		return nil, err
	}

	s.db = sql.OpenDB(&connector{paths: [2]string{path}, owners: s.owners})
	s.db.SetMaxOpenConns(1)
	s.db.SetMaxIdleConns(1)

	s.queries = sqlc.New(s.db)
	if err = s.initializeFinancial(ctx, fresh); err != nil {
		return nil, err
	}

	if err = s.openActivityReader(ctx, path); err != nil {
		return nil, err
	}

	return s, nil
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
	}

	if err = s.prepareHistoryUpdate(ctx, s.queries.WithTx(tx), fresh); err != nil {
		return err
	}

	if _, err = tx.ExecContext(ctx, profileStatement("InterruptAttempts", "")); err != nil {
		return err
	}

	if _, err = tx.ExecContext(ctx, activityDateIndexesSQL); err != nil {
		return err
	}

	if _, err = tx.ExecContext(ctx, activitySearchIndexSQL); err != nil {
		return err
	}

	return s.commit(ctx, tx)
}
