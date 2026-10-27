// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package cli

import (
	"context"
	"encoding/json"

	"github.com/urfave/cli/v3"
	"go.trippwill.dev/wake/internal/config"
	"go.trippwill.dev/wake/internal/transport/admin"
)

func performProxyGuard(_ context.Context, c *cli.Command) error {
	if err := requireNoArgs(c); err != nil {
		return err
	}

	activity := newActivity(c.ErrWriter, c.Bool("json"), "admin set-proxy-guard", "provisioning proxy guard")
	defer activity.Stop()

	dir, err := config.ConfigDir()
	if err != nil {
		return failure("configure admin: %v", err)
	}

	guard, err := admin.SetProxyGuard(dir)
	if err != nil {
		return failure("set proxy guard: %v", err)
	}

	if c.Bool("json") {
		return json.NewEncoder(c.Writer).Encode(map[string]string{"proxy_guard": guard})
	}

	writef(c.Writer, "wake: reverse-proxy guard (store it in the proxy only): %s\n", guard)

	return nil
}

func clear(value []byte) {
	for i := range value {
		value[i] = 0
	}
}
