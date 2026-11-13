// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

const ErrRestoreChanged Error = "restore plan changed; run wake restore again"

type RestorePlan struct {
	ID            string    `json:"plan_id"`
	Archive       string    `json:"archive"`
	ArchiveDigest string    `json:"archive_sha256"`
	Stores        [2]string `json:"destinations"`
	Formats       [2]string `json:"formats"`
	Rules         [2]int64  `json:"applied_and_pending_rules"`
	Displaced     []string  `json:"displaced_files"`
	RecoveryDirs  [2]string `json:"recovery_directories"`
	Pending       bool      `json:"recovery_pending"`
	Installed     bool      `json:"installed"`
}

type restoreFile struct {
	Path     string
	Store    int
	Digest   string
	Identity upgradeFile
}

type restoreIntent struct {
	Plan      RestorePlan
	Parents   [2]upgradeFile
	Originals []restoreFile
	Staged    [2]restoreFile
	Committed bool
}

// Restore validates an archive or installs an explicitly approved pair.
func Restore(
	ctx context.Context,
	profile Profile,
	archive, confirm string,
	protected []string,
) (RestorePlan, error) {
	paths := profile.paths()
	if err := checkStorePaths(paths); err != nil {
		return RestorePlan{}, err
	}

	if !filepath.IsAbs(archive) || filepath.Clean(archive) != archive {
		return RestorePlan{}, ErrUnsafeStore
	}

	if err := registerCollations(); err != nil {
		return RestorePlan{}, err
	}

	protected = append(slices.Clone(protected), archive)

	parents, err := restoreParents(paths, protected)
	if err != nil {
		return RestorePlan{}, err
	}

	release, err := LockPaths(paths, confirm != "")
	if err != nil {
		return RestorePlan{}, err
	}
	defer release()

	var pending restoreIntent

	err = readPrivateRecord(paths[0]+restoreSuffix, &pending)
	if err == nil {
		if pending.Plan.Stores != paths || pending.Plan.Archive != archive || pending.Parents != parents ||
			!pending.valid() {
			return RestorePlan{}, ErrRestorePending
		}

		if confirm != "" && confirm != pending.Plan.ID {
			return RestorePlan{}, ErrRestoreChanged
		}

		pending.Plan.Pending = true
		if confirm == "" {
			return pending.Plan, nil
		}

		if !pending.Committed {
			if err = verifyUpgradeBackup(ctx, archive, pending.Plan.ArchiveDigest); err != nil {
				return pending.Plan, err
			}
		}

		err = installRestore(ctx, pending, nil)
		pending.Plan.Installed = err == nil

		return pending.Plan, err
	}

	if !os.IsNotExist(err) {
		return RestorePlan{}, ErrRestorePending
	}

	if err = CheckRestorePending(paths); err != nil {
		return RestorePlan{}, err
	}

	locked := map[string]bool{}
	for _, path := range paths {
		if _, e := os.Lstat(path); os.IsNotExist(e) {
			continue
		}

		owner, err := lockStoreFile(path, false)
		if err != nil {
			return RestorePlan{}, err
		}

		defer func() { _ = owner.Close() }()

		locked[path] = true
	}

	originals, err := restoreInventory(ctx, paths, protected)
	if err != nil {
		return RestorePlan{}, err
	}

	var dirs [2]string

	retain := false
	defer func() {
		if !retain {
			for _, dir := range dirs {
				_ = os.RemoveAll(dir)
			}
		}
	}()

	var snapshots [2]string
	for i, path := range paths {
		parent := filepath.Dir(path)
		if confirm == "" {
			parent = ""
		}

		dirs[i], err = os.MkdirTemp(parent, ".wake-restore-")
		if err != nil {
			return RestorePlan{}, err
		}

		snapshots[i] = filepath.Join(dirs[i], "snapshot.db")
	}

	manifest, hash, err := unpackBackup(ctx, archive, snapshots)
	if err != nil {
		return RestorePlan{}, err
	}

	actual, err := validateRestorePair(ctx, snapshots, manifest)
	if err != nil {
		return RestorePlan{}, err
	}

	plan := RestorePlan{
		Archive:       archive,
		ArchiveDigest: hash,
		Stores:        paths,
		Formats:       actual.Formats,
		Rules:         actual.Rules,
		Displaced:     []string{},
	}
	for _, old := range originals {
		plan.Displaced = append(plan.Displaced, old.Path)
	}

	intent := restoreIntent{Plan: plan, Parents: parents, Originals: originals}

	id, err := intent.planID()
	if err != nil {
		return RestorePlan{}, err
	}

	plan.ID = id
	for i, path := range paths {
		plan.RecoveryDirs[i] = filepath.Join(filepath.Dir(path), ".wake-restore-<random>")
	}

	if confirm == "" {
		return plan, nil
	}

	if confirm != plan.ID {
		return plan, ErrRestoreChanged
	}

	if err = pauseRestoredWarehouse(ctx, snapshots[0]); err != nil {
		return plan, err
	}

	plan.RecoveryDirs = dirs
	intent.Plan = plan
	for i, path := range snapshots {
		if err = os.Mkdir(filepath.Join(dirs[i], "previous"), 0o700); err != nil {
			return plan, err
		}

		intent.Staged[i], err = inspectRestoreFile(ctx, path, i)
		if err != nil {
			return plan, err
		}

		if err = errors.Join(syncUpgradeDir(path), syncUpgradeDir(dirs[i])); err != nil {
			return plan, err
		}
	}
	// A record may have reached disk even if directory sync reports an error.
	retain = true

	if err = writePrivateRecord(paths[0]+restoreSuffix, intent); err != nil {
		return plan, err
	}

	err = installRestore(ctx, intent, locked)
	plan.Installed = err == nil

	return plan, err
}

