// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"go.trippwill.dev/wake/internal/recovery"
)

// RecoverSynchronization rebuilds optional state from a clean warehouse. The
// confirmation binds the bytes of the selected stores and the exact paths.
func RecoverSynchronization(
	ctx context.Context,
	profile Profile,
	archivePath, confirm string,
) (recovery.SyncPlan, error) {
	paths := profile.paths()
	plan := recovery.SyncPlan{Stores: paths}

	if err := profile.CheckArtifactPath(archivePath, false); err != nil {
		return plan, err
	}

	guard, err := lockStoreDirectory(paths[0])
	if err != nil {
		return plan, err
	}
	defer func() { _ = guard.Close() }()

	if err := checkNoUpgrade(paths[0]); err != nil {
		return plan, err
	}

	intentPath := paths[0] + RestoreIntentSuffix

	intent, err := readRestoreIntent(intentPath)
	if err != nil {
		return plan, err
	}

	if intent != nil {
		if !intent.SyncOnly || !intent.Replace {
			return plan, ErrRestorePending
		}

		if err := matchRestorePaths(intent, paths); err != nil {
			return plan, err
		}

		plan.ID, plan.Pending = intent.ArchiveDigest, true
		plan.RetainAccess = intent.Stores[1].Original != nil
		err = plan.CheckConfirmation(confirm)
	} else {
		plan, err = planSyncRecovery(ctx, paths, intentPath, archivePath, confirm)
	}

	if err != nil || confirm == "" {
		return plan, err
	}

	intent, err = readRestoreIntent(intentPath)
	if err != nil || intent == nil {
		return plan, errors.Join(ErrRestorePending, err)
	}

	if err := installRestore(ctx, intentPath, intent, nil); err != nil {
		return plan, err
	}

	plan.Pending, plan.Applied = false, true

	return plan, nil
}

func planSyncRecovery(
	ctx context.Context, paths [storeCount]string, intentPath, archivePath, confirm string,
) (recovery.SyncPlan, error) {
	plan := recovery.SyncPlan{Stores: paths}

	var (
		originals [storeCount]*os.File
		inputs    [storeCount]string
	)
	for i, path := range paths {
		owner, err := lockRestoreOriginal(path, true)
		if err != nil {
			return plan, err
		}

		if owner == nil {
			if i == 0 {
				return plan, os.ErrNotExist
			}

			continue
		}
		defer func() { _ = owner.Close() }()

		inputs[i], err = restoreDigest(ctx, owner)
		if err != nil {
			return plan, err
		}

		originals[i] = owner
	}

	if _, err := validateStorageCopy(ctx, paths[:1], originals[:1]); err != nil {
		return plan, err
	}

	var (
		access storeMetadata
		pacing recoveryPacing
	)
	if originals[1] != nil {
		db, metadata, err := inspectStorageMember(ctx, paths[1], originals[1], 1)
		if err != nil {
			return plan, err
		}

		_ = db.Close()
		access, plan.RetainAccess = metadata, true
	}

	if originals[2] != nil {
		db, err := inspectRecoverableSync(ctx, paths[2], originals[2])
		if err == nil {
			plan.Current, _ = readSyncConfigurations(ctx, db)
			pacing, err = readRecoveryPacing(ctx, db)
			if err := errors.Join(err, db.Close()); err != nil {
				return plan, err
			}
		}
	}

	var (
		archiveDigest string
		err           error
	)
	if archivePath != "" {
		var archived recoveryPacing

		plan.Proposed, archived, archiveDigest, err = readSyncRecoveryArchive(
			ctx,
			archivePath,
			paths[0],
			originals[0],
		)
		if err != nil {
			return plan, err
		}

		pacing.deadlines = append(pacing.deadlines, archived.deadlines...)
		if archived.reference.After(pacing.reference) {
			pacing.reference = archived.reference
		}
	}

	plan.ID, err = recovery.SyncPlanID(paths, inputs, archiveDigest)
	if err != nil {
		return plan, err
	}

	if err = plan.CheckConfirmation(confirm); err != nil || confirm == "" {
		return plan, err
	}

	return plan, prepareSyncRecovery(ctx, paths, intentPath, plan, pacing, originals, access)
}

