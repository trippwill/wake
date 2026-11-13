// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/urfave/cli/v3"
	"go.trippwill.dev/wake/internal/config"
	"go.trippwill.dev/wake/internal/daemon"
)

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

	if commandJSON(c) {
		return json.NewEncoder(c.Writer).Encode(struct {
			Event     string `json:"event"`
			Confirmed bool   `json:"confirmed"`
			daemon.UpgradePlan
		}{"upgrade_plan", confirm != "", plan})
	}

	if confirm != "" {
		writef(
			c.Writer,
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

		writef(c.Writer, "%s %s (%s → %s)\n", store.Kind, store.Path, from, store.Target)
	}

	for _, step := range plan.Steps {
		writef(c.Writer, "Migrate %s from %s to %s\n", step.Store, step.From, step.To)
	}

	if plan.BackupPath != "" {
		writef(c.Writer, "Pre-upgrade backup %s\n", plan.BackupPath)
	}

	if plan.Rebuild != "" {
		writef(c.Writer, "History rebuild %s for %s. Temporarily unavailable: %s.\n",
			plan.Rebuild, plan.TargetRule, strings.Join(plan.AffectedViews, ", "))

		for _, note := range plan.ReleaseNotes {
			writef(c.Writer, "\nRule version %d\n%s\n", note.Version, note.Text)
		}
	}

	switch {
	case plan.RecoveryPending:
		writeString(
			c.Writer,
			"An approved upgrade was interrupted. Start the matching Wake build to recover it.\n",
		)
	case plan.ID != "":
		writef(c.Writer, "\nConfirm with %s\n", upgradeConfirmationCommand(resolved, plan.ID))
	case plan.Rebuild == "approved":
		writeString(c.Writer, "Start Wake to resume the approved rebuild.\n")
	default:
		writeString(c.Writer, "No upgrade needed.\n")
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
