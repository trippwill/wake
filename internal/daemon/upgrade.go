// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package daemon

import (
	"context"
	"log/slog"
	"path/filepath"

	"go.trippwill.dev/wake/internal/config"
	"go.trippwill.dev/wake/internal/credential"
	persistence "go.trippwill.dev/wake/internal/persistence/sqlite"
)

const ErrUpgradeConfirmation = persistence.ErrUpgradeConfirmation

type (
	UpgradePlan = persistence.UpgradePlan
	RestorePlan = persistence.RestorePlan
)

func RestoreStorage(
	ctx context.Context,
	cfg config.Config,
	configPath, archive, confirm string,
) (RestorePlan, error) {
	return persistence.Restore(ctx, persistence.Profile{
		WarehousePath: cfg.Storage.WarehousePath, ConnectionsPath: cfg.Storage.ConnectionsPath,
	}, archive, confirm, protectedStoragePaths(cfg, configPath))
}

func protectedStoragePaths(cfg config.Config, configPath string) []string {
	candidates := []string{configPath, cfg.Credentials.MasterKeyPath}
	for _, name := range []string{"config.toml", "master.key", "admin-unlock", "admin-session.key", "admin-proxy.key"} {
		candidates = append(candidates, filepath.Join(cfg.Paths.ConfigDir, name))
	}

	for _, name := range []string{"wake.sock", "wake.sock.lock"} {
		candidates = append(candidates, filepath.Join(cfg.Paths.RuntimeDir, name))
	}

	var protected []string
	for _, path := range candidates {
		if path == "" {
			continue
		}

		protected = append(protected, filepath.Clean(path))
		if resolved, err := filepath.EvalSymlinks(path); err == nil {
			protected = append(protected, resolved)
		} else if parent, e := filepath.EvalSymlinks(filepath.Dir(path)); e == nil {
			protected = append(protected, filepath.Join(parent, filepath.Base(path)))
		}
	}

	return protected
}

func BackupStorage(ctx context.Context, cfg config.Config, logger *slog.Logger) (string, error) {
	return persistence.Backup(ctx, persistence.Profile{
		WarehousePath:   cfg.Storage.WarehousePath,
		ConnectionsPath: cfg.Storage.ConnectionsPath,
		ProtectedPaths:  protectedStoragePaths(cfg, cfg.Paths.ConfigFile),
	}, logger)
}

func UpgradeStorage(
	ctx context.Context,
	cfg config.Config,
	logger *slog.Logger,
	confirm string,
) (UpgradePlan, error) {
	if confirm != "" && cfg.Credentials.MasterKeyPath != "" {
		key, err := credential.LoadMasterKey(cfg.Credentials.MasterKeyPath)
		if err != nil {
			return UpgradePlan{}, err
		}

		key.Clear()
	}

	registrations, err := (sourceOptions{}).registrations()
	if err != nil {
		return UpgradePlan{}, err
	}

	return persistence.Upgrade(ctx, persistence.Profile{
		WarehousePath:   cfg.Storage.WarehousePath,
		ConnectionsPath: cfg.Storage.ConnectionsPath,
		Registrations:   registrations,
		ProtectedPaths:  protectedStoragePaths(cfg, cfg.Paths.ConfigFile),
	}, logger, confirm)
}
