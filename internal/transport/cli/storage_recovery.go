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

func performBackup(ctx context.Context, c *cli.Command) error {
	return runStorageRecovery(
		c,
		"backup",
		"verifying and copying stopped storage",
		func(resolved config.Resolution, artifact string) (storageResult, error) {
			archive, err := daemon.BackupStorage(ctx, resolved.Config, resolved.ConfigPath, artifact)
			return storageResult{Event: "backup", Archive: archive, WithCredentials: true}, err
		},
	)
}

func performRestore(ctx context.Context, c *cli.Command) error {
	if !c.Bool("with-credentials") {
		return usageError("restore requires --with-credentials for a backup that contains encrypted access")
	}

	return runStorageRecovery(
		c,
		"restore",
		"checking vacant destinations and validating the backup",
		func(resolved config.Resolution, artifact string) (storageResult, error) {
			paths, err := daemon.RestoreStorage(ctx, resolved.Config, resolved.ConfigPath, artifact)

			return storageResult{
				Event:           "restore",
				Archive:         artifact,
				WithCredentials: true,
				Destinations:    paths,
			}, err
		},
	)
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

	resolved, err := config.Resolve(c)
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

	if result.Event == "restore" {
		_, err := fmt.Fprintf(
			w,
			"Restored %s to %s. Start Wake to read retained history. Supply the original credential key and review access before resuming upstream work.\n",
			result.Archive,
			strings.Join(result.Destinations, ", "),
		)

		return err
	}

	_, err := fmt.Fprintf(
		w,
		"Backed up the warehouse and encrypted access to %s. Retain the credential key separately.\n",
		result.Archive,
	)

	return err
}