func readSyncRecoveryArchive(
	ctx context.Context, archivePath, warehouse string, financialOwner *os.File,
) ([]recovery.SyncConfiguration, recoveryPacing, string, error) {
	archive, err := openBackup(archivePath)
	if err != nil {
		return nil, recoveryPacing{}, "", err
	}
	defer func() { _ = archive.Close() }()

	var (
		copies [storeCount]string
		owners [storeCount]*os.File
	)
	defer func() {
		closeRestoreCopies(owners)

		for _, path := range copies {
			if path != "" {
				_ = os.Remove(path)
			}
		}
	}()

	destinations := [storeCount]string{warehouse, warehouse, warehouse}

	manifest, archiveDigest, err := copyRestoreArchive(ctx, archive, destinations, &copies, &owners)
	if err != nil {
		return nil, recoveryPacing{}, "", err
	}

	if manifest == nil {
		return nil, recoveryPacing{}, "", ErrBackupScope
	}

	financial, _, err := inspectStorageMember(ctx, warehouse, financialOwner, 0)
	if err != nil {
		return nil, recoveryPacing{}, "", err
	}
	defer func() { _ = financial.Close() }()

	var lineage string
	if err := financial.QueryRowContext(ctx, profileStatement("FinancialLineage", "")).
		Scan(&lineage); err != nil {
		return nil, recoveryPacing{}, "", err
	}

	if lineage != manifest.Storage.Lineage {
		return nil, recoveryPacing{}, "", ErrAssociation
	}

	baseline := releaseBaseline()
	if manifest.Storage.Stores[2].Format == syncArchiveVersion {
		baseline = installationArchiveBaseline()
	}

	sync, _, err := inspectStorageMemberAt(ctx, copies[2], owners[2], 2, baseline)
	if err != nil {
		return nil, recoveryPacing{}, "", err
	}

	defer func() { _ = sync.Close() }()

	configs, err := readSyncConfigurations(ctx, sync)
	if err != nil {
		return nil, recoveryPacing{}, "", err
	}

	for _, config := range configs {
		var providerID string
		if err := financial.QueryRowContext(ctx, profileStatement("RecoveryDescriptor", ""), config.ID).
			Scan(&providerID); err != nil || providerID != config.Provider {
			return nil, recoveryPacing{}, "", errors.Join(ErrAssociation, err)
		}
	}

	pacing, err := readRecoveryPacing(ctx, sync)

	return configs, pacing, archiveDigest, err
}

func readSyncConfigurations(ctx context.Context, db *sql.DB) ([]recovery.SyncConfiguration, error) {
	rows, err := db.QueryContext(ctx, profileStatement("SyncRecoveryConfigurations", "main"))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var result []recovery.SyncConfiguration
	for rows.Next() {
		var config recovery.SyncConfiguration
		if err := rows.Scan(&config.ID, &config.Provider, &config.Name, &config.Settings); err != nil {
			return nil, err
		}

		if _, _, err := decodeConnectionSettings(config.Settings); err != nil {
			return nil, err
		}

		result = append(result, config)
	}

	return result, rows.Err()
}

