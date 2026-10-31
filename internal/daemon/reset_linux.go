// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"go.trippwill.dev/wake/internal/config"
)

type ResetTarget string

const (
	ResetWarehouse   ResetTarget    = "warehouse"
	ResetCredentials ResetTarget    = "credentials"
	ErrResetTarget   OwnershipError = "reset requires a target: credentials or warehouse"
	ErrUnsafeReset   OwnershipError = "reset refuses unsafe, linked or protected paths"
	ErrResetChanged  OwnershipError = "storage paths changed during reset; inspect remaining files and preview again"
)

func ParseResetTarget(value string) (ResetTarget, error) {
	target := ResetTarget(value)
	switch target {
	case ResetWarehouse, ResetCredentials:
		return target, nil
	default:
		return "", ErrResetTarget
	}
}

type ResetDatabase struct {
	Name   ResetTarget `json:"name"`
	Path   string      `json:"path"`
	Exists bool        `json:"exists"`
}
type ResetResult struct {
	Target    ResetTarget     `json:"target"`
	DryRun    bool            `json:"dry_run"`
	Databases []ResetDatabase `json:"databases"`
	Files     []string        `json:"files"`
	Removed   []string        `json:"removed"`
}

type resetSnapshot map[string]os.FileInfo

// ResetStorage never opens SQLite, reads credentials or creates missing paths.
// Even a single-store reset holds every existing store's ownership until it ends.
func ResetStorage(
	ctx context.Context,
	cfg config.Config,
	configPath string,
	target ResetTarget,
	confirm bool,
	phase func(string),
) (ResetResult, error) {
	if _, err := ParseResetTarget(string(target)); err != nil {
		return ResetResult{}, err
	}

	if phase == nil {
		phase = func(string) {}
	}

	phase("checking storage paths and ownership")

	all := []ResetDatabase{
		{Name: ResetWarehouse, Path: cfg.Storage.WarehousePath},
		{Name: ResetCredentials, Path: cfg.Storage.ConnectionsPath},
	}
	selected := []ResetDatabase{}
	snapshots := resetSnapshot{}
	parents := resetSnapshot{}
	files := []string{}

	protected := map[string]bool{}
	for _, path := range []string{configPath, cfg.Credentials.MasterKeyPath} {
		if path != "" {
			protected[filepath.Clean(path)] = true
		}
	}

	if cfg.Paths.ConfigDir != "" {
		for _, name := range []string{"config.toml", "master.key", "admin-unlock", "admin-session.key", "admin-proxy.key"} {
			protected[filepath.Join(cfg.Paths.ConfigDir, name)] = true
		}
	}

	if cfg.Paths.RuntimeDir != "" {
		for _, name := range []string{"wake.sock", "wake.sock.lock"} {
			protected[filepath.Join(cfg.Paths.RuntimeDir, name)] = true
		}
	}

	for _, db := range all {
		if !filepath.IsAbs(db.Path) || filepath.Clean(db.Path) != db.Path {
			return ResetResult{}, ErrUnsafeReset
		}

		if err := snapshotResetParent(filepath.Dir(db.Path), parents); err != nil {
			return ResetResult{}, err
		}

		info, err := snapshotResetFile(db.Path, snapshots)
		if err != nil {
			return ResetResult{}, err
		}

		if (target == ResetCredentials && db.Name != ResetCredentials) ||
			(target == ResetWarehouse && db.Name == ResetCredentials) {
			continue
		}

		if protected[db.Path] {
			return ResetResult{}, fmt.Errorf("%w: %s", ErrUnsafeReset, db.Path)
		}

		db.Exists = info != nil
		selected = append(selected, db)

		sidecars, err := resetSidecars(db.Path)
		if err != nil {
			return ResetResult{}, err
		}

		for _, file := range append(sidecars, db.Path) {
			if protected[file] {
				return ResetResult{}, fmt.Errorf("%w: %s", ErrUnsafeReset, file)
			}

			info, err := snapshotResetFile(file, snapshots)
			if err != nil {
				return ResetResult{}, err
			}

			if info != nil {
				files = append(files, file)
			}
		}
	}

	held := []*os.File{}
	defer func() {
		for i := len(held) - 1; i >= 0; i-- {
			_ = held[i].Close()
		}
	}()
	// The runtime guard also catches a live daemon when a selected file is missing.
	if cfg.Paths.RuntimeDir != "" {
		if err := snapshotResetParent(cfg.Paths.RuntimeDir, parents); err != nil {
			return ResetResult{}, err
		}

		guard := filepath.Join(cfg.Paths.RuntimeDir, "wake.sock.lock")
		if _, err := snapshotResetFile(guard, snapshots); err != nil {
			return ResetResult{}, err
		}

		if snapshots[guard] != nil {
			f, err := claimOwnershipFile(
				guard,
				syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC,
				false,
			)
			if err != nil {
				return ResetResult{}, err
			}

			held = append(held, f)

			opened, e := f.Stat()
			if e != nil || !os.SameFile(snapshots[guard], opened) {
				return ResetResult{}, ErrResetChanged
			}
		}

		if err := checkOfflineSocket(filepath.Join(cfg.Paths.RuntimeDir, "wake.sock")); err != nil {
			return ResetResult{}, err
		}
	}

	for _, db := range all {
		if err := ctx.Err(); err != nil {
			return ResetResult{}, err
		}

		if snapshots[db.Path] == nil {
			continue
		}

		f, err := claimOwnershipFile(
			db.Path,
			syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC,
			false,
		)
		if err != nil {
			return ResetResult{}, err
		}

		held = append(held, f)

		opened, err := f.Stat()
		if err != nil || !os.SameFile(snapshots[db.Path], opened) {
			return ResetResult{}, ErrResetChanged
		}
	}
	// No deletion starts until every original identity and sidecar set is rechecked.
	if err := checkResetSnapshot(parents); err != nil {
		return ResetResult{}, err
	}

	if err := checkResetSnapshot(snapshots); err != nil {
		return ResetResult{}, err
	}

	for _, db := range selected {
		sidecars, err := resetSidecars(db.Path)
		if err != nil {
			return ResetResult{}, err
		}

		for _, file := range sidecars {
			if _, known := snapshots[file]; !known {
				return ResetResult{}, ErrResetChanged
			}
		}
	}

	result := ResetResult{
		Target:    target,
		DryRun:    !confirm,
		Databases: selected,
		Files:     files,
		Removed:   []string{},
	}
	if !confirm {
		return result, nil
	}

	if err := ctx.Err(); err != nil {
		return ResetResult{}, err
	}

	phase("removing selected databases and sidecars")

	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return result, err
		}

		if err := checkResetSnapshot(resetSnapshot{file: snapshots[file]}); err != nil {
			return result, err
		}

		if err := os.Remove(file); err != nil {
			return result, fmt.Errorf(
				"reset stopped after removing %d of %d files: %w",
				len(result.Removed),
				len(files),
				err,
			)
		}

		result.Removed = append(result.Removed, file)
	}

	return result, nil
}

