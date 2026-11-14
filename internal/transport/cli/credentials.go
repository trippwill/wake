// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package cli

import (
	"context"
	"encoding/json"

	"github.com/urfave/cli/v3"
	"go.trippwill.dev/wake/internal/config"
	"go.trippwill.dev/wake/internal/daemon"
)

func performGenerateCredentialKey(ctx context.Context, c *cli.Command) error {
	if err := requireNoArgs(c); err != nil {
		return err
	}

	resolved, err := config.Resolve(c)
	if err != nil {
		return failure("configure credential key generation: %v", err)
	}

	if err := daemon.GenerateCredentialKey(ctx, resolved.Config); err != nil {
		return failure("generate credential encryption key: %v", err)
	}

	keyPath := resolved.Config.Credentials.KeyPath

	if commandJSON(c) {
		return json.NewEncoder(c.Writer).Encode(map[string]any{
			"generated": true, "config_path": resolved.ConfigPath, "key_path": keyPath,
		})
	}

	writef(c.Writer, "Credential encryption key generated at %s.\n", keyPath)

	return nil
}
