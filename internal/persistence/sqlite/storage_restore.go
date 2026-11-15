// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"archive/tar"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

const (
	RestoreIntentSuffix       = ".restore-intent"
	ErrRestorePending   Error = "a storage restore is pending; retry with the original archive and profile"
	ErrRestoreOccupied  Error = "restore destination is occupied"
)

// The intent is durable before either destination is installed. On retry only
// the specific staged files, identified by inode, may occupy the destinations.
type restoreIntent struct {
	ArchiveDigest string          `json:"archive_sha256"`
	Stores        [2]restoreStore `json:"stores"`
}

type restoreStore struct {
	Path  string      `json:"path"`
	Stage string      `json:"stage"`
	File  upgradeFile `json:"file"`
}

// Restore installs a warehouse snapshot or an explicitly selected paired backup.
func Restore(
	ctx context.Context,
	profile Profile,
	archivePath string,
	withCredentials bool,
) ([]string, error) {
	paths := profile.paths()
	intentPath := paths[0] + RestoreIntentSuffix
	if err := checkNoUpgrade(paths[0]); err != nil {
		return nil, err
	}

	if !withCredentials {
		if err := rejectPendingRestore(paths[0]); err != nil {
			return nil, err
		}

		return paths[:1], restoreWarehouseSnapshot(ctx, paths, archivePath)
	}

	intent, err := readRestoreIntent(intentPath)
	if err != nil {
		return nil, err
	}

	if intent == nil {
		if err := checkVacantStores(paths[:]); err != nil {
			return nil, err
		}
	}

	archive, err := openBackup(archivePath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = archive.Close() }()

	if intent != nil {
		if err := matchRestore(ctx, intent, archive, paths); err != nil {
			return nil, err
		}
	} else {
		prepared, err := prepareRestore(ctx, archive, paths, intentPath)
		if err != nil {
			return nil, err
		}

		intent = prepared
	}

	if err := installRestore(ctx, intentPath, intent); err != nil {
		return nil, err
	}

	return paths[:], nil
}

func checkVacantStores(paths []string) error {
	var occupied error
	for _, path := range paths {
		err := checkVacantStore(path, false)
		if err != nil && !errors.Is(err, ErrRestoreOccupied) {
			return err
		}

		occupied = errors.Join(occupied, err)
	}

	return occupied
}

func checkVacantStore(path string, installed bool) error {
	paths, err := storeSidecars(path)
	if err != nil {
		return err
	}

	if !installed {
		paths = append([]string{path}, paths...)
	}

	var occupied error
	for _, candidate := range paths {
		if _, err := os.Lstat(candidate); err == nil {
			occupied = errors.Join(occupied, fmt.Errorf("%w: %s", ErrRestoreOccupied, candidate))
		} else if !os.IsNotExist(err) {
			return err
		}
	}

	return occupied
}

func prepareRestore(
	ctx context.Context,
	archive *os.File,
	paths [2]string,
	intentPath string,
) (*restoreIntent, error) {
	var (
		intent restoreIntent
		staged [2]string
		owners [2]*os.File
	)

	published := false
	defer func() {
		for _, path := range staged {
			if !published && path != "" {
				_ = os.Remove(path)
			}
		}
	}()

	hash := sha256.New()
	reader := io.TeeReader(backupReader{ctx, archive}, hash)

	if err := consumeBackupPair(tar.NewReader(reader), func(i int, member io.Reader) error {
		file, err := os.CreateTemp(filepath.Dir(paths[i]), ".wake-restore-")
		if err != nil {
			return err
		}

		staged[i] = file.Name()
		_, err = io.Copy(file, member)

		return errors.Join(err, file.Sync(), file.Close())
	}); err != nil {
		return nil, err
	}

	for i, path := range staged {
		owner, err := lockStoreFile(path, false)
		if err != nil {
			return nil, err
		}
		defer func() { _ = owner.Close() }()

		owners[i] = owner
	}

	if _, err := io.Copy(io.Discard, reader); err != nil {
		return nil, err
	}

	intent.ArchiveDigest = hex.EncodeToString(hash.Sum(nil))

	if err := validateStorageCopy(ctx, staged[:], owners[:]); err != nil {
		return nil, err
	}

	if err := pauseRestoredWarehouse(ctx, staged[0]); err != nil {
		return nil, err
	}

	for i, path := range staged {
		info, err := owners[i].Stat()
		if err != nil {
			return nil, err
		}

		intent.Stores[i] = restoreStore{Path: paths[i], Stage: path, File: fileIdentity(info)}
		if err := syncUpgradeDir(path); err != nil {
			return nil, err
		}
	}

	guard, err := lockStoreDirectory(paths[0])
	if err != nil {
		return nil, err
	}
	defer func() { _ = guard.Close() }()

	if err := rejectPendingRestore(paths[0]); err != nil {
		return nil, err
	}

	if err := checkVacantStores(paths[:]); err != nil {
		return nil, err
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	published, err = publishRestoreIntent(intentPath, intent)
	if err != nil {
		return nil, err
	}

	return &intent, nil
}

func pauseRestoredWarehouse(ctx context.Context, path string) (result error) {
	uri := (&url.URL{Scheme: "file", Path: path, RawQuery: "mode=rw&_pragma=synchronous(EXTRA)"}).String()

	db, err := sql.Open("sqlite", uri)
	if err != nil {
		return err
	}

	defer func() { result = errors.Join(result, db.Close()) }()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var nonce [8]byte
	rand.Read(nonce[:])
	revision := int64(binary.LittleEndian.Uint64(nonce[:])&(1<<61-1)) + 1<<61

	if _, err := tx.ExecContext(ctx, profileStatement("RestoreWarehouse", ""), rand.Text()); err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx, profileStatement("RestoreReview", ""), revision); err != nil {
		return err
	}

	return tx.Commit()
}

