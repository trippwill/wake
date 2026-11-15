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

	"go.trippwill.dev/wake/internal/credential"
)

const (
	RestoreIntentSuffix       = ".restore-intent"
	ErrRestorePending   Error = "a storage restore is pending; retry with the original archive and profile"
	ErrRestoreOccupied  Error = "restore destination is occupied"
)

// The intent is durable before any destination is replaced. The original
// identities and prepared bytes determine where a retry can continue.
type restoreIntent struct {
	ArchiveDigest string                   `json:"archive_sha256"`
	Replace       bool                     `json:"replace"`
	Stores        [storeCount]restoreStore `json:"stores"`
}

type restoreStore struct {
	Path     string         `json:"path"`
	Stage    string         `json:"stage"`
	File     upgradeFile    `json:"file"`
	Digest   string         `json:"sha256"`
	Original *originalStore `json:"original,omitempty"`
}

type originalStore struct {
	Path string      `json:"path"`
	File upgradeFile `json:"file"`
}

// Restore installs a warehouse snapshot or an explicitly selected paired backup.
func Restore(
	ctx context.Context,
	profile Profile,
	archivePath string,
	withCredentials bool,
) ([]string, error) {
	return restore(ctx, profile, archivePath, withCredentials, false, nil)
}

// RestoreInstallation verifies encrypted access against the selected key before
// activating the paused association. An explicit replacement keeps originals.
func RestoreInstallation(
	ctx context.Context, profile Profile, archivePath string, replace bool, envelope *credential.Envelope,
) ([]string, error) {
	return restore(ctx, profile, archivePath, true, replace, func(path string) error {
		return CheckStoredCredentialKey(ctx, path, envelope)
	})
}

