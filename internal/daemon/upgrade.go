// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package daemon

import (
	"context"
	"log/slog"

	"go.trippwill.dev/wake/internal/config"
	"go.trippwill.dev/wake/internal/credential"
	persistence "go.trippwill.dev/wake/internal/persistence/sqlite"
)

const ErrUpgradeConfirmation = persistence.ErrUpgradeConfirmation

type UpgradePlan = persistence.UpgradePlan

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
	}, logger, confirm)
}
