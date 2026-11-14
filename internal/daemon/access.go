// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package daemon

import (
	"context"

	"go.trippwill.dev/wake/internal/clientaccess"
	"go.trippwill.dev/wake/internal/config"
)

// ResetAccess revokes restored client and browser authority without opening SQLite.
func ResetAccess(ctx context.Context, cfg config.Config) error {
	return WithStoppedProfile(cfg, func() error {
		return clientaccess.NewFileStore(cfg.Paths.ConfigDir).Reset(ctx)
	})
}
