// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"

	"github.com/urfave/cli/v3"
	"go.trippwill.dev/wake/internal/config"
)

const (
	exitOK      = 0
	exitFailure = 1
	exitUsage   = 2
)

type commandError struct {
	status  int
	message string
}

func (e commandError) Error() string { return e.message }
func (e commandError) ErrorCode() string {
	if e.status == exitUsage {
		return "invalid_request"
	}

	return "operation_failed"
}
func usageError(message string) error { return commandError{exitUsage, message} }
func failure(format string, args ...any) error {
	return commandError{exitFailure, fmt.Sprintf(format, args...)}
}

func Run(args []string, stdout, stderr io.Writer) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	output := &synchronizedWriter{writer: stdout}
	command := commandTree(
		output,
		&synchronizedWriter{writer: stderr},
	)

	err := command.Run(ctx, append([]string{"wake"}, args...))
	if err == nil {
		err = output.Err()
	}

	if err == nil {
		return exitOK
	}

	status := exitFailure
	if e, ok := errors.AsType[commandError](err); ok {
		status = e.status
	}

	if e, ok := errors.AsType[cli.ExitCoder](err); ok {
		status = e.ExitCode()
	}

	if commandJSON(command) {
		event := map[string]any{
			"event":       "error",
			"code":        "operation_failed",
			"message":     err.Error(),
			"exit_status": status,
		}
		if coded, ok := errors.AsType[interface {
			error
			ErrorCode() string
		}](err); ok {
			event["code"] = coded.ErrorCode()
		}

		_ = json.NewEncoder(stderr).
			Encode(event)
	} else {
		writef(stderr, "wake: %v\n", err)
	}

	return status
}

func commandTree(stdout, stderr io.Writer) *cli.Command {
	common := func() []cli.Flag {
		return []cli.Flag{
			&cli.StringFlag{Name: "config", Usage: "Absolute TOML configuration path (env: WAKE_CONFIG)"},
		}
	}

	serveFlags := append(common(), config.Flags()...)

	root := &cli.Command{
		Flags: []cli.Flag{
			&cli.BoolFlag{Name: "json", Usage: "Structured results and status events"},
		},
		Name:                          "wake",
		Usage:                         "Configure and operate the Wake daemon",
		Writer:                        stdout,
		ErrWriter:                     stderr,
		HideHelpCommand:               true,
		ExitErrHandler:                func(context.Context, *cli.Command, error) {},
		CustomRootCommandHelpTemplate: strings.ReplaceAll(cli.RootCommandHelpTemplate, "USAGE:", "Usage:"),
		Action: func(_ context.Context, c *cli.Command) error {
			if c.NArg() != 0 {
				return usageError("unknown command " + c.Args().First())
			}

			printCommandHelp(stderr, c)

			return usageError("specify a command")
		},
	}

	root.OnUsageError = func(_ context.Context, _ *cli.Command, err error, _ bool) error {
		return commandError{exitUsage, err.Error()}
	}
	root.Commands = []*cli.Command{
		{
			Name:   "setup",
			Usage:  "Guide local configuration, credentials and operator access",
			Flags:  common(),
			Action: performSetup,
		},
		{
			Name:   "serve",
			Usage:  "Run the Wake daemon",
			Flags:  serveFlags,
			Action: performServe,
		},
		{
			Name:            "admin",
			Usage:           "Provision reverse-proxy access",
			HideHelpCommand: true,
			Action: func(_ context.Context, c *cli.Command) error {
				printCommandHelp(stderr, c)

				if c.NArg() != 0 {
					return usageError("unknown admin command " + c.Args().First())
				}

				return usageError("specify an admin command")
			},
			Commands: []*cli.Command{
				{
					Name:   "set-proxy-guard",
					Usage:  "Generate the guard a reverse proxy must inject",
					Action: performProxyGuard,
				},
			},
		},
		{
			Name:            "config",
			Usage:           "Discover local launch configuration without changing it",
			HideHelpCommand: true,
			Action: func(_ context.Context, c *cli.Command) error {
				printCommandHelp(stderr, c)
				return usageError("specify config describe, show, get or check")
			},
			Commands: []*cli.Command{
				{
					Name:   "describe",
					Usage:  "Describe supported non-secret settings",
					Flags:  common(),
					Action: describeConfig,
				},
				{
					Name:   "show",
					Usage:  "Show resolved local launch values and sources",
					Flags:  append(common(), config.Flags()...),
					Action: showConfig,
				},
				{
					Name:      "get",
					Usage:     "Print one resolved setting value without commentary",
					ArgsUsage: "SETTING",
					Flags:     append(common(), config.Flags()...),
					Action:    getConfig,
				},
				{
					Name:   "check",
					Usage:  "Check launch settings and any configured master key",
					Flags:  append(common(), config.Flags()...),
					Action: checkConfig,
				},
			},
		},
		{
			Name:      "help",
			Usage:     "Discover a command; --json describes the command tree",
			ArgsUsage: "[COMMAND ...]",
			Action:    discoverCommands,
		},
	}
	for _, c := range root.Commands {
		configureHelp(c)
	}

	return root
}

func configureHelp(c *cli.Command) {
	c.CustomHelpTemplate = strings.ReplaceAll(cli.CommandHelpTemplate, "USAGE:\n   ", "Usage: ")
	c.OnUsageError = func(_ context.Context, _ *cli.Command, err error, _ bool) error {
		return commandError{exitUsage, err.Error()}
	}
	for _, child := range c.Commands {
		configureHelp(child)
	}
}

func printCommandHelp(w io.Writer, c *cli.Command) {
	if c.Bool("json") {
		return
	}

	writef(w, "Usage: %s [options] %s\n\n%s\n", c.FullName(), c.ArgsUsage, c.Usage)

	for _, child := range c.Commands {
		writef(w, "  %-20s %s\n", child.Name, child.Usage)
	}

	for _, flag := range c.Flags {
		writef(w, "  %s\n", flag.String())
	}
}

func explicitSettings(c *cli.Command) config.Overrides {
	values := map[string]string{}
	for _, s := range config.Settings() {
		if c.IsSet(s.Flag) {
			values[s.Flag] = fmt.Sprint(c.Value(s.Flag))
		}
	}

	return config.Overrides{
		ConfigPath:         c.String("config"),
		ConfigPathExplicit: c.IsSet("config"),
		Values:             values,
	}
}

func requireNoArgs(c *cli.Command) error {
	if c.NArg() != 0 {
		return usageError("unexpected argument " + c.Args().First())
	}

	return nil
}
func writef(w io.Writer, format string, args ...any) { _, _ = fmt.Fprintf(w, format, args...) }
func writeString(w io.Writer, value string)          { _, _ = io.WriteString(w, value) }

func commandJSON(c *cli.Command) bool {
	if c.Bool("json") {
		return true
	}

	return slices.ContainsFunc(c.Commands, commandJSON)
}
