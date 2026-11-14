// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package cli

import (
	"context"
	"encoding/json"
	"os"

	"github.com/urfave/cli/v3"
	"go.trippwill.dev/wake/internal/clientaccess"
	"go.trippwill.dev/wake/internal/config"
	"go.trippwill.dev/wake/internal/daemon"
	"go.trippwill.dev/wake/internal/transport/admin"
	"golang.org/x/term"
)

func performSetPassword(ctx context.Context, c *cli.Command) error {
	if err := requireNoArgs(c); err != nil {
		return err
	}

	resolved, err := config.ResolveReadOnly(c)
	if err != nil {
		return failure("configure browser password: %v", err)
	}

	if err := config.CheckPrivateDir(resolved.Config.Paths.ConfigDir); err != nil {
		return failure("set browser password: %v", err)
	}

	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return usageError("setting a password requires a terminal")
	}

	password, err := readOperatorPassword(ctx, c)
	defer clear(password)

	if err != nil {
		return failure("read browser password: %v", err)
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	if err := admin.SetPassword(resolved.Config.Paths.ConfigDir, password); err != nil {
		return failure("set browser password: %v", err)
	}

	loginURL := resolved.Config.Admin.Origin() + "/login"
	if commandJSON(c) {
		return json.NewEncoder(c.Writer).Encode(map[string]any{"password_set": true, "url": loginURL})
	}

	writef(c.Writer, "Password set. Sign in at %s.\n", loginURL)

	return nil
}

func performRevokeSessions(ctx context.Context, c *cli.Command) error {
	if err := requireNoArgs(c); err != nil {
		return err
	}

	resolved, err := config.ResolveReadOnly(c)
	if err != nil {
		return failure("configure browser session revocation: %v", err)
	}

	if err := clientaccess.NewFileStore(resolved.Config.Paths.ConfigDir).
		RevokeBrowserSessions(ctx); err != nil {
		return failure("revoke browser sessions: %v", err)
	}

	if commandJSON(c) {
		return json.NewEncoder(c.Writer).Encode(map[string]bool{"sessions_revoked": true})
	}

	writeString(c.Writer, "Revoked all browser sessions. The password is still valid.\n")

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

	writef(
		c.Writer,
		"Revoked all API clients and browser sessions. The browser password is still valid.\n",
	)

	return nil
}
