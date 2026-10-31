// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package config

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/BurntSushi/toml"
	altsrc "github.com/urfave/cli-altsrc/v3"
	"github.com/urfave/cli/v3"
)

var defaultAdmin = Admin{Port: 8787, AuthMode: "local"}

// Setting describes an admitted non-secret process setting.
type Setting struct {
	Key         string `json:"toml_key"`
	Flag        string `json:"flag"`
	Environment string `json:"environment"`
	Type        string `json:"type"`
	Default     string `json:"default"`
	Description string `json:"description"`
}

// Settings returns fresh metadata; changing it does not change the registry.
func Settings() []Setting {
	settings := []Setting{
		{
			Key:         "storage.warehouse_path",
			Flag:        "warehouse-path",
			Type:        "string",
			Default:     "${XDG_DATA_HOME:-$HOME/.local/share}/wake/wake.db",
			Description: "Absolute path to the financial warehouse database",
		},
		{
			Key:         "storage.connections_path",
			Flag:        "connections-path",
			Type:        "string",
			Default:     "${XDG_CONFIG_HOME:-$HOME/.config}/wake/connections.db",
			Description: "Absolute path to the active connection and encrypted credential database",
		},
		{
			Key:         "paths.runtime_dir",
			Flag:        "runtime-dir",
			Type:        "string",
			Default:     "$XDG_RUNTIME_DIR/wake (required when unset)",
			Description: "Absolute path to transient runtime files",
		},
		{
			Key:         "log.level",
			Flag:        "log-level",
			Type:        "string",
			Default:     "info",
			Description: "Diagnostic log level: debug, info, warn, error",
		},
		{
			Key:         "credentials.master_key_path",
			Flag:        "master-key-path",
			Type:        "string",
			Description: "Absolute path to an existing private 32-byte master key",
		},
		{
			Key:         "admin.port",
			Flag:        "admin-port",
			Type:        "uint16",
			Default:     strconv.Itoa(int(defaultAdmin.Port)),
			Description: "Localhost browser administration port",
		},
		{
			Key:         "admin.auth_mode",
			Flag:        "admin-auth-mode",
			Type:        "string",
			Default:     defaultAdmin.AuthMode,
			Description: "Browser authentication mode: local or proxy",
		},
		{
			Key:         "admin.public_origin",
			Flag:        "admin-public-origin",
			Type:        "string",
			Description: "HTTPS browser origin in proxy mode",
		},
	}
	for i := range settings {
		settings[i].Environment = "WAKE_" + strings.ToUpper(strings.ReplaceAll(settings[i].Flag, "-", "_"))
	}

	return settings
}

// Flags supplies fresh flags for parsing explicit inputs without reading files or environments.
func Flags() []cli.Flag {
	var flags []cli.Flag
	for _, setting := range Settings() {
		flags = append(flags, setting.flag(setting.Default, cli.ValueSourceChain{}))
	}

	return flags
}

func (s Setting) flag(value string, sources cli.ValueSourceChain) cli.Flag {
	usage := s.Description + " (TOML: " + s.Key + "; env: " + s.Environment + ")"
	switch s.Type {
	case "bool":
		parsed, _ := strconv.ParseBool(value)
		return &cli.BoolFlag{Name: s.Flag, Usage: usage, Value: parsed, Sources: sources}
	case "uint16":
		parsed, _ := strconv.ParseUint(value, 10, 16)
		return &cli.Uint16Flag{Name: s.Flag, Usage: usage, Value: uint16(parsed), Sources: sources}
	case "int64":
		parsed, _ := strconv.ParseInt(value, 10, 64)
		return &cli.Int64Flag{Name: s.Flag, Usage: usage, Value: parsed, Sources: sources}
	default:
		return &cli.StringFlag{Name: s.Flag, Usage: usage, Value: value, Sources: sources}
	}
}

// Value describes an effective launch value and the authority that supplied it.
type Value struct {
	Key    string `json:"toml_key"`
	Value  any    `json:"value"`
	Source string `json:"source"`
}

// Resolution is local launch configuration, not the running daemon's settings.
type Resolution struct {
	Config       Config  `json:"-"`
	ConfigPath   string  `json:"config_path"`
	ConfigSource string  `json:"config_source"`
	Values       []Value `json:"settings"`
}

// SelectConfigPath selects the bootstrap file without preparing directories.
func SelectConfigPath(path string, explicit bool) (string, string, error) {
	source := "default"
	if explicit {
		source = "flag"
	} else if env, found := os.LookupEnv("WAKE_CONFIG"); found {
		path, source = env, "environment:WAKE_CONFIG"
	} else {
		dir, err := ResolveConfigDir()
		if err != nil {
			return "", "", err
		}

		path = filepath.Join(dir, "config.toml")
	}

	if err := requireAbsolute("config path", path); err != nil {
		return "", "", err
	}

	return path, source, nil
}