func restoreParents(paths [2]string, protected []string) ([2]upgradeFile, error) {
	var parents [2]upgradeFile
	for i, path := range paths {
		for _, suffix := range []string{"", storeLockSuffix, restoreSuffix, UpgradeIntentSuffix, latestUpgradeSuffix, "-journal", "-wal", "-shm"} {
			if slices.Contains(protected, path+suffix) {
				return parents, ErrUnsafeStore
			}
		}

		dir := filepath.Dir(path)
		info, err := os.Lstat(dir)
		resolved, resolveErr := filepath.EvalSymlinks(dir)
		if err != nil || !info.IsDir() || !privateOwned(info) || resolveErr != nil || resolved != dir {
			return parents, ErrUnsafeStore
		}

		parents[i] = restoreIdentity(info)
	}

	return parents, nil
}

func (i restoreIntent) planID() (string, error) {
	plan := i.Plan
	plan.ID, plan.Pending, plan.RecoveryDirs = "", false, [2]string{}
	plan.Installed = false

	data, err := json.Marshal(struct {
		Plan      RestorePlan
		Parents   [2]upgradeFile
		Originals []restoreFile
	}{plan, i.Parents, i.Originals})
	if err != nil {
		return "", err
	}

	return digest(data), nil
}

func (i restoreIntent) valid() bool {
	id, err := i.planID()
	if err != nil || id != i.Plan.ID {
		return false
	}

	for n, dir := range i.Plan.RecoveryDirs {
		if filepath.Dir(dir) != filepath.Dir(i.Plan.Stores[n]) || filepath.Clean(dir) != dir ||
			!strings.HasPrefix(filepath.Base(dir), ".wake-restore-") ||
			i.Staged[n].Path != filepath.Join(dir, "snapshot.db") || i.Staged[n].Store != n {
			return false
		}

		for _, path := range []string{dir, filepath.Join(dir, "previous")} {
			info, e := os.Lstat(path)
			if e != nil || !info.IsDir() || !privateOwned(info) {
				return false
			}
		}
	}

	for _, file := range i.Originals {
		if file.Store < 0 || file.Store >= len(i.Plan.Stores) {
			return false
		}

		store := i.Plan.Stores[file.Store]
		suffix, hasPrefix := strings.CutPrefix(file.Path, store)
		if filepath.Dir(file.Path) != filepath.Dir(store) ||
			(!hasPrefix || !slices.Contains([]string{"", "-journal", "-wal", "-shm", UpgradeIntentSuffix}, suffix)) &&
				!isStoreSuperJournal(store, file.Path) {
			return false
		}
	}

	return true
}

func restoreIdentity(info os.FileInfo) upgradeFile {
	stat := info.Sys().(*syscall.Stat_t)
	return upgradeFile{Device: stat.Dev, Inode: stat.Ino}
}

func inspectRestoreFile(ctx context.Context, path string, store int) (restoreFile, error) {
	f, err := openBackup(path)
	if err != nil {
		return restoreFile{}, err
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return restoreFile{}, err
	}

	hash := sha256.New()
	if _, err = io.Copy(hash, backupReader{ctx, f}); err != nil {
		return restoreFile{}, err
	}

	return restoreFile{
		Path:     path,
		Store:    store,
		Digest:   hex.EncodeToString(hash.Sum(nil)),
		Identity: restoreIdentity(info),
	}, nil
}