func rejectPendingRestore(warehouse string) error {
	if _, err := os.Lstat(warehouse + RestoreIntentSuffix); !os.IsNotExist(err) {
		return ErrRestorePending
	}

	return nil
}

func readRestoreIntent(path string) (*restoreIntent, error) {
	file, err := openBackup(path)
	if os.IsNotExist(err) {
		return nil, nil
	}

	if err != nil {
		return nil, ErrRestorePending
	}

	defer func() { _ = file.Close() }()

	var intent restoreIntent

	data, err := io.ReadAll(io.LimitReader(file, 1<<20))
	if err != nil || json.Unmarshal(data, &intent) != nil || len(intent.ArchiveDigest) != sha256.Size*2 {
		return nil, ErrRestorePending
	}

	return &intent, nil
}

func publishRestoreIntent(path string, intent restoreIntent) (bool, error) {
	file, err := os.CreateTemp(filepath.Dir(path), ".wake-restore-intent-")
	if err != nil {
		return false, err
	}
	defer func() { _ = os.Remove(file.Name()) }()

	err = json.NewEncoder(file).Encode(intent)
	if err = errors.Join(err, file.Sync(), file.Close()); err != nil {
		return false, err
	}

	if err := renameNewFile(file.Name(), path); err != nil {
		return false, err
	}

	return true, syncUpgradeDir(path)
}

func matchRestore(ctx context.Context, intent *restoreIntent, archive *os.File, paths [2]string) error {
	for i, store := range intent.Stores {
		if store.Path != paths[i] || filepath.Clean(store.Stage) != store.Stage ||
			filepath.Dir(store.Stage) != filepath.Dir(store.Path) ||
			!strings.HasPrefix(filepath.Base(store.Stage), ".wake-restore-") {
			return ErrRestorePending
		}
	}

	hash := sha256.New()
	if _, err := io.Copy(hash, backupReader{ctx, archive}); err != nil {
		return err
	}

	if hex.EncodeToString(hash.Sum(nil)) != intent.ArchiveDigest {
		return ErrRestorePending
	}

	return nil
}

func installRestore(ctx context.Context, intentPath string, intent *restoreIntent) error {
	var installed [2]bool
	for i, store := range intent.Stores {
		_, err := os.Lstat(store.Stage)
		if err != nil && !os.IsNotExist(err) {
			return err
		}

		installed[i] = os.IsNotExist(err)

		path := store.Stage
		if installed[i] {
			path = store.Path
		}

		owner, err := lockStoreFile(path, false)
		if err != nil {
			return fmt.Errorf("%w: %s: %w", ErrRestorePending, path, err)
		}

		defer func() { _ = owner.Close() }()

		info, err := owner.Stat()
		if err != nil || fileIdentity(info) != store.File {
			return ErrRestorePending
		}

		if err := checkVacantStore(store.Path, installed[i]); err != nil {
			return err
		}

		if !installed[i] {
			if err := checkUpgradeJournals(path); err != nil {
				return err
			}
		}
	}

	for i, store := range intent.Stores {
		if err := ctx.Err(); err != nil {
			return err
		}

		if !installed[i] {
			if err := renameNewFile(store.Stage, store.Path); err != nil {
				return err
			}
		}

		if err := syncUpgradeDir(store.Path); err != nil {
			return err
		}
	}

	if err := os.Remove(intentPath); err != nil {
		return err
	}

	return syncUpgradeDir(intentPath)
}