// Resolve performs strict flag > environment > TOML > default resolution without writes.
func Resolve(overrides Overrides) (Resolution, error) {
	return resolve(overrides, true)
}

// ResolveReadOnly resolves inspection paths without requiring a daemon runtime directory.
func ResolveReadOnly(overrides Overrides) (Resolution, error) {
	return resolve(overrides, false)
}

func resolve(overrides Overrides, runtimeRequired bool) (Resolution, error) {
	var out Resolution

	path, source, err := SelectConfigPath(overrides.ConfigPath, overrides.ConfigPathExplicit)
	if err != nil {
		return out, err
	}

	out.ConfigPath, out.ConfigSource = path, source
	// Read and validate once before altsrc, whose lookup intentionally treats read errors as absent.
	content, err := readConfigSnapshot(path)
	exists := err == nil
	if err != nil && (source != "default" || !errors.Is(err, os.ErrNotExist)) {
		return out, fmt.Errorf("%w: read config file: %v", ErrInvalid, err)
	}

	if exists {
		if _, err := decodeFileConfig(content); err != nil {
			return out, err
		}
	}

	tree := map[string]any{}

	metadata, err := toml.Decode(string(content), &tree)
	if err != nil {
		return out, fmt.Errorf("%w: malformed TOML", ErrInvalid)
	}

	defaults, dir, err := settingDefaults()
	if err != nil {
		return out, err
	}

	var sourceErr error

	flags := []cli.Flag{}
	for _, setting := range Settings() {
		if value, explicit := overrides.Values[setting.Flag]; explicit {
			if err := checkSettingInput(setting, value); err != nil {
				return out, err
			}
		} else if value, present := os.LookupEnv(setting.Environment); present {
			if err := checkSettingInput(setting, value); err != nil {
				return out, fmt.Errorf("%w: %s", err, setting.Environment)
			}
		}

		chain := cli.EnvVars(setting.Environment)
		if exists {
			valueSource := altsrc.NewValueSource(func(current []byte, target any) error {
				if !bytes.Equal(current, content) {
					sourceErr = fmt.Errorf("%w: configuration changed during resolution", ErrInvalid)
					return sourceErr
				}

				return toml.Unmarshal(current, target)
			}, "toml", setting.Key, altsrc.StringSourcer(path))
			chain.Append(
				cli.NewValueSourceChain(
					&checkedSource{
						ValueSource: valueSource,
						defined:     metadata.IsDefined(strings.Split(setting.Key, ".")...),
						err:         &sourceErr,
					},
				),
			)
		}

		flags = append(flags, setting.flag(defaults[setting.Flag], chain))
	}

	effective := map[string]any{}
	command := &cli.Command{
		Name:            "settings",
		Flags:           flags,
		HideHelp:        true,
		HideHelpCommand: true,
		Writer:          io.Discard,
		ErrWriter:       io.Discard,
		Action: func(_ context.Context, c *cli.Command) error {
			for _, setting := range Settings() {
				value := c.Value(setting.Flag)

				parts := strings.Split(setting.Key, ".")
				table, ok := effective[parts[0]].(map[string]any)
				if !ok {
					table = map[string]any{}
					effective[parts[0]] = table
				}

				table[parts[1]] = value

				authority := "default"
				if strings.HasPrefix(setting.Key, "paths.") || strings.HasPrefix(setting.Key, "storage.") {
					authority = "XDG/default"
				}

				if metadata.IsDefined(parts...) {
					authority = "toml:" + path
				}

				if _, ok := os.LookupEnv(setting.Environment); ok {
					authority = "environment:" + setting.Environment
				}

				if _, ok := overrides.Values[setting.Flag]; ok {
					authority = "flag"
				}

				out.Values = append(out.Values, Value{setting.Key, value, authority})
			}

			return nil
		},
	}

	args := []string{"settings"}
	for _, setting := range Settings() {
		if value, ok := overrides.Values[setting.Flag]; ok {
			args = append(args, "--"+setting.Flag+"="+value)
		}
	}

	for name := range overrides.Values {
		found := false
		for _, setting := range Settings() {
			if setting.Flag == name {
				found = true
			}
		}

		if !found {
			return out, fmt.Errorf("%w: unknown setting %s", ErrInvalid, name)
		}
	}

	if err := command.Run(context.Background(), args); err != nil {
		return out, fmt.Errorf("%w: %v", ErrInvalid, err)
	}

	if sourceErr != nil {
		return out, sourceErr
	}

	if exists {
		current, err := readConfigSnapshot(path)
		if err != nil || !bytes.Equal(current, content) {
			return out, fmt.Errorf("%w: configuration changed or became unreadable during resolution", ErrInvalid)
		}
	}

	var encoded bytes.Buffer
	if err := toml.NewEncoder(&encoded).Encode(effective); err != nil {
		return out, err
	}

	file, err := decodeFileConfig(encoded.Bytes())
	if err != nil {
		return out, err
	}

	out.Config, err = resolvedConfig(file, dir, runtimeRequired)

	return out, err
}

