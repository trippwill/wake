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

func performReset(ctx context.Context, c *cli.Command) error {
	if c.NArg() != 1 {
		return usageError("reset requires one target: credentials or warehouse")
	}

	target, err := daemon.ParseResetTarget(c.Args().First())
	if err != nil {
		return usageError(err.Error())
	}

	activity := newActivity(c.ErrWriter, c.Bool("json"), "reset", "resolving configured database files")
	defer activity.Stop()

	resolved, err := config.ResolveReadOnly(explicitSettings(c))
	if err != nil {
		return failure("configure reset: %v", err)
	}

	result, resetErr := daemon.ResetStorage(
		ctx,
		resolved.Config,
		resolved.ConfigPath,
		target,
		c.Bool("confirm"),
		activity.Phase,
	)
	activity.Stop()

	if len(result.Databases) > 0 {
		if c.Bool("json") {
			if err := json.NewEncoder(c.Writer).Encode(result); err != nil {
				return err
			}
		} else {
			if result.DryRun {
				writeString(c.Writer, "Dry run: no files deleted. Add --confirm to execute this reset.\n")
			}

			for _, db := range result.Databases {
				state := "missing"
				if db.Exists {
					state = "present"
				}

				writef(c.Writer, "%s: %s (%s)\n", db.Name, db.Path, state)
			}

			if result.DryRun {
				for _, file := range result.Files {
					writef(c.Writer, "Selected file: %s\n", file)
				}
			} else {
				for _, file := range result.Removed {
					writef(c.Writer, "Removed file: %s\n", file)
				}
			}

			if !result.DryRun {
				writef(c.Writer, "Removed %d of %d selected files.\n", len(result.Removed), len(result.Files))
			}

			if target == daemon.ResetWarehouse {
				writeString(
					c.Writer,
					"Warehouse reset clears financial history and its operational state. Credentials are retained; resume discovery and confirm an account after starting the fresh warehouse.\n",
				)
			}

			if target == daemon.ResetCredentials {
				writeString(
					c.Writer,
					"Financial data is retained. Repair credentials in the admin console and resume upstream work.\n",
				)
			}
		}
	}

	if resetErr != nil {
		return failure("reset failed: %v", resetErr)
	}

	return nil
}
