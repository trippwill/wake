// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"context"
	"crypto/rand"
	"io"
	"os"
	"time"

	recoveryarchive "go.trippwill.dev/wake/internal/recovery/archive"
)

func readInstallationMetadata(
	ctx context.Context, paths [storeCount]string, owner *os.File, plan UpgradePlan,
) (recoveryarchive.Metadata, error) {
	var metadata recoveryarchive.Metadata
	for i, store := range plan.Stores {
		metadata.Stores[i] = recoveryarchive.Store{Kind: store.Kind, ID: store.ID, Format: store.Current}
	}

	db, err := openInstallationCopy(ctx, paths, owner)
	if err != nil {
		return metadata, err
	}
	defer func() { _ = db.Close() }()

	if err := db.QueryRowContext(ctx, profileStatement("BackupAssociation", "")).Scan(
		&metadata.Lineage, &metadata.Generation, &metadata.SafetyToken,
	); err != nil {
		return metadata, err
	}

	err = db.QueryRowContext(ctx, profileStatement("HistoryVersion", "main")).Scan(
		&metadata.AppliedRule, &metadata.TargetRule,
	)

	return metadata, err
}

func writeInstallationBackup(
	ctx context.Context,
	path string,
	paths [storeCount]string,
	owners [storeCount]*os.File,
	plan UpgradePlan,
	evidence InstallationEvidence,
) error {
	metadata, err := readInstallationMetadata(ctx, paths, owners[0], plan)
	if err != nil {
		return err
	}

	inputs, err := backupInputs(ctx, owners)
	if err != nil {
		return err
	}

	manifest := recoveryarchive.Manifest{BackupID: rand.Text(), CapturedAt: time.Now().UTC(), Storage: metadata}
	_, err = writeRecoveryArchive(ctx, path, false, func(output io.Writer) error {
		return recoveryarchive.WriteInstallation(
			output,
			inputs,
			manifest,
			evidence.Configuration,
			evidence.LocalAuth,
		)
	}, func(input io.Reader) error {
		_, err := consumeRestoreArchive(input, func(_ int, member io.Reader) error {
			_, err := io.Copy(io.Discard, member)

			return err
		})

		return err
	})

	return err
}

func consumeRestoreArchive(
	input io.Reader,
	stage func(int, io.Reader) error,
) (*recoveryarchive.Manifest, error) {
	manifest, err := recoveryarchive.ReadInstallation(input, stage)

	return manifest, archiveReadError(err)
}
