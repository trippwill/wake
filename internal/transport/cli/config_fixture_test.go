// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package cli

import (
	"context"
	"io"

	"github.com/urfave/cli/v3"
	"go.trippwill.dev/wake/internal/config"
)

func launchForTest(path string, prepare bool) (config.Resolution, error) {
	var out config.Resolution

	command := &cli.Command{
		Name: "wake-test", Writer: io.Discard, ErrWriter: io.Discard,
		Flags: append([]cli.Flag{&cli.StringFlag{Name: "config"}}, config.Flags()...),
		Action: func(_ context.Context, c *cli.Command) error {
			var err error

			out, err = config.Resolve(c)
			if err == nil && prepare {
				err = config.Prepare(out.Config)
			}

			return err
		},
	}
	args := []string{"wake-test"}
	if path != "" {
		args = append(args, "--config="+path)
	}

	err := command.Run(context.Background(), args)

	return out, err
}
