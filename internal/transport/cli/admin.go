// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package cli

import (
	"context"
	"encoding/json"
	"os"

	"github.com/urfave/cli/v3"
	"go.trippwill.dev/wake/internal/config"
	"go.trippwill.dev/wake/internal/daemon"
	"go.trippwill.dev/wake/internal/transport/admin"
	"golang.org/x/term"
)

func performSetUnlock(ctx context.Context, c *cli.Command) error {
	if err := requireNoArgs(c); err != nil {
		return err
	}

	resolved, err := config.ResolveReadOnly(c)
	if err != nil {
		return failure("configure operator access: %v", err)
	}

	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return failure("read operator unlock: %v", errTerminalRequired)
	}

	if err := config.CheckPrivateDir(resolved.Config.Paths.ConfigDir); err != nil {
		return failure("set operator unlock: %v", err)
	}

	unlock, err := readOperatorUnlock(ctx, c)
	if err != nil {
		return failure("read operator unlock: %v", err)
	}
	defer clear(unlock)

	if err := admin.SetUnlock(resolved.Config.Paths.ConfigDir, unlock); err != nil {
		return failure("set operator unlock: %v", err)
	}

	if commandJSON(c) {
		return json.NewEncoder(c.Writer).Encode(map[string]bool{"unlock_set": true})
	}

	writeString(c.Writer, "Operator unlock set. Sign in with the new unlock.\n")

	return nil
}

func performResetAccess(ctx context.Context, c *cli.Command) error {
	if err := requireNoArgs(c); err != nil {
		return err
	}

	resolved, err := config.Resolve(c)
	if err != nil {
		return failure("configure access reset: %v", err)
	}

	if err := daemon.ResetAccess(ctx, resolved.Config); err != nil {
		return failure("reset access: %v", err)
	}

	if commandJSON(c) {
		return json.NewEncoder(c.Writer).Encode(map[string]bool{"access_reset": true})
	}

	writef(c.Writer, "Revoked all API clients and browser sessions. The operator unlock is unchanged.\n")

	return nil
}

func clear(value []byte) {
	for i := range value {
		value[i] = 0
	}
}
