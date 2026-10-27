// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/urfave/cli/v3"
	"go.trippwill.dev/wake/internal/config"
	"go.trippwill.dev/wake/internal/credential"
)

func describeConfig(_ context.Context, c *cli.Command) error {
	if err := requireNoArgs(c); err != nil {
		return err
	}

	if c.Bool("json") {
		return json.NewEncoder(c.Writer).Encode(config.Settings())
	}

	writeString(
		c.Writer,
		"Precedence: switches > environment > TOML > built-in/XDG defaults\nConfig file: --config > WAKE_CONFIG > user-scoped optional default\n",
	)

	for _, s := range config.Settings() {
		writef(
			c.Writer,
			"%s\n  --%s | %s | %s; default: %q\n  %s\n",
			s.Key,
			s.Flag,
			s.Environment,
			s.Type,
			s.Default,
			s.Description,
		)
	}

	return nil
}

func showConfig(_ context.Context, c *cli.Command) error {
	if err := requireNoArgs(c); err != nil {
		return err
	}

	activity := newActivity(c.ErrWriter, c.Bool("json"), "config show", "resolving local launch configuration")
	defer activity.Stop()

	out, err := config.Resolve(explicitSettings(c))
	if err != nil {
		return failure("configure: %v", err)
	}

	activity.Stop()

	if c.Bool("json") {
		return json.NewEncoder(c.Writer).Encode(out)
	}

	writef(c.Writer, "Local launch configuration: %s (%s)\n", out.ConfigPath, out.ConfigSource)

	for _, v := range out.Values {
		writef(c.Writer, "%-32s %v [%s]\n", v.Key, v.Value, v.Source)
	}

	return nil
}

func getConfig(_ context.Context, c *cli.Command) error {
	if c.NArg() != 1 {
		return usageError("config get requires one setting name")
	}

	out, err := config.ResolveReadOnly(explicitSettings(c))
	if err != nil {
		return failure("configure: %v", err)
	}

	for _, value := range out.Values {
		if value.Key != c.Args().First() {
			continue
		}

		if c.Bool("json") {
			return json.NewEncoder(c.Writer).Encode(value.Value)
		}

		writef(c.Writer, "%v\n", value.Value)

		return nil
	}

	return usageError(fmt.Sprintf("unknown setting %q", c.Args().First()))
}

func checkConfig(_ context.Context, c *cli.Command) error {
	if err := requireNoArgs(c); err != nil {
		return err
	}

	activity := newActivity(c.ErrWriter, c.Bool("json"), "config check", "checking local launch configuration")
	defer activity.Stop()

	out, err := config.Resolve(explicitSettings(c))
	if err != nil {
		return failure("configure: %v", err)
	}

	if out.Config.Credentials.MasterKeyPath != "" {
		activity.Phase("verifying configured master key")

		key, err := credential.LoadMasterKey(out.Config.Credentials.MasterKeyPath)
		if err != nil {
			return failure("verify master key: %v", err)
		}

		key.Clear()
	}

	activity.Stop()

	if c.Bool("json") {
		return json.NewEncoder(c.Writer).Encode(map[string]any{"valid": true, "config_path": out.ConfigPath})
	}

	writef(c.Writer, "wake: local launch configuration is valid: %s\n", out.ConfigPath)

	return nil
}

type flagDescription struct {
	Names       []string `json:"names"`
	Type        string   `json:"type"`
	Description string   `json:"description"`
	Default     string   `json:"default"`
	Required    bool     `json:"required"`
}

func describeFlag(f cli.Flag) flagDescription {
	out := flagDescription{Names: f.Names(), Type: fmt.Sprintf("%T", f.Get())}
	if doc, ok := f.(cli.DocGenerationFlag); ok {
		out.Description = doc.GetUsage()
		out.Default = doc.GetDefaultText()
		if out.Default == "" {
			out.Default = doc.GetValue()
		}
	}

	if required, ok := f.(cli.RequiredFlag); ok {
		out.Required = required.IsRequired()
	}

	return out
}

type commandDescription struct {
	Name        string               `json:"name"`
	Description string               `json:"description"`
	Arguments   string               `json:"arguments,omitempty"`
	Interactive bool                 `json:"interactive"`
	Flags       []flagDescription    `json:"flags"`
	Commands    []commandDescription `json:"commands,omitempty"`
}

func describeCommand(c *cli.Command, path string) commandDescription {
	out := commandDescription{
		Name:        path,
		Description: c.Usage,
		Arguments:   c.ArgsUsage,
		Interactive: c.Name == "setup",
		Flags:       []flagDescription{},
	}
	for _, f := range c.Flags {
		out.Flags = append(out.Flags, describeFlag(f))
	}

	if !slices.ContainsFunc(
		out.Flags,
		func(f flagDescription) bool { return slices.Contains(f.Names, "help") },
	) {
		out.Flags = append(
			out.Flags,
			flagDescription{Names: []string{"help", "h"}, Type: "bool", Description: "Show help"},
		)
	}

	if c.Name != "wake" {
		out.Flags = append(
			out.Flags,
			flagDescription{
				Names:       []string{"json"},
				Type:        "bool",
				Description: "Structured results and status events",
			},
		)
	}

	for _, child := range c.Commands {
		out.Commands = append(out.Commands, describeCommand(child, path+" "+child.Name))
	}

	return out
}

func discoverCommands(_ context.Context, c *cli.Command) error {
	target := c.Root()
	path := "wake"
	for _, name := range c.Args().Slice() {
		var next *cli.Command
		for _, candidate := range target.Commands {
			if candidate.Name == name {
				next = candidate
				break
			}
		}

		if next == nil {
			return usageError(fmt.Sprintf("unknown command %q in %s", name, path))
		}

		target = next
		path += " " + name
	}

	if c.Bool("json") {
		return json.NewEncoder(c.Writer).Encode(describeCommand(target, path))
	}

	writef(c.Writer, "Usage: %s [options] %s\n\n%s\n", path, target.ArgsUsage, target.Usage)

	for _, child := range target.Commands {
		writef(c.Writer, "  %-20s %s\n", child.Name, child.Usage)
	}

	for _, f := range target.Flags {
		writef(c.Writer, "  %s\n", strings.TrimSpace(f.String()))
	}

	return nil
}