func checkSettingInput(setting Setting, value string) error {
	var err error
	switch setting.Type {
	case "bool":
		_, err = strconv.ParseBool(value)
	case "uint16":
		_, err = strconv.ParseUint(value, 10, 16)
	case "int64":
		_, err = strconv.ParseInt(value, 10, 64)
	}

	if err != nil {
		return fmt.Errorf("%w: --%s requires %s", ErrInvalid, setting.Flag, setting.Type)
	}

	return nil
}

type checkedSource struct {
	cli.ValueSource
	defined bool
	err     *error
}

func (s *checkedSource) Lookup() (string, bool) {
	value, found := s.ValueSource.Lookup()
	if s.defined && !found {
		*s.err = fmt.Errorf("%w: cannot resolve TOML value", ErrInvalid)
	}

	return value, found
}

func settingDefaults() (map[string]string, string, error) {
	dir, err := ResolveConfigDir()
	if err != nil {
		return nil, "", err
	}

	data, err := xdgRoot("XDG_DATA_HOME", filepath.Join(".local", "share"))
	if err != nil {
		return nil, "", err
	}

	defaults := map[string]string{}
	for _, setting := range Settings() {
		defaults[setting.Flag] = setting.Default
	}

	defaults["warehouse-path"] = filepath.Join(data, "wake", "wake.db")

	defaults["connections-path"] = filepath.Join(dir, "connections.db")
	defaults["runtime-dir"] = ""
	if runtime := os.Getenv("XDG_RUNTIME_DIR"); runtime != "" {
		if err := requireAbsolute("XDG_RUNTIME_DIR", runtime); err != nil {
			return nil, "", err
		}

		defaults["runtime-dir"] = filepath.Join(runtime, "wake")
	}

	return defaults, dir, nil
}

func resolvedConfig(file fileConfig, dir string, runtimeRequired bool) (Config, error) {
	if err := checkStorage(file.Storage); err != nil {
		return Config{}, err
	}

	if file.Paths.RuntimeDir == "" && runtimeRequired {
		return Config{}, ErrRuntimeDirRequired
	}

	if file.Paths.RuntimeDir != "" {
		if err := requireAbsolute("runtime directory", file.Paths.RuntimeDir); err != nil {
			return Config{}, err
		}
	}

	level, err := parseLogLevel(file.Log.Level)
	if err != nil {
		return Config{}, err
	}

	admin, err := resolveAdmin(file.Admin)
	if err != nil {
		return Config{}, err
	}

	if file.Admin.AuthMode == "" {
		return Config{}, fmt.Errorf("%w: admin auth mode is empty", ErrInvalid)
	}

	return Config{
		Paths:       Paths{ConfigDir: dir, RuntimeDir: file.Paths.RuntimeDir},
		Storage:     file.Storage,
		Log:         Log{Level: level},
		Credentials: Credentials{MasterKeyPath: file.Credentials.MasterKeyPath},
		Admin:       admin,
	}, nil
}

func readConfigSnapshot(path string) ([]byte, error) {
	file, err := os.OpenFile(
		path,
		os.O_RDONLY|syscall.O_NONBLOCK,
		0,
	) // #nosec G304,G703 -- validated absolute configuration path; nonblocking open rejects pipes/devices.
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()

	info, err := file.Stat()
	if err != nil {
		return nil, err
	}

	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: config must be a regular file", ErrInvalid)
	}

	return io.ReadAll(file)
}

func checkStorage(storage Storage) error {
	paths := []string{storage.WarehousePath, storage.ConnectionsPath}
	for i, path := range paths {
		if err := requireAbsolute("database file", path); err != nil {
			return err
		}

		if filepath.Clean(path) != path {
			return fmt.Errorf("%w: database paths must be clean", ErrInvalid)
		}

		for _, other := range paths[:i] {
			for _, suffix := range []string{"", "-journal", "-wal", "-shm"} {
				if path == other+suffix || other == path+suffix {
					return fmt.Errorf("%w: database paths overlap another store or its sidecars", ErrInvalid)
				}
			}

			if strings.HasPrefix(path, other+"-mj") || strings.HasPrefix(other, path+"-mj") {
				return fmt.Errorf("%w: database paths overlap a journal namespace", ErrInvalid)
			}
		}
	}

	return nil
}
