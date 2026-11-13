// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
)

const (
	upgradeBackupSuffix       = ".upgrade-backup.tar"
	backupSuffix              = ".backup.tar"
	ErrUpgradeBackup    Error = "pre-upgrade backup unavailable; check the backup path and free space or restore a verified pair"
)

var backupEntries = [2]string{"wake-upgrade/warehouse.db", "wake-upgrade/access.db"}

type backupReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r backupReader) Read(data []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}

	return r.reader.Read(data)
}

func verifyBackupSource(ctx context.Context, db *sql.DB) error {
	var result string
	if err := db.QueryRowContext(ctx, profileStatement("Integrity", "main")).Scan(&result); err != nil {
		return err
	}

	if result != "ok" {
		return ErrCorrupt
	}

	return nil
}

// createUpgradeBackup is called with both originals locked until publication.
func createUpgradeBackup(ctx context.Context, plan UpgradePlan, owners [2]*os.File) (string, error) {
	if err := checkBackupDestination(plan.BackupPath); err != nil {
		return "", err
	}

	dir, err := os.MkdirTemp(filepath.Dir(plan.BackupPath), ".wake-snapshots-")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(dir) }()

	var snapshots [2]string
	for i, store := range plan.Stores {
		if err = checkUpgradeJournals(store.Path); err != nil {
			return "", err
		}

		snapshots[i] = filepath.Join(dir, filepath.Base(backupEntries[i]))
		if err = snapshotStore(ctx, store, owners[i], snapshots[i]); err != nil {
			return "", err
		}
	}

	warehouse := sql.OpenDB(&connector{paths: [2]string{snapshots[0]}, probe: true})
	defer func() { _ = warehouse.Close() }()

	warehouse.SetMaxOpenConns(1)

	var accessID string
	if err = warehouse.QueryRowContext(ctx, profileStatement("Profile", "")).Scan(&accessID); err != nil {
		return "", err
	}

	if accessID != plan.Stores[1].ID {
		return "", ErrAssociation
	}

	access := (&url.URL{Scheme: "file", Path: snapshots[1], RawQuery: "mode=ro&immutable=1"}).String()
	if _, err = warehouse.ExecContext(ctx, profileStatement("AttachAccess", ""), access); err != nil {
		return "", err
	}

	if err = checkProviderAssociations(ctx, warehouse); err != nil {
		return "", err
	}

	temp, err := os.CreateTemp(filepath.Dir(plan.BackupPath), ".wake-backup-")
	if err != nil {
		return "", err
	}

	defer func() { _ = os.Remove(temp.Name()) }()
	defer func() { _ = temp.Close() }()

	hash := sha256.New()

	archive := tar.NewWriter(io.MultiWriter(temp, hash))
	for i, path := range snapshots {
		if err = archiveSnapshot(ctx, archive, path, backupEntries[i]); err != nil {
			return "", err
		}
	}

	if err = archive.Close(); err != nil {
		return "", err
	}

	if err = temp.Sync(); err != nil {
		return "", err
	}

	if err = temp.Close(); err != nil {
		return "", err
	}

	expected := hex.EncodeToString(hash.Sum(nil))
	if err = verifyUpgradeBackup(ctx, temp.Name(), expected); err != nil {
		return "", err
	}

	if err = ctx.Err(); err != nil {
		return "", err
	}

	if err = os.Rename(temp.Name(), plan.BackupPath); err != nil {
		return "", err
	}

	return expected, syncUpgradeDir(plan.BackupPath)
}

func snapshotStore(ctx context.Context, store UpgradeStore, owner *os.File, path string) error {
	source := sql.OpenDB(&connector{paths: [2]string{store.Path}, owners: [2]*os.File{owner}, probe: true})
	defer func() { _ = source.Close() }()

	if err := verifyBackupSource(ctx, source); err != nil {
		return err
	}

	if _, err := source.ExecContext(ctx, profileStatement("BackupSnapshot", ""), path); err != nil {
		return err
	}

	snapshot := sql.OpenDB(&connector{paths: [2]string{path}, probe: true})
	defer func() { _ = snapshot.Close() }()

	var kind, version, id string
	if err := snapshot.QueryRowContext(ctx, profileStatement("Metadata", "main")).
		Scan(&kind, &version, &id); err != nil {
		return err
	}

	if kind != store.Kind || version != store.Current || id != store.ID {
		return ErrAssociation
	}

	return checkForeignKeys(ctx, snapshot, "main")
}

func archiveSnapshot(ctx context.Context, archive *tar.Writer, path, name string) error {
	file, err := os.Open(path) // #nosec G304 -- generated filename in Wake's private staging directory.
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()

	info, err := file.Stat()
	if err != nil {
		return err
	}

	if err = archive.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: info.Size()}); err != nil {
		return err
	}

	_, err = io.Copy(archive, backupReader{ctx, file})

	return err
}

// checkBackupDestination protects existing files outside Wake's archive format.
func checkBackupDestination(path string) error {
	file, err := openBackup(path)
	if os.IsNotExist(err) {
		return nil
	}

	if err != nil {
		return err
	}

	defer func() { _ = file.Close() }()

	return readBackupPair(tar.NewReader(file))
}

func openBackup(path string) (*os.File, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}

	if !info.Mode().IsRegular() || !privateOwned(info) || !singleLink(info) {
		return nil, ErrUnsafeStore
	}

	return openUpgradeIntent(path, info)
}

func readBackupPair(archive *tar.Reader) error {
	for _, name := range backupEntries {
		entry, err := archive.Next()
		if err != nil || entry.Name != name || entry.Typeflag != tar.TypeReg || entry.Size <= 0 {
			return ErrCorrupt
		}
	}

	if _, err := archive.Next(); err != io.EOF {
		return ErrCorrupt
	}

	return nil
}

func verifyUpgradeBackup(ctx context.Context, path, expected string) error {
	file, err := openBackup(path)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()

	hash := sha256.New()

	reader := io.TeeReader(backupReader{ctx, file}, hash)
	if err = readBackupPair(tar.NewReader(reader)); err != nil {
		return err
	}

	if _, err = io.Copy(io.Discard, reader); err != nil {
		return err
	}

	if hex.EncodeToString(hash.Sum(nil)) != expected {
		return fmt.Errorf("%w: archive checksum mismatch", ErrCorrupt)
	}

	return nil
}
