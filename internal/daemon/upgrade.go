// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package daemon

import (
	"context"
	"log/slog"

	"go.trippwill.dev/wake/internal/config"
	persistence "go.trippwill.dev/wake/internal/persistence/sqlite"
)

const ErrUpgradeConfirmation = persistence.ErrUpgradeConfirmation

type UpgradePlan = persistence.UpgradePlan

func UpgradeStorage(
	ctx context.Context,
	cfg config.Config,
	_ *slog.Logger,
	confirm string,
) (UpgradePlan, error) {
	return persistence.PreviewFinancial(ctx, cfg.Storage.WarehousePath, confirm)
}
