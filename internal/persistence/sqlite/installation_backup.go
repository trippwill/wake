// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"
)

const installationManifestEntry = "wake-installation/v1/manifest.json"

var installationEntries = [...]string{
	"wake-installation/v1/warehouse.db",
	"wake-installation/v1/access.db",
	"wake-installation/v1/sync.db",
	"wake-installation/v1/config.toml",
	"wake-installation/v1/auth.json",
}

type installationManifest struct {
	BackupID   string               `json:"backup_id"`
	CapturedAt time.Time            `json:"captured_at"`
	Scope      string               `json:"scope"`
	Storage    installationMetadata `json:"storage"`
	Members    []installationMember `json:"members"`
}

type installationMetadata struct {
	Stores      [storeCount]installationStore `json:"stores"`
	Lineage     string                        `json:"financial_lineage"`
	Generation  string                        `json:"association_generation"`
	SafetyToken string                        `json:"safety_token"`
	AppliedRule int64                         `json:"applied_rule"`
	TargetRule  int64                         `json:"target_rule"`
}

type installationStore struct {
	Kind   string `json:"kind"`
	ID     string `json:"store_id"`
	Format string `json:"format"`
}

type installationMember struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	Digest string `json:"sha256"`
}

func readInstallationMetadata(
	ctx context.Context, paths [storeCount]string, owner *os.File, plan UpgradePlan,
) (installationMetadata, error) {
	var metadata installationMetadata
	for i, store := range plan.Stores {
		metadata.Stores[i] = installationStore{store.Kind, store.ID, store.Current}
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

	_, err = writeRecoveryArchive(ctx, path, false, func(archive *tar.Writer) error {
		manifest := installationManifest{
			BackupID: rand.Text(), CapturedAt: time.Now().UTC(), Scope: "installation", Storage: metadata,
		}
		for i, owner := range owners {
			info, err := owner.Stat()
			if err != nil {
				return err
			}

			entry, err := writeInstallationMember(
				archive,
				installationEntries[i],
				info.Size(),
				backupReader{ctx, io.NewSectionReader(owner, 0, info.Size())},
			)
			if err != nil {
				return err
			}

			manifest.Members = append(manifest.Members, entry)
		}

		for i, data := range [][]byte{evidence.Configuration, evidence.LocalAuth} {
			if len(data) == 0 {
				continue
			}

			entry, err := writeInstallationMember(
				archive,
				installationEntries[storeCount+i],
				int64(len(data)),
				bytes.NewReader(data),
			)
			if err != nil {
				return err
			}

			manifest.Members = append(manifest.Members, entry)
		}

		data, err := json.Marshal(manifest)
		if err != nil {
			return err
		}

		return writeRecoveryEntry(archive, installationManifestEntry, int64(len(data)), bytes.NewReader(data))
	}, func(archive *tar.Reader) error {
		_, err := consumeRestoreArchive(archive, func(_ int, member io.Reader) error {
			_, err := io.Copy(io.Discard, member)
			return err
		})

		return err
	})

	return err
}

func writeInstallationMember(
	archive *tar.Writer,
	name string,
	size int64,
	data io.Reader,
) (installationMember, error) {
	hash := sha256.New()
	err := writeRecoveryEntry(archive, name, size, io.TeeReader(data, hash))

	return installationMember{name, size, hex.EncodeToString(hash.Sum(nil))}, err
}

// Installation archives use fixed member names. Auth and configuration are
// checked as evidence and never supplied to the installation callback.
func consumeRestoreArchive(
	archive *tar.Reader,
	install func(int, io.Reader) error,
) (*installationManifest, error) {
	entry, err := archive.Next()
	if err != nil {
		return nil, ErrBackupScope
	}

	if entry.Name != installationEntries[0] {
		return nil, consumeBackupStores(archive, entry, install)
	}

	var members []installationMember
	for i, name := range installationEntries {
		if err != nil {
			return nil, ErrCorrupt
		}

		if i >= storeCount && entry.Name != name {
			continue
		}

		if !isRegularRecoveryEntry(entry, name) {
			return nil, ErrCorrupt
		}

		hash := sha256.New()
		reader := io.TeeReader(archive, hash)

		if i < storeCount {
			err = install(i, reader)
		} else {
			//nolint:gosec // Fixed-name non-sparse entries are read from the archive without extraction.
			_, err = io.Copy(io.Discard, reader)
		}

		if err != nil {
			return nil, err
		}

		members = append(members, installationMember{name, entry.Size, hex.EncodeToString(hash.Sum(nil))})
		entry, err = archive.Next()
	}

	if err != nil || !isRegularRecoveryEntry(entry, installationManifestEntry) || entry.Size > 16<<10 {
		return nil, ErrCorrupt
	}

	data, err := io.ReadAll(io.LimitReader(archive, entry.Size))
	if err != nil {
		return nil, err
	}

	var manifest installationManifest
	if json.Unmarshal(data, &manifest) != nil || manifest.BackupID == "" || manifest.CapturedAt.IsZero() ||
		manifest.Scope != "installation" || len(manifest.Members) != len(members) {
		return nil, ErrCorrupt
	}

	for i, member := range members {
		if manifest.Members[i] != member {
			return nil, fmt.Errorf("%w: archive checksum mismatch for %s", ErrCorrupt, member.Name)
		}
	}

	if _, err := archive.Next(); err != io.EOF {
		return nil, ErrCorrupt
	}

	return &manifest, nil
}
