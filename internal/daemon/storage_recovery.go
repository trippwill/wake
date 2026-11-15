// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"go.trippwill.dev/wake/internal/clientaccess"
	"go.trippwill.dev/wake/internal/config"
	persistence "go.trippwill.dev/wake/internal/persistence/sqlite"
)

func SnapshotWarehouse(ctx context.Context, cfg config.Config, configPath, artifact string) (string, error) {
	profile, archive, err := recoveryPaths(cfg, configPath, artifact, true)
	if err != nil {
		return "", err
	}

	return archive, persistence.SnapshotWarehouse(ctx, profile, archive)
}

func RestoreStorage(
	ctx context.Context,
	cfg config.Config,
	configPath, artifact string,
	withCredentials bool,
) ([]string, error) {
	return restoreStorage(ctx, cfg, configPath, artifact, withCredentials, false)
}

func ReplaceStorage(ctx context.Context, cfg config.Config, configPath, artifact string) ([]string, error) {
	return restoreStorage(ctx, cfg, configPath, artifact, true, true)
}

func restoreStorage(
	ctx context.Context, cfg config.Config, configPath, artifact string, withCredentials, replace bool,
) ([]string, error) {
	prepare := config.Prepare
	if withCredentials {
		prepare = config.PrepareInstallationRestore
	}

	if err := prepare(cfg); err != nil {
		return nil, err
	}

	profile, archive, err := recoveryPaths(cfg, configPath, artifact, false)
	if err != nil {
		return nil, err
	}

	owner, err := acquireProcessOwnership(cfg.Paths.RuntimeDir)
	if err != nil {
		return nil, err
	}
	defer owner.close()

	if withCredentials {
		envelope, err := openCredentialKey(cfg.Credentials.KeyPath)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		defer envelope.Clear()

		return persistence.RestoreInstallation(ctx, profile, archive, replace, envelope)
	}

	return persistence.Restore(ctx, profile, archive, false)
}

func BackupStorage(ctx context.Context, cfg config.Config, configPath, artifact string) (string, error) {
	profile, archive, err := recoveryPaths(cfg, configPath, artifact, true)
	if err != nil {
		return "", err
	}

	if err := config.Prepare(cfg); err != nil {
		return "", err
	}

	owner, err := acquireProcessOwnership(cfg.Paths.RuntimeDir)
	if err != nil {
		return "", err
	}
	defer owner.close()

	configuration, err := config.BackupEvidence(cfg, configPath)
	if err != nil {
		return "", err
	}

	auth, err := clientaccess.NewFileStore(cfg.Paths.ConfigDir).BackupEvidence()
	if err != nil {
		return "", err
	}
	defer clear(auth)

	return archive, persistence.BackupInstallation(ctx, profile, archive, persistence.InstallationEvidence{
		Configuration: configuration,
		LocalAuth:     auth,
	})
}

func recoveryPaths(
	cfg config.Config,
	configPath, artifact string,
	creating bool,
) (persistence.Profile, string, error) {
	var profile persistence.Profile

	warehouse, err := storagePath(cfg.Storage.WarehousePath)
	if err != nil {
		return profile, "", err
	}

	optionalPath := func(path string) string {
		if path != "" {
			if canonical, err := storagePath(path); err == nil {
				return canonical
			}
		}

		return path
	}
	profile = persistence.Profile{
		WarehousePath:   warehouse,
		ConnectionsPath: optionalPath(cfg.Storage.ConnectionsPath),
		SyncPath:        optionalPath(cfg.Storage.SyncPath),
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

		for _, candidate := range []string{archive, profile.WarehousePath, profile.ConnectionsPath, profile.SyncPath} {
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