func snapshotResetParent(path string, snapshots resetSnapshot) error {
	if _, seen := snapshots[path]; seen {
		return nil
	}

	if err := config.CheckPrivateDir(path); err != nil && !os.IsNotExist(err) {
		return err
	}

	info, err := os.Lstat(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	snapshots[path] = info

	return nil
}

func snapshotResetFile(path string, snapshots resetSnapshot) (os.FileInfo, error) {
	if info, seen := snapshots[path]; seen {
		return info, nil
	}

	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		snapshots[path] = nil
		return nil, nil
	}

	if err != nil {
		return nil, err
	}

	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || !ok || int64(stat.Uid) != int64(os.Geteuid()) || stat.Nlink != 1 {
		return nil, fmt.Errorf("%w: %s", ErrUnsafeReset, path)
	}

	snapshots[path] = info

	return info, nil
}

func checkResetSnapshot(snapshot resetSnapshot) error {
	for path, prior := range snapshot {
		current, err := os.Lstat(path)
		if prior == nil && os.IsNotExist(err) {
			continue
		}

		if err != nil || prior == nil || !os.SameFile(prior, current) || prior.Mode() != current.Mode() {
			return ErrResetChanged
		}

		if current.Mode().IsRegular() {
			stat, ok := current.Sys().(*syscall.Stat_t)
			if !ok || int64(stat.Uid) != int64(os.Geteuid()) || stat.Nlink != 1 || prior.Size() != current.Size() ||
				!prior.ModTime().Equal(current.ModTime()) {
				return ErrResetChanged
			}
		}
	}

	return nil
}

func resetSidecars(database string) ([]string, error) {
	files := []string{}
	entries, err := os.ReadDir(filepath.Dir(database))
	if os.IsNotExist(err) {
		return files, nil
	}

	if err != nil {
		return nil, err
	}

	prefix := filepath.Base(database) + "-mj"
	for _, entry := range entries {
		suffix, ok := strings.CutPrefix(entry.Name(), prefix)
		if ok && len(suffix) == 9 && suffix[6] == '9' && strings.Trim(suffix, "0123456789abcdefABCDEF") == "" {
			files = append(files, filepath.Join(filepath.Dir(database), entry.Name()))
		}
	}

	return append(files, database+"-journal", database+"-wal", database+"-shm"), nil
}
