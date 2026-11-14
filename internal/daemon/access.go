// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package daemon

import (
	"context"

	"go.trippwill.dev/wake/internal/clientaccess"
	"go.trippwill.dev/wake/internal/config"
)

// ResetAccess revokes restored client and browser authority without opening SQLite.
func ResetAccess(_ context.Context, cfg config.Config) error {
	return WithStoppedProfile(cfg, clientaccess.NewFileStore(cfg.Paths.ConfigDir).Reset)
}
