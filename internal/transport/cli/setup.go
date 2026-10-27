// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package cli

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/urfave/cli/v3"
	"go.trippwill.dev/wake/internal/config"
	"go.trippwill.dev/wake/internal/credential"
	"go.trippwill.dev/wake/internal/daemon"
	"go.trippwill.dev/wake/internal/transport/admin"
	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

type setupError string

func (e setupError) Error() string { return string(e) }

const (
	errTerminalRequired setupError = "standard input is not a terminal"
	errUnlockMismatch   setupError = "operator unlocks do not match"
)

func performSetup(ctx context.Context, c *cli.Command) error {
	if c.NArg() != 0 || (c.IsSet("config") && c.String("config") == "") {
		printCommandHelp(c.ErrWriter, c)
		return usageError("setup accepts only --config")
	}

	activity := newActivity(c.ErrWriter, c.Bool("json"), "setup", "checking local configuration")
	defer activity.Stop()

	path, source, err := config.SelectConfigPath(c.String("config"), c.IsSet("config"))
	if err != nil {
		return failure("configure local setup: %v", err)
	}

	if source == "default" && !term.IsTerminal(int(os.Stdin.Fd())) {
		return failure("read setup input: %v", errTerminalRequired)
	}

	if c.Bool("json") {
		_ = json.NewEncoder(c.ErrWriter).
			Encode(map[string]string{"event": "notice", "message": "Provider connections are configured in the browser."})
	} else {
		writeString(c.ErrWriter, "Provider connections are configured in the browser.\n")
	}

	if source == "default" {
		activity.Pause()

		selected, err := promptLine(ctx, c, "Configuration save path ["+path+"]: ", false)
		if err != nil {
			return failure("read configuration save path: %v", err)
		}

		if selected != "" {
			path = selected
		}
	}

	activity.Phase("reading selected configuration and checking master key")

	setup, err := config.ReadLocalSetup(path)
	if err != nil {
		return failure("configure local setup: %v", err)
	}

	if err := checkSetupKey(setup); err != nil {
		return failure("verify master key: %v", err)
	}

	guide, err := setup.Guidance()
	if err != nil {
		return failure("configure local setup: %v", err)
	}

	activity.Phase("checking for running Wake at configured paths")

	if err := checkSetupStopped(guide); err != nil {
		return err
	}

	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return failure("read setup input: %v", errTerminalRequired)
	}

	activity.Pause()

	data, err := promptSetting(ctx, c, "data-dir", guide.DataDir)
	if err != nil {
		return failure("read setup settings: %v", err)
	}

	runtime, err := promptSetting(ctx, c, "runtime-dir", guide.RuntimeDir)
	if err != nil {
		return failure("read setup settings: %v", err)
	}

	portText, err := promptSetting(ctx, c, "admin-port", strconv.Itoa(int(guide.AdminPort)))
	if err != nil {
		return failure("read setup settings: %v", err)
	}

	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil {
		return failure("admin port must be between 1 and 65535")
	}

	setup, err = setup.WithLaunchSettings(data, runtime, uint16(port))
	if err != nil {
		return failure("configure local setup: %v", err)
	}

	proposed := config.SetupGuidance{DataDir: data, StateDir: guide.StateDir, RuntimeDir: runtime}

	activity.Phase("checking for running Wake at proposed paths")

	if err := checkSetupStopped(guide, proposed); err != nil {
		return err
	}

	activity.Pause()

	if c.Bool("json") {
		_ = json.NewEncoder(c.ErrWriter).
			Encode(map[string]any{"event": "setup_summary", "config_path": setup.ConfigPath, "data_dir": data, "state_dir": guide.StateDir, "runtime_dir": runtime, "admin_port": port, "overwrite": setup.ConfigExists()})
	} else {
		writef(
			c.ErrWriter,
			"Save configuration to %s\n  data: %s\n  state: %s\n  runtime: %s\n  browser: http://127.0.0.1:%d\n",
			setup.ConfigPath,
			data,
			guide.StateDir,
			runtime,
			port,
		)
	}

	action := "Save new configuration to "
	if setup.ConfigExists() {
		action = "Overwrite existing configuration at "
	}

	confirmation, err := promptLine(
		ctx,
		c,
		action+setup.ConfigPath+" and provision local access? [y/N]: ",
		false,
	)
	if err != nil {
		return failure("read setup confirmation: %v", err)
	}

	if strings.ToLower(confirmation) != "y" && strings.ToLower(confirmation) != "yes" {
		return failure("setup canceled; no settings or credentials saved")
	}

	unlock, err := readOperatorUnlock(ctx, c)
	if err != nil {
		return failure("read operator unlock: %v", err)
	}
	defer clear(unlock)

	activity.Phase("rechecking running Wake before provisioning")

	if err := checkSetupStopped(guide, proposed); err != nil {
		return err
	}

	activity.Phase("provisioning local access")

	if err := finishLocalSetup(setup, unlock); err != nil {
		return failure(
			"local setup incomplete: %v; keep any existing master key, correct the failure and rerun with the same configuration",
			err,
		)
	}

	activity.Stop()

	if c.Bool("json") {
		return json.NewEncoder(c.Writer).
			Encode(map[string]any{"complete": true, "config_path": setup.ConfigPath, "master_key_path": setup.MasterKeyPath, "restart_required": true})
	}

	writef(
		c.Writer,
		"wake: local setup complete; verified master key at %s\nwake: configuration: %s\nwake: start or restart wake serve with this configuration, then sign in with your operator unlock\n",
		setup.MasterKeyPath,
		setup.ConfigPath,
	)

	return nil
}