func restoreInventory(ctx context.Context, paths [2]string, protected []string) ([]restoreFile, error) {
	var files []restoreFile
	for i, path := range paths {
		sidecars, err := storeSidecars(path)
		if err != nil {
			return nil, err
		}

		for _, candidate := range append(sidecars, path, path+UpgradeIntentSuffix) {
			file, e := inspectRestoreFile(ctx, candidate, i)
			if os.IsNotExist(e) {
				continue
			}

			if e != nil {
				return nil, e
			}

			for _, fixed := range protected {
				if fixed == candidate {
					return nil, ErrUnsafeStore
				}
			}

			files = append(files, file)
		}
	}

	return files, nil
}

func unpackBackup(ctx context.Context, archive string, paths [2]string) (backupManifest, string, error) {
	f, err := openBackup(archive)
	if err != nil {
		return backupManifest{}, "", err
	}
	defer func() { _ = f.Close() }()

	var (
		outputs [2]io.Writer
		files   [2]*os.File
	)
	for i, path := range paths {
		//nolint:gosec // The destination is a fixed snapshot name inside a private staging directory.
		file, e := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if e != nil {
			return backupManifest{}, "", e
		}
		defer func() { _ = file.Close() }()

		files[i], outputs[i] = file, file
	}

	hash := sha256.New()
	reader := io.TeeReader(backupReader{ctx, f}, hash)

	manifest, err := readBackupPair(reader, outputs)
	if err != nil {
		return manifest, "", err
	}

	if _, err = io.Copy(io.Discard, reader); err != nil {
		return manifest, "", err
	}

	for _, file := range files {
		if err = errors.Join(file.Sync(), file.Close()); err != nil {
			return manifest, "", err
		}
	}

	return manifest, hex.EncodeToString(hash.Sum(nil)), nil
}

func validateRestorePair(
	ctx context.Context,
	paths [2]string,
	manifest backupManifest,
) (backupManifest, error) {
	var actual backupManifest

	baseline := releaseBaseline()
	for i, path := range paths {
		db := sql.OpenDB(&connector{paths: [2]string{path}, probe: true})
		defer func() { _ = db.Close() }()

		db.SetMaxOpenConns(1)

		var kind string
		if err := db.QueryRowContext(ctx, profileStatement("Metadata", "main")).
			Scan(&kind, &actual.Formats[i], &actual.IDs[i]); err != nil {
			return actual, errors.Join(ErrUnsupportedSchema, err)
		}

		if kind != storeKinds[i] || actual.Formats[i] != baseline.formats[i] || actual.IDs[i] == "" {
			return actual, ErrUnsupportedSchema
		}

		var count int
		if err := db.QueryRowContext(ctx, profileStatement("ObjectCount", "main")).Scan(&count); err != nil {
			return actual, err
		}

		if count != strings.Count(baseline.schemas[i], "CREATE TABLE") {
			return actual, ErrUnsupportedSchema
		}

		if err := verifyBackupSource(ctx, db); err != nil {
			return actual, err
		}

		if err := checkForeignKeys(ctx, db, "main"); err != nil {
			return actual, err
		}
	}

	db := sql.OpenDB(&connector{paths: [2]string{paths[0]}, probe: true})
	defer func() { _ = db.Close() }()

	db.SetMaxOpenConns(1)

	var accessID string
	if err := db.QueryRowContext(ctx, profileStatement("Profile", "")).Scan(&accessID); err != nil {
		return actual, err
	}

	if accessID != actual.IDs[1] {
		return actual, ErrAssociation
	}

	if err := attachBackupAccess(ctx, db, paths[1]); err != nil {
		return actual, err
	}

	if err := checkProviderAssociations(ctx, db); err != nil {
		return actual, err
	}

	if err := db.QueryRowContext(ctx, profileStatement("HistoryVersion", "main")).
		Scan(&actual.Rules[0], &actual.Rules[1]); err != nil {
		return actual, err
	}

	rule, err := ruleVersion(baseline.rule)
	if err != nil ||
		(actual.Rules != [2]int64{rule, 0} && (actual.Rules[0] <= 0 || actual.Rules[0] >= rule || actual.Rules[1] != rule)) {
		return actual, ErrHistoryVersion
	}

	if manifest.Version != 0 &&
		(manifest.IDs != actual.IDs || manifest.Formats != actual.Formats || manifest.Rules != actual.Rules) {
		return actual, ErrCorrupt
	}

	return actual, nil
}