func prepareSyncRecovery(
	ctx context.Context, paths [storeCount]string, intentPath string, plan recovery.SyncPlan,
	pacing recoveryPacing, originals [storeCount]*os.File, access storeMetadata,
) error {
	intent := restoreIntent{ArchiveDigest: plan.ID, SyncOnly: true, Replace: true}

	staged := [storeCount]string{paths[0]}
	owners := [storeCount]*os.File{originals[0]}
	published := false
	for i := 1; i < storeCount; i++ {
		file, err := os.CreateTemp(filepath.Dir(paths[i]), ".wake-restore-")
		if err != nil {
			return err
		}
		defer func() {
			if !published {
				_ = os.Remove(file.Name())
			}
		}()

		staged[i] = file.Name()
		if i == 1 && originals[1] != nil {
			_, err = io.Copy(file, backupReader{ctx, originals[1]})
		}

		if err = errors.Join(err, file.Close()); err != nil {
			return err
		}

		owners[i], err = lockStoreFile(staged[i], false)
		if err != nil {
			return err
		}
		defer func() { _ = owners[i].Close() }()
	}

	if err := buildSyncRecovery(ctx, staged, owners, access, plan.Proposed, pacing); err != nil {
		return err
	}

	if _, err := validateStorageCopy(ctx, staged[:], owners[:]); err != nil {
		return err
	}

	warehouse, err := describeRestoreStore(ctx, paths[0], "", owners[0], nil, 0)
	if err != nil {
		return err
	}

	intent.Stores[0] = warehouse

	for i := 1; i < storeCount; i++ {
		if err := owners[i].Sync(); err != nil {
			return err
		}

		store, err := describeRestoreStore(ctx, paths[i], staged[i], owners[i], originals[i], i)
		if err != nil {
			return err
		}

		intent.Stores[i] = store
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	published, err = publishRestoreIntent(intentPath, intent)

	return err
}

func buildSyncRecovery(
	ctx context.Context,
	paths [storeCount]string,
	owners [storeCount]*os.File,
	access storeMetadata,
	proposed []recovery.SyncConfiguration,
	pacing recoveryPacing,
) error {
	db := sql.OpenDB(
		&connector{paths: [storeCount]string{paths[0]}, owners: [storeCount]*os.File{owners[0]}, probe: true},
	)
	defer func() { _ = db.Close() }()

	db.SetMaxOpenConns(1)

	for i, path := range paths[1:] {
		schema := storeSchemas[i+1]
		uri := (&url.URL{Scheme: "file", Path: path, RawQuery: "mode=rw"}).String()
		if _, err := db.ExecContext(ctx, profileStatement("Attach", schema), uri); err != nil {
			return err
		}

		if _, err := db.ExecContext(ctx, profileStatement("Sync", schema)); err != nil {
			return err
		}
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var financial storeMetadata
	if err := tx.QueryRowContext(ctx, profileStatement("Metadata", "main")).Scan(
		&financial.kind, &financial.version, &financial.id,
	); err != nil {
		return err
	}

	sync := storeMetadata{kind: storeKinds[2], version: syncSchemaVersion, id: rand.Text()}
	if access.id == "" {
		access = storeMetadata{kind: storeKinds[1], version: accessSchemaVersion, id: rand.Text()}
		if err := createRecoveryMember(ctx, tx, access, 1); err != nil {
			return err
		}
	}

	if err := createRecoveryMember(ctx, tx, sync, 2); err != nil {
		return err
	}

	if _, err := tx.ExecContext(
		ctx,
		profileStatement("InitializeRetainedAccessProfile", "sync"),
		access.id,
	); err != nil {
		return err
	}

	if err := prepareRecoveredProfile(ctx, tx, time.Now()); err != nil {
		return err
	}

	if err := bindUpstream(
		ctx,
		tx,
		[storeCount]storeMetadata{financial, access, sync},
		false,
		true,
	); err != nil {
		return err
	}

	for _, config := range proposed {
		if _, err := tx.ExecContext(ctx, profileStatement("ImportSyncConfiguration", ""),
			config.ID, config.Provider, config.Name, []byte(config.Settings)); err != nil {
			return err
		}
	}

	if err := preserveRecoveryPacing(ctx, tx, pacing); err != nil {
		return err
	}

	return tx.Commit()
}

func createRecoveryMember(ctx context.Context, tx *sql.Tx, metadata storeMetadata, index int) error {
	if _, err := tx.ExecContext(ctx, releaseBaseline().schemas[index]); err != nil {
		return err
	}

	_, err := tx.ExecContext(
		ctx,
		profileStatement("PutMetadata", storeSchemas[index]),
		metadata.kind,
		metadata.version,
		metadata.id,
	)

	return err
}
