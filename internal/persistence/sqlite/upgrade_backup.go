// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"archive/tar"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const (
	upgradeBackupSuffix       = ".upgrade-backup.tar"
	backupSuffix              = ".backup.tar"
	latestUpgradeSuffix       = ".latest-upgrade"
	ErrUpgradeBackup    Error = "pre-upgrade backup unavailable; check the backup path and free space or restore a verified pair"
)

var backupEntries = [2]string{"wake-upgrade/warehouse.db", "wake-upgrade/access.db"}

const manifestEntry = "wake-upgrade/manifest.json"

type backupManifest struct {
	Version int       `json:"version"`
	Formats [2]string `json:"formats"`
	IDs     [2]string `json:"store_ids"`
	Rules   [2]int64  `json:"applied_and_pending_rules"`
	Digests [2]string `json:"sha256"`
}

type upgradeArchive struct {
	Path      string `json:"path"`
	Digest    string `json:"sha256"`
	Warehouse string `json:"warehouse_id"`
}

func validUpgradeArchive(warehouse, path string) bool {
	return filepath.Dir(path) == filepath.Dir(warehouse) && filepath.Clean(path) == path &&
		strings.HasPrefix(path, warehouse+".upgrade-backup-") && strings.HasSuffix(path, ".tar")
}

func readLatestUpgrade(warehouse string) (upgradeArchive, error) {
	var record upgradeArchive

	err := readPrivateRecord(warehouse+latestUpgradeSuffix, &record)
	if os.IsNotExist(err) {
		return record, nil
	}

	if err != nil || !validUpgradeArchive(warehouse, record.Path) || !validBackupDigest(record.Digest) ||
		record.Warehouse == "" {
		return upgradeArchive{}, ErrUpgradeRecovery
	}

	return record, nil
}

func validBackupDigest(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

func (s *Store) retainUpgradeArchive(ctx context.Context, warehouse string, intent upgradeIntent) error {
	current := upgradeArchive{
		Path:      intent.ArchivePath,
		Digest:    intent.BackupDigest,
		Warehouse: intent.Plan.Stores[0].ID,
	}
	if err := writePrivateRecord(warehouse+latestUpgradeSuffix, current); err != nil {
		return err
	}

	old := intent.Predecessor
	if old.Path == "" || old.Path == current.Path {
		return nil
	}

	if !validUpgradeArchive(warehouse, old.Path) {
		return ErrUpgradeRecovery
	}

	err := verifyUpgradeBackup(ctx, old.Path, old.Digest)
	if err == nil {
		err = os.Remove(old.Path)
		if err == nil {
			err = syncUpgradeDir(old.Path)
		}
	}

	if err != nil && !os.IsNotExist(err) {
		s.logger.WarnContext(
			ctx,
			"could not remove preceding upgrade backup",
			"event",
			"daemon_upgrade_backup_cleanup",
			"path",
			old.Path,
		)
	}

	return nil
}

func uniqueBackupPath(warehouse, suffix string) string {
	return warehouse + suffix[:len(suffix)-4] + "-" + time.Now().
		UTC().
		Format("20060102T150405Z") +
		"-" + rand.Text() + ".tar"
}

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

	if err = attachBackupAccess(ctx, warehouse, snapshots[1]); err != nil {
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

	manifest := backupManifest{Version: 1}
	if err = warehouse.QueryRowContext(ctx, profileStatement("HistoryVersion", "main")).
		Scan(&manifest.Rules[0], &manifest.Rules[1]); err != nil {
		return "", err
	}

	for i, path := range snapshots {
		manifest.Formats[i], manifest.IDs[i] = plan.Stores[i].Current, plan.Stores[i].ID

		manifest.Digests[i], err = archiveSnapshot(ctx, archive, path, backupEntries[i])
		if err != nil {
			return "", err
		}
	}

	data, err := json.Marshal(manifest)
	if err != nil {
		return "", err
	}

	if err = archive.WriteHeader(
		&tar.Header{Name: manifestEntry, Mode: 0o600, Size: int64(len(data))},
	); err != nil {
		return "", err
	}

	if _, err = archive.Write(data); err != nil {
		return "", err
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

	if err = unix.Renameat2(
		unix.AT_FDCWD,
		temp.Name(),
		unix.AT_FDCWD,
		plan.BackupPath,
		unix.RENAME_NOREPLACE,
	); err != nil {
		return "", err
	}

	return expected, syncUpgradeDir(plan.BackupPath)
}

func backupURI(path, query string) string {
	return (&url.URL{Scheme: "file", Path: path, RawQuery: query}).String()
}

func attachBackupAccess(ctx context.Context, db *sql.DB, path string) error {
	_, err := db.ExecContext(ctx, profileStatement("AttachAccess", ""), backupURI(path, "mode=ro&immutable=1"))
	return err
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

func archiveSnapshot(ctx context.Context, archive *tar.Writer, path, name string) (string, error) {
	file, err := os.Open(path) // #nosec G304 -- generated filename in Wake's private staging directory.
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()

	info, err := file.Stat()
	if err != nil {
		return "", err
	}

	if err = archive.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: info.Size()}); err != nil {
		return "", err
	}

	hash := sha256.New()
	_, err = io.Copy(io.MultiWriter(archive, hash), backupReader{ctx, file})

	return hex.EncodeToString(hash.Sum(nil)), err
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

// readBackupPair accepts the original pair and the manifest-bearing archive.
func readBackupPair(reader io.Reader, outputs [2]io.Writer) (backupManifest, error) {
	counter := &archiveReader{Reader: reader}

	archive := tar.NewReader(counter)

	var (
		manifest backupManifest
		digests  [2]string
	)
	for i, name := range backupEntries {
		entry, err := archive.Next()
		if err != nil || entry.Name != name || entry.Typeflag != tar.TypeReg || entry.Size <= 0 {
			return manifest, ErrCorrupt
		}

		hash := sha256.New()

		var dest io.Writer = hash
		if outputs[i] != nil {
			dest = io.MultiWriter(outputs[i], hash)
		}

		if _, err = io.CopyN(dest, archive, entry.Size); err != nil {
			return manifest, err
		}

		digests[i] = hex.EncodeToString(hash.Sum(nil))
	}

	end := archiveEnd(counter.read)
	entry, err := archive.Next()
	if err == io.EOF {
		if counter.read != end {
			return manifest, ErrCorrupt
		}

		return manifest, nil
	}

	if err != nil || entry.Name != manifestEntry || entry.Typeflag != tar.TypeReg || entry.Size <= 0 ||
		entry.Size > 8192 {
		return manifest, ErrCorrupt
	}

	data, err := io.ReadAll(archive)
	if err != nil || json.Unmarshal(data, &manifest) != nil || manifest.Version != 1 ||
		manifest.Digests != digests {
		return manifest, ErrCorrupt
	}

	end = archiveEnd(counter.read)
	if _, err = archive.Next(); err != io.EOF || counter.read != end {
		return manifest, ErrCorrupt
	}

	return manifest, nil
}

type archiveReader struct {
	io.Reader
	read int64
}

func (r *archiveReader) Read(data []byte) (int, error) {
	n, err := r.Reader.Read(data)
	r.read += int64(n)

	return n, err
}

func archiveEnd(offset int64) int64 {
	const block = 512
	return (offset+block-1)/block*block + 2*block
}

func verifyUpgradeBackup(ctx context.Context, path, expected string) error {
	file, err := openBackup(path)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()

	hash := sha256.New()

	reader := io.TeeReader(backupReader{ctx, file}, hash)
	if _, err = readBackupPair(reader, [2]io.Writer{}); err != nil {
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
