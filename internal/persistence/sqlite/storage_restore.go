// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
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
	"time"

	"go.trippwill.dev/wake/internal/credential"
	"go.trippwill.dev/wake/internal/persistence/sqlite/recoveryarchive"
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
	SyncOnly      bool                     `json:"sync_only,omitempty"`
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

	_, archiveDigest, err := copyRestoreArchive(ctx, archive, paths, &staged, &owners)
	defer closeRestoreCopies(owners)

	if err != nil {
		return nil, err
	}

	intent.ArchiveDigest, intent.Replace = archiveDigest, replace

	if checkAccess != nil {
		if err := checkAccess(staged[1]); err != nil {
			return nil, err
		}
	}

	var originals [storeCount]*os.File
	defer func() { closeRestoreCopies(originals) }()

	for i, path := range paths {
		original, err := lockRestoreOriginal(path, replace)
		if err != nil {
			return nil, err
		}

		originals[i] = original
	}

	var pacing recoveryPacing
	if originals[2] != nil {
		prior, err := inspectRecoverableSync(ctx, paths[2], originals[2])
		if err == nil {
			pacing, err = readRecoveryPacing(ctx, prior)
			if err := errors.Join(err, prior.Close()); err != nil {
				return nil, err
			}
		}
	}

	if err := renewRestoredStores(ctx, staged, pacing); err != nil {
		return nil, err
	}

	if _, err := validateStorageCopy(ctx, staged[:], owners[:]); err != nil {
		return nil, err
	}

	for i, owner := range owners {
		store, err := describeRestoreStore(ctx, paths[i], staged[i], owner, originals[i], i)
		if err != nil {
			return nil, err
		}

		intent.Stores[i] = store
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

// copyRestoreArchive verifies complete archive members before callers use
// either their data or metadata. The caller owns the staged files and locks.
func copyRestoreArchive(
	ctx context.Context, archive *os.File, destinations [storeCount]string,
	staged *[storeCount]string, owners *[storeCount]*os.File,
) (*recoveryarchive.Manifest, string, error) {
	hash := sha256.New()
	reader := io.TeeReader(backupReader{ctx, archive}, hash)

	manifest, err := consumeRestoreArchive(reader, func(i int, member io.Reader) error {
		file, err := os.CreateTemp(filepath.Dir(destinations[i]), ".wake-restore-")
		if err != nil {
			return err
		}

		staged[i] = file.Name()
		_, err = io.Copy(file, member)

		return errors.Join(err, file.Sync(), file.Close())
	})
	if err != nil {
		return nil, "", err
	}

	for i, path := range staged {
		owner, err := lockStoreFile(path, false)
		if err != nil {
			return nil, "", err
		}

		owners[i] = owner
	}

	if _, err := io.Copy(io.Discard, reader); err != nil {
		return nil, "", err
	}

	baseline := releaseBaseline()
	if manifest != nil && manifest.Storage.Stores[2].Format == syncArchiveVersion {
		baseline = installationArchiveBaseline()
	}

	plan, err := validateStorageSet(ctx, staged[:], owners[:], baseline)
	if err != nil {
		return nil, "", err
	}

	if manifest != nil {
		metadata, err := readInstallationMetadata(ctx, *staged, owners[0], plan)
		if err != nil {
			return nil, "", err
		}

		if metadata != manifest.Storage {
			return nil, "", ErrCorrupt
		}
	}

	return manifest, hex.EncodeToString(hash.Sum(nil)), nil
}

func closeRestoreCopies(owners [storeCount]*os.File) {
	for _, owner := range owners {
		if owner != nil {
			_ = owner.Close()
		}
	}
}

func lockRestoreOriginal(path string, replace bool) (*os.File, error) {
	if err := checkVacantStore(path, replace); err != nil {
		return nil, err
	}

	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return nil, nil
	}

	return lockStoreFile(path, false)
}

func describeRestoreStore(
	ctx context.Context, path, stage string, owner, original *os.File, index int,
) (restoreStore, error) {
	store := restoreStore{Path: path, Stage: stage}

	info, err := owner.Stat()
	if err != nil {
		return store, err
	}

	store.File = fileIdentity(info)

	store.Digest, err = restoreDigest(ctx, owner)
	if err != nil {
		return store, err
	}

	if original != nil {
		info, err := original.Stat()
		if err != nil {
			return store, err
		}

		store.Original = &originalStore{
			Path: filepath.Join(filepath.Dir(path), ".wake-original-"+rand.Text()+"-"+storeKinds[index]),
			File: fileIdentity(info),
		}
	}

	return store, syncUpgradeDir(path)
}

func renewRestoredStores(
	ctx context.Context,
	paths [storeCount]string,
	pacing recoveryPacing,
) (result error) {
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
		if err := prepareRecoveredProfile(ctx, tx, time.Now()); err != nil {
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

		if err := preserveRecoveryPacing(ctx, tx, pacing); err != nil {
			return err
		}

		if err := bindUpstream(ctx, tx, metadata, false, true); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func prepareRecoveredProfile(ctx context.Context, tx *sql.Tx, at time.Time) error {
	var kind, format, id string
	if err := tx.QueryRowContext(ctx, profileStatement("Metadata", "sync")).
		Scan(&kind, &format, &id); err != nil {
		return err
	}

	if format == syncArchiveVersion {
		if _, err := tx.ExecContext(ctx, recoverySchemaSQL); err != nil {
			return err
		}

		if _, err := tx.ExecContext(
			ctx,
			profileStatement("AdvanceFormat", "sync"),
			syncSchemaVersion,
			format,
		); err != nil {
			return err
		}
	}

	if _, err := tx.ExecContext(ctx, profileStatement("ClearRecoveryScopes", "")); err != nil {
		return err
	}

	if _, err := tx.ExecContext(
		ctx,
		profileStatement("SetRecoveryStart", ""),
		at.UTC().Format(time.RFC3339Nano),
	); err != nil {
		return err
	}

	_, err := tx.ExecContext(ctx, profileStatement("RestoreReview", "sync"), newRecoveryRevision())

	return err
}

func newRecoveryRevision() int64 {
	var nonce [8]byte
	rand.Read(nonce[:])

	return int64(binary.LittleEndian.Uint64(nonce[:])&(1<<61-1)) + 1<<61
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
	if intent.SyncOnly || intent.Replace != replace {
		return ErrRestorePending
	}

	if err := matchRestorePaths(intent, paths); err != nil {
		return err
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

func matchRestorePaths(intent *restoreIntent, paths [storeCount]string) error {
	for i, store := range intent.Stores {
		if store.Path != paths[i] || len(store.Digest) != sha256.Size*2 {
			return ErrRestorePending
		}

		if i == 0 && intent.SyncOnly {
			if store.Stage != "" || store.Original != nil {
				return ErrRestorePending
			}

			continue
		}

		if !restorePathMatches(store.Path, store.Stage, ".wake-restore-") ||
			store.Original != nil && (!intent.Replace ||
				!restorePathMatches(store.Path, store.Original.Path, ".wake-original-")) {
			return ErrRestorePending
		}
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

		installed[i] = i == 0 && intent.SyncOnly || os.IsNotExist(err)

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
