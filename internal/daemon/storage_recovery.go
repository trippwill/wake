// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"go.trippwill.dev/wake/internal/config"
	persistence "go.trippwill.dev/wake/internal/persistence/sqlite"
)

func BackupStorage(ctx context.Context, cfg config.Config, configPath, artifact string) (string, error) {
	return stoppedStorage(
		cfg,
		configPath,
		artifact,
		true,
		func(profile persistence.Profile, archive string) error {
			return persistence.Backup(ctx, profile, archive)
		},
	)
}

func RestoreStorage(ctx context.Context, cfg config.Config, configPath, artifact string) ([]string, error) {
	var paths []string

	_, err := stoppedStorage(
		cfg,
		configPath,
		artifact,
		false,
		func(profile persistence.Profile, archive string) error {
			var err error

			paths, err = persistence.Restore(ctx, profile, archive)

			return err
		},
	)

	return paths, err
}

func stoppedStorage(
	cfg config.Config, configPath, artifact string, creating bool,
	fn func(persistence.Profile, string) error,
) (string, error) {
	if err := config.Prepare(cfg); err != nil {
		return "", err
	}

	profile, archive, err := recoveryPaths(cfg, configPath, artifact, creating)
	if err != nil {
		return "", err
	}

	owner, err := acquireProcessOwnership(cfg.Paths.RuntimeDir)
	if err != nil {
		return "", err
	}
	defer owner.close()

	return archive, fn(profile, archive)
}

func recoveryPaths(
	cfg config.Config,
	configPath, artifact string,
	creating bool,
) (persistence.Profile, string, error) {
	var profile persistence.Profile

	for ptr, path := range map[*string]string{
		&profile.WarehousePath:   cfg.Storage.WarehousePath,
		&profile.ConnectionsPath: cfg.Storage.ConnectionsPath,
	} {
		canonical, err := storagePath(path)
		if err != nil {
			return profile, "", err
		}

		*ptr = canonical
	}

	archive, err := storagePath(artifact)
	if err != nil {
		return profile, "", err
	}

	if err := profile.CheckArtifactPath(archive, creating); err != nil {
		return profile, "", err
	}

	for _, path := range []string{
		configPath, cfg.Credentials.KeyPath,
		filepath.Join(cfg.Paths.ConfigDir, "config.toml"),
		filepath.Join(cfg.Paths.ConfigDir, "credentials.key"),
		filepath.Join(cfg.Paths.ConfigDir, "auth.json"),
		filepath.Join(cfg.Paths.RuntimeDir, "wake.lock"),
	} {
		if path == "" {
			continue
		}

		for _, candidate := range []string{archive, profile.WarehousePath, profile.ConnectionsPath} {
			if overlapsProtectedPath(candidate, path) {
				return profile, "", fmt.Errorf("%w: %s", persistence.ErrUnsafeStore, candidate)
			}
		}
	}

	if creating {
		if _, err := os.Lstat(archive); err == nil {
			return profile, "", fmt.Errorf("output already exists: %s: %w", archive, os.ErrExist)
		} else if !os.IsNotExist(err) {
			return profile, "", err
		}
	}

	return profile, archive, nil
}

func overlapsProtectedPath(candidate, path string) bool {
	canonical, err := storagePath(path)
	if err == nil && candidate == canonical {
		return true
	}

	actual, err := filepath.EvalSymlinks(path)

	return err == nil && candidate == actual
}

func storagePath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}

	parent, err := filepath.EvalSymlinks(filepath.Dir(absolute))
	if err != nil {
		return "", err
	}

	return filepath.Join(parent, filepath.Base(absolute)), nil
}
