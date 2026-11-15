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

type storageResult struct {
	Event           string   `json:"event"`
	Archive         string   `json:"archive"`
	WithCredentials bool     `json:"with_credentials"`
	Destinations    []string `json:"destinations,omitempty"`
}

func performSnapshotWarehouse(ctx context.Context, c *cli.Command) error {
	return runStorageRecovery(
		c,
		"snapshot",
		"copying and verifying the warehouse snapshot",
		func(resolved config.Resolution, artifact string) (storageResult, error) {
			archive, err := daemon.SnapshotWarehouse(ctx, resolved.Config, resolved.ConfigPath, artifact)
			return storageResult{Event: "snapshot", Archive: archive}, err
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
			return storageResult{Event: "backup", Archive: archive, WithCredentials: true}, err
		},
	)
}

func performRestore(ctx context.Context, c *cli.Command) error {
	withCredentials := c.Bool("with-credentials")
	replace := c.Bool("replace")
	if replace && !withCredentials {
		return usageError("--replace requires --with-credentials")
	}

	return runStorageRecovery(
		c,
		"restore",
		"checking destinations and validating the backup",
		func(resolved config.Resolution, artifact string) (storageResult, error) {
			var (
				paths []string
				err   error
			)
			if replace {
				paths, err = daemon.ReplaceStorage(ctx, resolved.Config, resolved.ConfigPath, artifact)
			} else {
				paths, err = daemon.RestoreStorage(
					ctx,
					resolved.Config,
					resolved.ConfigPath,
					artifact,
					withCredentials,
				)
			}

			return storageResult{
				Event:           "restore",
				Archive:         artifact,
				WithCredentials: withCredentials,
				Destinations:    paths,
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
		_, err := fmt.Fprintf(w, "Saved a warehouse snapshot to %s.\n", result.Archive)
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
