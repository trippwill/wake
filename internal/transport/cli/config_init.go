// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/urfave/cli/v3"
	"go.trippwill.dev/wake/internal/config"
	"go.trippwill.dev/wake/internal/daemon"
)

type keyProvisionResult struct {
	ConfigPath   string `json:"config_path"`
	KeyGenerated bool   `json:"key_generated"`
	KeyPath      string `json:"key_path,omitempty"`
}

func performConfigInit(ctx context.Context, c *cli.Command) error {
	if err := requireNoArgs(c); err != nil {
		return err
	}

	resolved, err := config.Initialize(c)
	if errors.Is(err, config.ErrConfigExists) {
		return failure("%v. Run wake config check with the same configuration path", err)
	}

	if err != nil {
		return failure("initialize configuration: %v", err)
	}

	result := keyProvisionResult{ConfigPath: resolved.ConfigPath}
	if c.Bool("generate-key") {
		generated, err := daemon.EnsureCredentialKey(ctx, resolved.Config)
		if err != nil {
			return failure(
				"created configuration at %s, but key provisioning failed: %v. After correcting the issue, retry with wake access provision credential-key --config %s",
				result.ConfigPath,
				err,
				shellQuote(result.ConfigPath),
			)
		}

		result.KeyGenerated = generated
		result.KeyPath = resolved.Config.Credentials.KeyPath
	}

	if commandJSON(c) {
		return json.NewEncoder(c.Writer).Encode(result)
	}

	writef(c.Writer, "Configuration created at %s.\n", result.ConfigPath)
	result.writeKeyStatus(c.Writer)

	return nil
}

func performProvisionCredentialKey(ctx context.Context, c *cli.Command) error {
	if err := requireNoArgs(c); err != nil {
		return err
	}

	resolved, err := config.Resolve(c)
	if err != nil {
		return failure("configure key provisioning: %v", err)
	}

	generated, err := daemon.EnsureCredentialKey(ctx, resolved.Config)
	if err != nil {
		return failure("provision credential encryption key at %s: %v", resolved.Config.Credentials.KeyPath, err)
	}

	result := keyProvisionResult{
		ConfigPath: resolved.ConfigPath, KeyGenerated: generated, KeyPath: resolved.Config.Credentials.KeyPath,
	}
	if commandJSON(c) {
		return json.NewEncoder(c.Writer).Encode(result)
	}

	result.writeKeyStatus(c.Writer)

	return nil
}

func (result keyProvisionResult) writeKeyStatus(w io.Writer) {
	if result.KeyGenerated {
		writef(w, "Credential encryption key generated at %s.\n", result.KeyPath)
	} else if result.KeyPath != "" {
		writef(w, "Using existing credential encryption key at %s.\n", result.KeyPath)
	}
}
