// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

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
	case "uint16":
		parsed, _ := strconv.ParseUint(value, 10, 16)
		return &cli.Uint16Flag{Name: s.Flag, Usage: usage, Value: uint16(parsed), Sources: sources}
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

// Resolve resolves launch settings on the actual parsed command without writes.
// Explicit flags win over environment, a strictly validated TOML snapshot and defaults.
func Resolve(command *cli.Command) (Resolution, error) {
	return resolve(command, true)
}

// ResolveReadOnly resolves inspection paths without requiring a daemon runtime directory.
func ResolveReadOnly(command *cli.Command) (Resolution, error) {
	return resolve(command, false)
}

func resolve(command *cli.Command, runtimeRequired bool) (Resolution, error) {
	var out Resolution

	path, source, err := SelectConfigPath(command.String("config"), command.IsSet("config"))
	if err != nil {
		return out, err
	}

	out.ConfigPath, out.ConfigSource = path, source
	content, err := readConfigSnapshot(path)
	if err != nil && (source != "default" || !errors.Is(err, os.ErrNotExist)) {
		return out, fmt.Errorf("%w: read config file: %v", ErrInvalid, err)
	}

	file, metadata, err := decodeFileConfigSnapshot(content)
	if err != nil {
		return out, err
	}

	defaults, dir, err := settingDefaults()
	if err != nil {
		return out, err
	}

	fileValues := fileSettingValues(file)
	selected := map[any]any{}
	fallback := map[any]any{}
	for _, setting := range Settings() {
		if metadata.IsDefined(strings.Split(setting.Key, ".")...) {
			value := fileValues[setting.Flag]

			selected[setting.Flag] = value
		}

		fallback[setting.Flag] = defaults[setting.Flag]
	}

	tomlSource := cli.NewMapSource("validated TOML: "+path, selected)
	defaultSource := cli.NewMapSource("defaults", fallback)
	for _, setting := range Settings() {
		explicit := command.IsSet(setting.Flag)
		authority := "default"
		if strings.HasPrefix(setting.Key, "paths.") || strings.HasPrefix(setting.Key, "storage.") {
			authority = "XDG/default"
		}

		if _, found := selected[setting.Flag]; found {
			authority = "toml:" + path
		}

		if value, found := os.LookupEnv(setting.Environment); found && !explicit {
			// cli skips empty numeric sources. Reject an explicitly empty number instead.
			if setting.Type == "uint16" && value == "" {
				return out, fmt.Errorf("%w: %s requires uint16", ErrInvalid, setting.Environment)
			}

			authority = "environment:" + setting.Environment
		}

		if explicit {
			authority = "flag"
		}

		sources := cli.EnvVars(setting.Environment)
		sources.Append(
			cli.NewValueSourceChain(
				cli.NewMapValueSource(setting.Flag, tomlSource),
				cli.NewMapValueSource(setting.Flag, defaultSource),
			),
		)

		var flag cli.Flag
		for _, candidate := range command.Flags {
			if candidate.Names()[0] == setting.Flag {
				flag = candidate
				break
			}
		}

		switch value := flag.(type) {
		case *cli.StringFlag:
			value.Sources = sources
		case *cli.Uint16Flag:
			value.Sources = sources
		default:
			return out, fmt.Errorf("%w: missing native setting flag %s", ErrInvalid, setting.Flag)
		}

		if err := flag.PostParse(); err != nil {
			return out, fmt.Errorf("%w: %v", ErrInvalid, err)
		}

		out.Values = append(out.Values, Value{setting.Key, command.Value(setting.Flag), authority})
	}

	port := command.Uint16("admin-port")
	file = fileConfig{
		Storage: Storage{
			WarehousePath:   command.String("warehouse-path"),
			ConnectionsPath: command.String("connections-path"),
		},
		Paths:       filePaths{RuntimeDir: command.String("runtime-dir")},
		Log:         fileLog{Level: command.String("log-level")},
		Credentials: fileCredentials{MasterKeyPath: command.String("master-key-path")},
		Admin: fileAdmin{
			Port:         &port,
			AuthMode:     command.String("admin-auth-mode"),
			PublicOrigin: command.String("admin-public-origin"),
		},
	}
	out.Config, err = resolvedConfig(file, dir, runtimeRequired)

	return out, err
}

func fileSettingValues(file fileConfig) map[string]any {
	var port uint16
	if file.Admin.Port != nil {
		port = *file.Admin.Port
	}

	return map[string]any{
		"warehouse-path": file.Storage.WarehousePath, "connections-path": file.Storage.ConnectionsPath,
		"runtime-dir": file.Paths.RuntimeDir, "log-level": file.Log.Level,
		"master-key-path": file.Credentials.MasterKeyPath, "admin-port": port,
		"admin-auth-mode": file.Admin.AuthMode, "admin-public-origin": file.Admin.PublicOrigin,
	}
}

// Validate selected file values before applying any higher-priority source.
func checkFileSetting(setting Setting, value any) error {
	text := fmt.Sprint(value)
	switch setting.Flag {
	case "warehouse-path", "connections-path":
		return checkDatabasePath(text)
	case "runtime-dir":
		return requireAbsolute(setting.Key, text)
	case "master-key-path":
		if text != "" {
			return requireAbsolute(setting.Key, text)
		}
	case "log-level":
		_, err := parseLogLevel(text)
		return err
	case "admin-port":
		if port, ok := value.(uint16); !ok || port == 0 {
			return fmt.Errorf("%w: admin port must be between 1 and 65535", ErrInvalid)
		}
	case "admin-auth-mode":
		if text != "local" && text != "proxy" {
			return fmt.Errorf("%w: unsupported admin auth mode %q", ErrInvalid, text)
		}
	case "admin-public-origin":
		if text != "" {
			return validateAdminOrigin(text, true)
		}
	}

	return nil
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
	if key := file.Credentials.MasterKeyPath; key != "" {
		if err := requireAbsolute("master key path", key); err != nil {
			return Config{}, err
		}
	}

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

func checkDatabasePath(path string) error {
	if err := requireAbsolute("database file", path); err != nil {
		return err
	}

	if filepath.Clean(path) != path {
		return fmt.Errorf("%w: database paths must be clean", ErrInvalid)
	}

	return nil
}

func checkStorage(storage Storage) error {
	paths := []string{storage.WarehousePath, storage.ConnectionsPath}
	for i, path := range paths {
		if err := checkDatabasePath(path); err != nil {
			return err
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
