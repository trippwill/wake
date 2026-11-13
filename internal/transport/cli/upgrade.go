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

func performBackup(ctx context.Context, c *cli.Command) error {
	if err := requireNoArgs(c); err != nil {
		return err
	}

	resolved, err := config.ResolveReadOnly(c)
	if err != nil {
		return failure("configure backup: %v", err)
	}

	path, err := daemon.BackupStorage(ctx, resolved.Config, newLogger(c.ErrWriter, resolved.Config.Log.Level))
	if err != nil {
		return failure("backup: %v", err)
	}

	if commandJSON(c) {
		return json.NewEncoder(c.Writer).Encode(struct {
			BackupPath string `json:"backup_path"`
		}{path})
	}

	writef(c.Writer, "Backup written to %s\n", path)

	return nil
}

func performRestore(ctx context.Context, c *cli.Command) error {
	if err := requireNoArgs(c); err != nil {
		return err
	}

	archive, confirm := c.String("archive"), c.String("confirm")
	if archive == "" || c.IsSet("confirm") && confirm == "" {
		return usageError("restore requires --archive; --confirm requires the plan ID from wake restore")
	}

	resolved, err := config.ResolveReadOnly(c)
	if err != nil {
		return failure("configure restore: %v", err)
	}

	plan, err := daemon.RestoreStorage(ctx, resolved.Config, resolved.ConfigPath, archive, confirm)
	if err != nil {
		return failure("restore: %v", err)
	}

	if commandJSON(c) {
		return json.NewEncoder(c.Writer).Encode(plan)
	}

	writef(
		c.Writer,
		"Archive %s (SHA-256 %s)\nApplied rule %d, pending rule %d\n",
		plan.Archive,
		plan.ArchiveDigest,
		plan.Rules[0],
		plan.Rules[1],
	)

	for i, path := range plan.Stores {
		writef(
			c.Writer,
			"Restore %s to %s. Displaced files go to %s.\n",
			plan.Formats[i],
			path,
			plan.RecoveryDirs[i],
		)
	}

	for _, path := range plan.Displaced {
		writef(c.Writer, "Preserve %s\n", path)
	}

	if confirm == "" {
		if plan.Pending {
			writeString(c.Writer, "An approved restore is pending.\n")
		}

		writef(c.Writer, "Run wake restore again with the same options and --confirm %s\n", plan.ID)
	} else {
		writeString(
			c.Writer,
			"Restore complete. Start Wake normally and review access before resuming upstream work.\n",
		)
	}

	return nil
}

func performUpgrade(ctx context.Context, c *cli.Command) error {
	if err := requireNoArgs(c); err != nil {
		return err
	}

	confirm := c.String("confirm")
	if c.IsSet("confirm") && confirm == "" {
		return usageError("--confirm requires the plan ID from wake upgrade")
	}

	resolved, err := config.ResolveReadOnly(c)
	if err != nil {
		return failure("configure upgrade: %v", err)
	}

	plan, err := daemon.UpgradeStorage(
		ctx,
		resolved.Config,
		newLogger(c.ErrWriter, resolved.Config.Log.Level),
		confirm,
	)
	if err != nil {
		return failure("upgrade: %v", err)
	}

	return writeUpgradeResult(c.Writer, commandJSON(c), confirm != "", resolved, plan)
}

func writeUpgradeResult(
	writer io.Writer,
	asJSON, confirmed bool,
	resolved config.Resolution,
	plan daemon.UpgradePlan,
) error {
	if asJSON {
		return json.NewEncoder(writer).Encode(struct {
			Event     string `json:"event"`
			Confirmed bool   `json:"confirmed"`
			daemon.UpgradePlan
		}{"upgrade_plan", confirmed, plan})
	}

	if confirmed {
		writef(
			writer,
			"Upgrade applied. Pre-upgrade backup %s. Start Wake normally to continue.\n",
			plan.BackupPath,
		)

		return nil
	}

	for _, store := range plan.Stores {
		from := store.Current
		if from == "" {
			from = "missing"
		}

		writef(writer, "%s %s (%s → %s)\n", store.Kind, store.Path, from, store.Target)
	}

	for _, step := range plan.Steps {
		writef(writer, "Migrate %s from %s to %s\n", step.Store, step.From, step.To)
	}

	if plan.BackupPath != "" {
		writef(writer, "Pre-upgrade backup %s\n", plan.BackupPath)
	}

	if plan.Rebuild != "" {
		writef(writer, "History rebuild %s for %s. Temporarily unavailable: %s.\n",
			plan.Rebuild, plan.TargetRule, strings.Join(plan.AffectedViews, ", "))

		for _, note := range plan.ReleaseNotes {
			writef(writer, "\nRule version %d\n%s\n", note.Version, note.Text)
		}
	}

	switch {
	case plan.RecoveryPending:
		writeString(
			writer,
			"An approved upgrade was interrupted. Start the matching Wake build to recover it.\n",
		)
	case plan.ID != "":
		writef(writer, "\nConfirm with %s\n", upgradeConfirmationCommand(resolved, plan.ID))
	case plan.Rebuild == "approved":
		writeString(writer, "Start Wake to resume the approved rebuild.\n")
	default:
		writeString(writer, "No upgrade needed.\n")
	}

	return nil
}

func upgradeConfirmationCommand(resolved config.Resolution, id string) string {
	args := []string{"wake", "upgrade"}
	if resolved.ConfigSource != "default" && resolved.ConfigPath != "" ||
		resolved.Config.Paths.ConfigFile != "" {
		args = append(args, "--config", shellQuote(resolved.ConfigPath))
	}

	for _, value := range resolved.Values {
		if value.Source != "flag" {
			continue
		}

		for _, setting := range config.Settings() {
			if value.Key == setting.Key {
				args = append(args, "--"+setting.Flag+"="+shellQuote(fmt.Sprint(value.Value)))
				break
			}
		}
	}

	return strings.Join(append(args, "--confirm", id), " ")
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