func checkSetupStopped(guides ...config.SetupGuidance) error {
	checked := make(map[config.Paths]bool)
	for _, guide := range guides {
		configured := config.Paths{DataDir: guide.DataDir, StateDir: guide.StateDir, RuntimeDir: guide.RuntimeDir}
		launch := configured
		for _, setting := range config.Settings() {
			if setting.Flag != "data-dir" && setting.Flag != "state-dir" && setting.Flag != "runtime-dir" {
				continue
			}

			value, found := os.LookupEnv(setting.Environment)
			if !found {
				continue
			}

			if !filepath.IsAbs(value) {
				return failure("cannot check running Wake: %s must be an absolute path", setting.Environment)
			}

			switch setting.Flag {
			case "data-dir":
				launch.DataDir = value
			case "state-dir":
				launch.StateDir = value
			default:
				launch.RuntimeDir = value
			}
		}

		for _, paths := range []config.Paths{configured, launch} {
			if checked[paths] {
				continue
			}

			if err := daemon.CheckStopped(paths); err != nil {
				if errors.Is(err, daemon.ErrAlreadyOwned) {
					return failure(
						"Wake is running or its paths are in use (data: %s; runtime: %s); stop it and rerun setup",
						paths.DataDir,
						paths.RuntimeDir,
					)
				}

				return failure(
					"cannot check running Wake at data %s and runtime %s: %v",
					paths.DataDir,
					paths.RuntimeDir,
					err,
				)
			}

			checked[paths] = true
		}
	}

	return nil
}

func promptSetting(ctx context.Context, c *cli.Command, flag, defaultValue string) (string, error) {
	label := flag
	for _, s := range config.Settings() {
		if s.Flag == flag {
			label = s.Description
			break
		}
	}

	value, err := promptLine(ctx, c, label+" ["+defaultValue+"]: ", false)
	if value == "" {
		value = defaultValue
	}

	return value, err
}

func promptLine(ctx context.Context, c *cli.Command, prompt string, secret bool) (string, error) {
	value, err := readTerminalInput(ctx, c, prompt, secret)
	defer clear(value)

	return string(value), err
}

func readTerminalInput(ctx context.Context, c *cli.Command, prompt string, secret bool) ([]byte, error) {
	if c.Bool("json") {
		_ = json.NewEncoder(c.ErrWriter).
			Encode(map[string]any{"event": "input_required", "prompt": prompt, "secret": secret})
	} else {
		writeString(c.ErrWriter, prompt)
	}

	fd := int(os.Stdin.Fd())

	state, err := term.GetState(fd)
	if err != nil {
		return nil, err
	}

	if secret {
		attrs, err := unix.IoctlGetTermios(fd, unix.TCGETS)
		if err != nil {
			return nil, err
		}

		attrs.Lflag &^= unix.ECHO
		if err := unix.IoctlSetTermios(fd, unix.TCSETS, attrs); err != nil {
			return nil, err
		}
	}

	defer func() { _ = term.Restore(fd, state) }()

	if fd < 0 || fd > math.MaxInt32 {
		return nil, errTerminalRequired
	}

	polls := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}

	content := make([]byte, 0, 128)
	defer func() { clear(content) }()

	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		n, err := unix.Poll(polls, 100)
		if errors.Is(err, unix.EINTR) {
			continue
		}

		if err != nil {
			return nil, err
		}

		if n == 0 {
			continue
		}

		var buffer [4096]byte

		n, err = unix.Read(fd, buffer[:])
		if errors.Is(err, unix.EINTR) {
			continue
		}

		if err != nil {
			return nil, err
		}

		if n == 0 {
			return nil, io.EOF
		}

		content = append(content, buffer[:n]...)
		clear(buffer[:])

		if len(content) > 4096 {
			return nil, fmt.Errorf("input too long")
		}

		if bytes.IndexByte(content, '\n') >= 0 {
			if secret && !c.Bool("json") {
				writeString(c.ErrWriter, "\n")
			}

			return bytes.Clone(bytes.TrimSuffix(bytes.TrimSuffix(content, []byte("\n")), []byte("\r"))), nil
		}
	}
}

func checkSetupKey(setup config.LocalSetup) error {
	key, err := credential.LoadMasterKey(setup.MasterKeyPath)
	if err == nil {
		key.Clear()
		return nil
	}
	// Only the unconfigured, user-scoped default may be created. Never replace
	// a missing configured key: it may protect existing provider credentials.
	if !setup.KeyConfigured && errors.Is(err, os.ErrNotExist) {
		return nil
	}

	return err
}

func finishLocalSetup(setup config.LocalSetup, unlock []byte) error {
	if !setup.KeyConfigured {
		err := credential.CreateMasterKey(setup.MasterKeyPath)
		if err != nil && !errors.Is(err, credential.ErrMasterKeyExists) {
			return err
		}
	}
	// Verify again after the prompt, including a key retained by an earlier
	// interrupted setup. Never delete or rotate key material on a later error.
	key, err := credential.LoadMasterKey(setup.MasterKeyPath)
	if err != nil {
		return err
	}

	key.Clear()

	if err := setup.Save(); err != nil {
		return err
	}

	return admin.SetUnlock(setup.ConfigDir, unlock)
}

func readOperatorUnlock(ctx context.Context, c *cli.Command) ([]byte, error) {
	first, err := readTerminalInput(ctx, c, "Operator unlock: ", true)
	if err != nil {
		return nil, err
	}

	second, err := readTerminalInput(ctx, c, "Confirm operator unlock: ", true)
	if err != nil {
		clear(first)
		clear(second)

		return nil, err
	}

	if subtle.ConstantTimeCompare(first, second) != 1 {
		clear(first)
		clear(second)

		return nil, errUnlockMismatch
	}

	clear(second)

	if len(first) < admin.MinimumUnlockBytes {
		clear(first)
		return nil, admin.ErrWeakUnlock
	}

	return first, nil
}