func pauseRestoredWarehouse(ctx context.Context, path string) error {
	uri := backupURI(path, "mode=rw&_pragma=synchronous(EXTRA)")

	db, err := sql.Open("sqlite", uri)
	if err != nil {
		return err
	}

	defer func() { _ = db.Close() }()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}

	defer func() { _ = tx.Rollback() }()

	if _, err = tx.ExecContext(ctx, profileStatement("RestoreWarehouse", ""), rand.Text()); err != nil {
		return err
	}

	return tx.Commit()
}

func renameRestore(from, to string) error {
	if err := unix.Renameat2(unix.AT_FDCWD, from, unix.AT_FDCWD, to, unix.RENAME_NOREPLACE); err != nil {
		return err
	}

	return errors.Join(syncUpgradeDir(from), syncUpgradeDir(to))
}

func moveRestoreFile(ctx context.Context, file restoreFile, to string) error {
	got, err := inspectRestoreFile(ctx, to, file.Store)
	if err == nil {
		if got.Identity != file.Identity || got.Digest != file.Digest {
			return ErrRestoreChanged
		}

		return errors.Join(syncUpgradeDir(file.Path), syncUpgradeDir(to))
	}

	if !os.IsNotExist(err) {
		return err
	}

	got, err = inspectRestoreFile(ctx, file.Path, file.Store)
	if err != nil || got != file {
		return ErrRestoreChanged
	}

	return renameRestore(file.Path, to)
}

func installRestore(ctx context.Context, intent restoreIntent, locked map[string]bool) error {
	paths := intent.Plan.Stores
	record := paths[0] + restoreSuffix
	guard := paths[1] + restoreSuffix

	var marker string

	err := readPrivateRecord(guard, &marker)
	if err == nil && marker != intent.Plan.ID || err != nil && !os.IsNotExist(err) {
		return ErrRestorePending
	}

	if !intent.Committed {
		if err = verifyRestoreLiveFiles(ctx, intent); err != nil {
			return err
		}

		for _, old := range intent.Originals {
			if old.Path != paths[old.Store] || locked[old.Path] {
				continue
			}

			if _, e := os.Lstat(displacedPath(intent, old)); os.IsNotExist(e) {
				file, e := lockStoreFile(old.Path, false)
				if e != nil {
					return e
				}
				defer func() { _ = file.Close() }()
			}
		}

		for i, staged := range intent.Staged {
			path := staged.Path
			if _, e := os.Lstat(path); os.IsNotExist(e) {
				path = paths[i]
			}

			file, e := lockStoreFile(path, false)
			if e != nil {
				return e
			}

			defer func() { _ = file.Close() }()
		}

		if err = writePrivateRecord(guard, intent.Plan.ID); err != nil {
			return err
		}

		// The displaced and staged filenames stay on their own filesystems.
		for _, old := range intent.Originals {
			if err = moveRestoreFile(ctx, old, displacedPath(intent, old)); err != nil {
				return err
			}
		}

		for i, staged := range intent.Staged {
			if err = moveRestoreFile(ctx, staged, paths[i]); err != nil {
				return err
			}
		}

		intent.Committed = true
		if err = writePrivateRecord(record, intent); err != nil {
			return err
		}
	}

	if err = os.Remove(guard); err != nil && !os.IsNotExist(err) {
		return err
	}

	if err = syncUpgradeDir(guard); err != nil {
		return err
	}

	if err = os.Remove(record); err != nil {
		return err
	}

	return syncUpgradeDir(record)
}

func verifyRestoreLiveFiles(ctx context.Context, intent restoreIntent) error {
	current, err := restoreInventory(ctx, intent.Plan.Stores, nil)
	if err != nil {
		return err
	}

	for _, file := range current {
		if slices.Contains(intent.Originals, file) {
			continue
		}

		staged := intent.Staged[file.Store]
		staged.Path = intent.Plan.Stores[file.Store]
		if staged != file {
			return ErrRestoreChanged
		}
	}

	return nil
}

func displacedPath(intent restoreIntent, old restoreFile) string {
	return filepath.Join(intent.Plan.RecoveryDirs[old.Store], "previous", filepath.Base(old.Path))
}
