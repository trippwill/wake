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
	for i, store := range paths {
		if store == "" {
			continue
		}

		for _, other := range paths[:i] {
			if other != "" && (overlapsStore(store, other) || overlapsStore(other, store)) {
				return ErrUnsafeStore
			}
		}

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

// Backup saves a verified, stopped installation without separate file evidence.
func Backup(ctx context.Context, profile Profile, path string) error {
	return BackupInstallation(ctx, profile, path, InstallationEvidence{})
}

// InstallationEvidence is captured under stopped-instance ownership. The
// configuration and local auth entries are never activated by restore.
type InstallationEvidence struct {
	Configuration []byte
	LocalAuth     []byte
}

func BackupInstallation(
	ctx context.Context,
	profile Profile,
	path string,
	evidence InstallationEvidence,
) error {
	if err := rejectPendingRestore(profile.WarehousePath); err != nil {
		return err
	}

	var owners [storeCount]*os.File
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

	if err := rejectPendingRestore(profile.WarehousePath); err != nil {
		return err
	}

	paths := profile.paths()

	plan, err := validateStorageCopy(ctx, paths[:], owners[:])
	if err != nil {
		return err
	}

	return writeInstallationBackup(ctx, path, paths, owners, plan, evidence)
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

func validateStorageCopy(ctx context.Context, paths []string, owners []*os.File) (UpgradePlan, error) {
	return validateStorageSet(ctx, paths, owners, releaseBaseline())
}

func validateStorageSet(
	ctx context.Context, paths []string, owners []*os.File, baseline upgradeBaseline,
) (UpgradePlan, error) {
	var plan UpgradePlan
	if len(paths) != len(owners) || (len(paths) != 1 && len(paths) != storeCount) {
		return plan, ErrUnsupportedSchema
	}

	if err := registerCollations(); err != nil {
		return plan, err
	}

	var (
		metadata [storeCount]storeMetadata
		history  historyVersion
		accessID string
	)

	for i, path := range paths {
		db, m, err := inspectStorageMemberAt(ctx, path, owners[i], i, baseline)
		if err != nil {
			return plan, err
		}
		defer func() { _ = db.Close() }()

		metadata[i] = m

		if i == 0 {
			if err := db.QueryRowContext(ctx, profileStatement("HistoryVersion", "main")).
				Scan(&history.applied, &history.target); err != nil {
				return plan, err
			}

			var lineage string
			if err := db.QueryRowContext(ctx, profileStatement("FinancialLineage", "")).
				Scan(&lineage); err != nil ||
				lineage == "" {
				return plan, errors.Join(ErrCorrupt, err)
			}
		}

		if i == 2 {
			if err := db.QueryRowContext(ctx, profileStatement("Profile", "main")).Scan(&accessID); err != nil {
				return plan, err
			}
		}
	}

	var storePaths [storeCount]string
	copy(storePaths[:], paths)

	full := len(paths) == storeCount

	plan, _, err := baseline.plan(storePaths, metadata, [storeCount]bool{true, full, full}, history)
	if err != nil {
		return plan, err
	}

	if !full {
		plan.Stores = plan.Stores[:1]
		return plan, nil
	}

	if accessID != metadata[1].id {
		return plan, ErrAssociation
	}

	db, err := openInstallationCopy(ctx, storePaths, owners[0])
	if err != nil {
		return plan, err
	}
	defer func() { _ = db.Close() }()

	var invalid int
	if err := db.QueryRowContext(ctx, profileStatement("AssociationCheck", "")).Scan(&invalid); err != nil {
		return plan, err
	}

	if invalid != 0 {
		return plan, ErrAssociation
	}

	association, err := readUpstreamAssociation(ctx, db)
	if err != nil {
		return plan, err
	}

	if !association.epochMatches || association.restoring {
		return plan, ErrAssociation
	}

	return plan, nil
}

func inspectStorageMember(
	ctx context.Context,
	path string,
	owner *os.File,
	index int,
) (*sql.DB, storeMetadata, error) {
	return inspectStorageMemberAt(ctx, path, owner, index, releaseBaseline())
}

func inspectStorageMemberAt(
	ctx context.Context, path string, owner *os.File, index int, baseline upgradeBaseline,
) (*sql.DB, storeMetadata, error) {
	var metadata storeMetadata

	db := sql.OpenDB(
		&connector{paths: [storeCount]string{path}, owners: [storeCount]*os.File{owner}, probe: true},
	)
	db.SetMaxOpenConns(1)

	err := db.QueryRowContext(ctx, profileStatement("Metadata", "main")).Scan(
		&metadata.kind, &metadata.version, &metadata.id,
	)
	if err != nil || metadata.kind != storeKinds[index] || metadata.version != baseline.formats[index] ||
		metadata.id == "" {
		_ = db.Close()
		return nil, metadata, errors.Join(ErrUnsupportedSchema, err)
	}

	err = validateCopySchema(ctx, db, baseline.schemas[index], storeSchemas[index])
	if err == nil {
		var integrity string

		err = db.QueryRowContext(ctx, profileStatement("Integrity", "main")).Scan(&integrity)
		if err == nil && integrity != "ok" {
			err = ErrCorrupt
		}
	}

	if err == nil {
		err = checkCopyForeignKeys(ctx, db)
	}

	if err != nil {
		_ = db.Close()
		return nil, metadata, err
	}

	return db, metadata, nil
}

func openInstallationCopy(ctx context.Context, paths [storeCount]string, owner *os.File) (*sql.DB, error) {
	db := sql.OpenDB(
		&connector{paths: [storeCount]string{paths[0]}, owners: [storeCount]*os.File{owner}, probe: true},
	)
	db.SetMaxOpenConns(1)

	for i, path := range paths[1:] {
		schema := storeSchemas[i+1]
		uri := (&url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro&immutable=1"}).String()
		if _, err := db.ExecContext(ctx, profileStatement("Attach", schema), uri); err != nil {
			_ = db.Close()
			return nil, err
		}
	}

	return db, nil
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

	if schema != "main" {
		if _, err := reference.ExecContext(ctx, profileStatement("Attach", schema), ":memory:"); err != nil {
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