func restore(
	ctx context.Context,
	profile Profile,
	archivePath string,
	withCredentials, replace bool,
	checkAccess func(string) error,
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

		return paths[:1], restoreWarehouseSnapshot(ctx, paths[0], archivePath)
	}

	guard, err := lockStoreDirectory(paths[0])
	if err != nil {
		return nil, err
	}
	defer func() { _ = guard.Close() }()

	if err := checkNoUpgrade(paths[0]); err != nil {
		return nil, err
	}

	intent, err := readRestoreIntent(intentPath)
	if err != nil {
		return nil, err
	}

	if intent == nil && !replace {
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
		if err := matchRestore(ctx, intent, archive, paths, replace); err != nil {
			return nil, err
		}
	} else {
		prepared, err := prepareRestore(ctx, archive, paths, intentPath, replace, checkAccess)
		if err != nil {
			return nil, err
		}

		intent = prepared
	}

	if err := installRestore(ctx, intentPath, intent, checkAccess); err != nil {
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
	paths [storeCount]string,
	intentPath string,
	replace bool,
	checkAccess func(string) error,
) (*restoreIntent, error) {
	var (
		intent restoreIntent
		staged [storeCount]string
		owners [storeCount]*os.File
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

	manifest, err := consumeRestoreArchive(tar.NewReader(reader), func(i int, member io.Reader) error {
		file, err := os.CreateTemp(filepath.Dir(paths[i]), ".wake-restore-")
		if err != nil {
			return err
		}

		staged[i] = file.Name()
		_, err = io.Copy(file, member)

		return errors.Join(err, file.Sync(), file.Close())
	})
	if err != nil {
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
	intent.Replace = replace

	plan, err := validateStorageCopy(ctx, staged[:], owners[:])
	if err != nil {
		return nil, err
	}

	if manifest != nil {
		metadata, err := readInstallationMetadata(ctx, staged, owners[0], plan)
		if err != nil {
			return nil, err
		}

		if metadata != manifest.Storage {
			return nil, ErrCorrupt
		}
	}

	if checkAccess != nil {
		if err := checkAccess(staged[1]); err != nil {
			return nil, err
		}
	}

	if err := renewRestoredStores(ctx, staged); err != nil {
		return nil, err
	}

	if _, err := validateStorageCopy(ctx, staged[:], owners[:]); err != nil {
		return nil, err
	}

	for i, path := range staged {
		info, err := owners[i].Stat()
		if err != nil {
			return nil, err
		}

		digest, err := restoreDigest(ctx, owners[i])
		if err != nil {
			return nil, err
		}

		store := restoreStore{Path: paths[i], Stage: path, File: fileIdentity(info), Digest: digest}
		if err := checkVacantStore(store.Path, true); err != nil {
			return nil, err
		}

		if replace {
			if _, err := os.Lstat(store.Path); !os.IsNotExist(err) {
				owner, err := lockStoreFile(store.Path, false)
				if err != nil {
					return nil, err
				}
				defer func() { _ = owner.Close() }()

				info, err := owner.Stat()
				if err != nil {
					return nil, err
				}

				store.Original = &originalStore{
					Path: filepath.Join(
						filepath.Dir(store.Path),
						".wake-original-"+rand.Text()+"-"+filepath.Base(store.Path),
					),
					File: fileIdentity(info),
				}
			}
		} else if err := checkVacantStore(store.Path, false); err != nil {
			return nil, err
		}

		intent.Stores[i] = store

		if err := syncUpgradeDir(path); err != nil {
			return nil, err
		}
	}

	if err := rejectPendingRestore(paths[0]); err != nil {
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

func renewRestoredStores(ctx context.Context, paths [storeCount]string) (result error) {
	uri := (&url.URL{Scheme: "file", Path: paths[0], RawQuery: "mode=rw&_pragma=synchronous(EXTRA)"}).String()

	db, err := sql.Open("sqlite", uri)
	if err != nil {
		return err
	}

	defer func() { result = errors.Join(result, db.Close()) }()

	db.SetMaxOpenConns(1)

	for i, path := range paths[1:] {
		if path == "" {
			continue
		}

		schema := storeSchemas[i+1]
		if _, err := db.ExecContext(ctx, profileStatement("Attach", schema), path); err != nil {
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

	if _, err := tx.ExecContext(ctx, profileStatement("RenewStoreID", "main"), rand.Text()); err != nil {
		return err
	}

	if paths[2] != "" {
		var nonce [8]byte
		rand.Read(nonce[:])
		revision := int64(binary.LittleEndian.Uint64(nonce[:])&(1<<61-1)) + 1<<61

		if _, err := tx.ExecContext(ctx, profileStatement("RestoreReview", "sync"), revision); err != nil {
			return err
		}

		if _, err := tx.ExecContext(ctx, profileStatement("RenewStoreID", "sync"), rand.Text()); err != nil {
			return err
		}

		if _, err := tx.ExecContext(ctx, profileStatement("DiscardRestoredWork", "")); err != nil {
			return err
		}

		var metadata [storeCount]storeMetadata
		for i, schema := range storeSchemas {
			m := &metadata[i]
			if err := tx.QueryRowContext(ctx, profileStatement("Metadata", schema)).
				Scan(&m.kind, &m.version, &m.id); err != nil {
				return err
			}
		}

		if err := bindUpstream(ctx, tx, metadata, false, true); err != nil {
			return err
		}
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

func matchRestore(
	ctx context.Context,
	intent *restoreIntent,
	archive *os.File,
	paths [storeCount]string,
	replace bool,
) error {
	if intent.Replace != replace {
		return ErrRestorePending
	}

	for i, store := range intent.Stores {
		if store.Path != paths[i] || !restorePathMatches(store.Path, store.Stage, ".wake-restore-") ||
			len(store.Digest) != sha256.Size*2 {
			return ErrRestorePending
		}

		if store.Original != nil && (!replace ||
			!restorePathMatches(store.Path, store.Original.Path, ".wake-original-")) {
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

func restorePathMatches(destination, path, prefix string) bool {
	return filepath.Clean(path) == path && filepath.Dir(path) == filepath.Dir(destination) &&
		strings.HasPrefix(filepath.Base(path), prefix) && path != destination
}

func restoreDigest(ctx context.Context, file *os.File) (string, error) {
	info, err := file.Stat()
	if err != nil {
		return "", err
	}

	hash := sha256.New()
	if _, err = io.CopyN(
		hash,
		backupReader{ctx, io.NewSectionReader(file, 0, info.Size())},
		info.Size(),
	); err != nil {
		return "", err
	}

	return hex.EncodeToString(hash.Sum(nil)), nil
}

func installRestore(
	ctx context.Context,
	intentPath string,
	intent *restoreIntent,
	checkAccess func(string) error,
) error {
	var installed, preserve [storeCount]bool
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

		digest, err := restoreDigest(ctx, owner)
		if err != nil {
			return err
		}

		if digest != store.Digest {
			return ErrRestorePending
		}

		if i == 1 && checkAccess != nil {
			if err := checkAccess(path); err != nil {
				return err
			}
		}

		if !installed[i] {
			if err := checkUpgradeJournals(path); err != nil {
				return err
			}
		}

		if store.Original != nil {
			originalPath := store.Original.Path
			if _, err := os.Lstat(originalPath); os.IsNotExist(err) && !installed[i] {
				originalPath = store.Path
				preserve[i] = true
			}

			original, err := lockStoreFile(originalPath, false)
			if err != nil {
				return fmt.Errorf("%w: %s: %w", ErrRestorePending, originalPath, err)
			}
			defer func() { _ = original.Close() }()

			info, err := original.Stat()
			if err != nil || fileIdentity(info) != store.Original.File {
				return ErrRestorePending
			}
		}

		if err := checkVacantStore(store.Path, installed[i] || preserve[i]); err != nil {
			return err
		}
	}

	if err := syncUpgradeDir(intentPath); err != nil {
		return err
	}

	for i, store := range intent.Stores {
		if err := ctx.Err(); err != nil {
			return err
		}

		if !installed[i] {
			if preserve[i] {
				if err := renameNewFile(store.Path, store.Original.Path); err != nil {
					return err
				}

				if err := syncUpgradeDir(store.Path); err != nil {
					return err
				}
			}

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
