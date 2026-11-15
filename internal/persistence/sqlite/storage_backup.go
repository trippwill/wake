// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
)

const ErrBackupScope Error = "archive does not match the requested restore scope"

// CheckArtifactPath reserves the database, journal and maintenance paths.
func (p Profile) CheckArtifactPath(path string, creating bool) error {
	paths := p.paths()
	if overlapsStore(paths[0], paths[1]) || overlapsStore(paths[1], paths[0]) {
		return ErrUnsafeStore
	}

	for _, store := range paths {
		if !creating && store == p.WarehousePath && path == store+upgradeBackupSuffix {
			continue
		}

		if overlapsStore(path, store) {
			return fmt.Errorf("%w: %s", ErrUnsafeStore, path)
		}
	}

	return nil
}

func overlapsStore(path, store string) bool {
	for _, suffix := range []string{"", "-journal", "-wal", "-shm", UpgradeIntentSuffix, upgradeBackupSuffix, RestoreIntentSuffix} {
		if path == store+suffix {
			return true
		}
	}

	return strings.HasPrefix(path, store+"-mj")
}

// Backup saves a verified, stopped warehouse and access store in the same
// archive format as the automatic pre-upgrade backup.
func Backup(ctx context.Context, profile Profile, path string) error {
	if err := rejectPendingRestore(profile.WarehousePath); err != nil {
		return err
	}

	var owners [2]*os.File
	for i, source := range profile.paths() {
		file, err := lockStoreFile(source, false)
		if err != nil {
			return fmt.Errorf("open %s with Wake stopped: %w", source, err)
		}
		defer func() { _ = file.Close() }()

		owners[i] = file

		if err := checkUpgradeJournals(source); err != nil {
			return fmt.Errorf("%s: %w", source, err)
		}
	}

	if err := checkNoUpgrade(profile.WarehousePath); err != nil {
		return err
	}

	paths := profile.paths()
	if err := validateStorageCopy(ctx, paths[:], owners[:]); err != nil {
		return err
	}

	_, err := writeBackupPair(ctx, path, owners, false)

	return err
}

func checkNoUpgrade(warehouse string) error {
	intent, err := readUpgradeIntent(warehouse + UpgradeIntentSuffix)
	if err != nil {
		return err
	}

	if intent.Plan.ID != "" {
		return ErrUpgradeRecovery
	}

	return nil
}

func validateStorageCopy(ctx context.Context, paths []string, owners []*os.File) error {
	if err := registerCollations(); err != nil {
		return err
	}

	baseline := releaseBaseline()

	var (
		metadata [2]storeMetadata
		history  historyVersion
		accessID string
	)

	for i, path := range paths {
		db := sql.OpenDB(&connector{paths: [2]string{path}, owners: [2]*os.File{owners[i]}, probe: true})
		defer func() { _ = db.Close() }()

		db.SetMaxOpenConns(1)

		m := &metadata[i]

		err := db.QueryRowContext(ctx, profileStatement("Metadata", "main")).Scan(&m.kind, &m.version, &m.id)
		if err != nil {
			return errors.Join(ErrUnsupportedSchema, err)
		}

		if m.kind != storeKinds[i] || m.version != baseline.formats[i] || m.id == "" {
			return ErrUnsupportedSchema
		}

		if err := validateCopySchema(ctx, db, baseline.schemas[i], storeSchemas[i]); err != nil {
			return err
		}

		var integrity string

		err = db.QueryRowContext(ctx, profileStatement("Integrity", "main")).Scan(&integrity)
		if err != nil {
			return err
		}

		if integrity != "ok" {
			return ErrCorrupt
		}

		if err := checkCopyForeignKeys(ctx, db); err != nil {
			return err
		}

		if i == 0 {
			if err := db.QueryRowContext(ctx, profileStatement("HistoryVersion", "main")).
				Scan(&history.applied, &history.target); err != nil {
				return err
			}

			if err := db.QueryRowContext(ctx, profileStatement("Profile", "")).Scan(&accessID); err != nil {
				return err
			}
		}
	}

	if _, _, err := baseline.plan([2]string{}, metadata, [2]bool{true, len(paths) == 2}, history); err != nil {
		return err
	}

	if len(paths) == 1 {
		return nil
	}

	if accessID != metadata[1].id {
		return ErrAssociation
	}

	db := sql.OpenDB(&connector{paths: [2]string{paths[0]}, owners: [2]*os.File{owners[0]}, probe: true})
	defer func() { _ = db.Close() }()

	db.SetMaxOpenConns(1)

	uri := (&url.URL{Scheme: "file", Path: paths[1], RawQuery: "mode=ro&immutable=1"}).String()
	if _, err := db.ExecContext(ctx, profileStatement("AttachAccess", ""), uri); err != nil {
		return err
	}

	var invalid int
	if err := db.QueryRowContext(ctx, profileStatement("AssociationCheck", "")).Scan(&invalid); err != nil {
		return err
	}

	if invalid != 0 {
		return ErrAssociation
	}

	association, err := readUpstreamAssociation(ctx, db)
	if err != nil {
		return err
	}

	if !association.epochMatches || association.restoring {
		return ErrAssociation
	}

	return nil
}

type recoverySchemaObject struct{ kind, name, definition string }

// Compare the catalog against the schema of the declared format. Build the
// reference independently so schema content from the archive is never executed.
func validateCopySchema(ctx context.Context, db *sql.DB, definition, schema string) error {
	// Use the registered driver with one connection so the reference stays in memory.
	reference, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return err
	}
	defer func() { _ = reference.Close() }()

	reference.SetMaxOpenConns(1)

	if schema == "access" {
		if _, err := reference.ExecContext(ctx, profileStatement("AttachAccess", ""), ":memory:"); err != nil {
			return err
		}
	}

	if _, err := reference.ExecContext(ctx, definition); err != nil {
		return err
	}

	required, err := readRecoverySchema(ctx, reference, schema)
	if err != nil {
		return err
	}

	allowed := required
	if schema == "main" {
		// Startup can recreate these indexes if an older archive lacks them.
		if _, err := reference.ExecContext(ctx, activityDateIndexesSQL+"\n"+activitySearchIndexSQL); err != nil {
			return err
		}

		allowed, err = readRecoverySchema(ctx, reference, schema)
		if err != nil {
			return err
		}
	}

	actual, err := readRecoverySchema(ctx, db, "main")
	if err != nil {
		return errors.Join(ErrUnsupportedSchema, err)
	}

	for object := range required {
		if _, ok := actual[object]; !ok {
			return fmt.Errorf("%w: missing or changed %s %s", ErrUnsupportedSchema, object.kind, object.name)
		}
	}

	for object := range actual {
		if _, ok := allowed[object]; !ok {
			return fmt.Errorf("%w: unexpected %s %s", ErrUnsupportedSchema, object.kind, object.name)
		}
	}

	return nil
}

func readRecoverySchema(
	ctx context.Context, db *sql.DB, schema string,
) (map[recoverySchemaObject]struct{}, error) {
	rows, err := db.QueryContext(ctx, profileStatement("RecoverySchema", schema))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	objects := make(map[recoverySchemaObject]struct{})
	for rows.Next() {
		var object recoverySchemaObject
		if err := rows.Scan(&object.kind, &object.name, &object.definition); err != nil {
			return nil, err
		}

		objects[object] = struct{}{}
	}

	return objects, rows.Err()
}

func checkCopyForeignKeys(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, profileStatement("ForeignCheck", "main"))
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()

	if rows.Next() {
		return ErrCorrupt
	}

	return rows.Err()
}
