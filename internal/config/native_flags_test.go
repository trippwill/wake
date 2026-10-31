// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package config

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/urfave/cli/v3"
)

// Test inputs are parsed by the same native flags used by Wake's commands.
type Overrides struct {
	ConfigPath         string
	ConfigPathExplicit bool
	Values             map[string]string
}

func resolveForTest(overrides Overrides) (Resolution, error) {
	return runSettingsForTest(overrides, false)
}

func loadForTest(overrides Overrides) (Config, error) {
	out, err := runSettingsForTest(overrides, true)
	return out.Config, err
}

func runSettingsForTest(overrides Overrides, prepare bool) (Resolution, error) {
	var out Resolution

	command := &cli.Command{
		Name: "wake-test", Writer: io.Discard, ErrWriter: io.Discard,
		Flags: append([]cli.Flag{&cli.StringFlag{Name: "config"}}, Flags()...),
		Action: func(_ context.Context, c *cli.Command) error {
			var err error

			out, err = Resolve(c)

			if err == nil && prepare {
				err = Prepare(out.Config)
			}

			return err
		},
	}
	args := []string{"wake-test"}
	if overrides.ConfigPathExplicit {
		args = append(args, "--config="+overrides.ConfigPath)
	}

	for name, value := range overrides.Values {
		args = append(args, "--"+name+"="+value)
	}

	if err := command.Run(context.Background(), args); err != nil {
		if errors.Is(err, ErrInvalid) || errors.Is(err, ErrRuntimeDirRequired) ||
			errors.Is(err, ErrInsecurePath) {
			return out, err
		}

		return out, fmt.Errorf("%w: %v", ErrInvalid, err)
	}

	return out, nil
}
