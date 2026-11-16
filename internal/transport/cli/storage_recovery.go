// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/urfave/cli/v3"
	"go.trippwill.dev/wake/internal/config"
	"go.trippwill.dev/wake/internal/daemon"
)

const (
	financialScope    = "financial"
	installationScope = "installation"
)

type storageResult struct {
	Event        string   `json:"event"`
	Archive      string   `json:"archive"`
	Scope        string   `json:"scope"`
	Destinations []string `json:"destinations,omitempty"`
}

func performSnapshot(ctx context.Context, c *cli.Command) error {
	return runStorageRecovery(
		c,
		"snapshot",
		"copying and verifying the financial snapshot",
		func(resolved config.Resolution, artifact string) (storageResult, error) {
			archive, err := daemon.SnapshotWarehouse(ctx, resolved.Config, resolved.ConfigPath, artifact)
			return storageResult{Event: "snapshot", Archive: archive, Scope: financialScope}, err
		},
	)
}

func performBackup(ctx context.Context, c *cli.Command) error {
	return runStorageRecovery(
		c,
		"backup",
		"verifying and copying the stopped installation",
		func(resolved config.Resolution, artifact string) (storageResult, error) {
			archive, err := daemon.BackupStorage(ctx, resolved.Config, resolved.ConfigPath, artifact)
			return storageResult{Event: "backup", Archive: archive, Scope: installationScope}, err
		},
	)
}

func performRestoreFinancial(ctx context.Context, c *cli.Command) error {
	return performRestore(ctx, c, financialScope)
}

func performRestoreInstallation(ctx context.Context, c *cli.Command) error {
	return performRestore(ctx, c, installationScope)
}

func performRestore(ctx context.Context, c *cli.Command, scope string) error {
	return runStorageRecovery(
		c,
		"restore",
		"checking destinations and validating the backup",
		func(resolved config.Resolution, artifact string) (storageResult, error) {
			var (
				paths []string
				err   error
			)
			if c.Bool("replace") {
				paths, err = daemon.ReplaceStorage(ctx, resolved.Config, resolved.ConfigPath, artifact)
			} else {
				paths, err = daemon.RestoreStorage(
					ctx,
					resolved.Config,
					resolved.ConfigPath,
					artifact,
					scope == installationScope,
				)
			}

			return storageResult{
				Event:        "restore",
				Archive:      artifact,
				Scope:        scope,
				Destinations: paths,
			}, err
		},
	)
}

func performRecoverSync(ctx context.Context, c *cli.Command) error {
	if c.NArg() > 1 || c.NArg() == 1 && strings.TrimSpace(c.Args().First()) == "" {
		return usageError("recover-sync accepts at most one installation backup path")
	}

	resolved, err := config.Resolve(c)
	if err != nil {
		return failure("configure synchronization recovery: %v", err)
	}

	plan, err := daemon.RecoverSynchronization(
		ctx,
		resolved.Config,
		resolved.ConfigPath,
		c.Args().First(),
		c.String("confirm"),
	)
	if err != nil {
		return failure("recover synchronization: %v", err)
	}

	encoder := json.NewEncoder(c.Writer)
	if !commandJSON(c) {
		encoder.SetIndent("", "  ")
	}

	return encoder.Encode(plan)
}

func runStorageRecovery(
	c *cli.Command, operation, phase string,
	run func(config.Resolution, string) (storageResult, error),
) error {
	if c.NArg() != 1 || strings.TrimSpace(c.Args().First()) == "" {
		return usageError(operation + " requires one archive file path")
	}

	activity := newActivity(c.ErrWriter, commandJSON(c), operation, "resolving configured storage paths")
	defer activity.Stop()

	resolve := config.Resolve
	if operation == "snapshot" {
		resolve = config.ResolveReadOnly
	}

	resolved, err := resolve(c)
	if err != nil {
		return failure("configure %s: %v", operation, err)
	}

	activity.Phase(phase)

	result, err := run(resolved, c.Args().First())

	activity.Stop()

	if err != nil {
		return failure("%s: %v", operation, err)
	}

	return writeStorageResult(c.Writer, commandJSON(c), result)
}

func writeStorageResult(w io.Writer, asJSON bool, result storageResult) error {
	if asJSON {
		return json.NewEncoder(w).Encode(result)
	}

	switch result.Event {
	case "restore":
		_, err := fmt.Fprintf(
			w,
			"Restored %s to %s. Start Wake to read retained history.\n",
			result.Archive,
			strings.Join(result.Destinations, ", "),
		)

		return err
	case "snapshot":
		_, err := fmt.Fprintf(w, "Saved a financial snapshot to %s.\n", result.Archive)
		return err
	case "backup":
		_, err := fmt.Fprintf(
			w,
			"Saved an installation backup to %s. Keep the credential key separately.\n",
			result.Archive,
		)

		return err
	default:
		return failure("unknown storage recovery result %q", result.Event)
	}
}
