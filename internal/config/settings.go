// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package config

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/urfave/cli/v3"
	"go.trippwill.dev/wake/provider"
)

var defaultAdmin = Admin{Listen: "127.0.0.1:8787"}

// Setting describes an admitted non-secret process setting.
type Setting struct {
	Key         string `json:"toml_key"`
	Type        string `json:"type"`
	Default     string `json:"default"`
	Description string `json:"description"`
}

// Settings returns fresh metadata; changing it does not change the registry.
func Settings() []Setting {
	settings := []Setting{
		{
			Key:         "storage.warehouse_path",
			Type:        "string",
			Default:     "wake.db",
			Description: "Path to the financial warehouse database",
		},
		{
			Key:         "storage.connections_path",
			Type:        "string",
			Default:     "connections.db",
			Description: "Path to the source connection and encrypted credential database",
		},
		{
			Key:  "storage.unattended_upgrade",
			Type: "bool", Default: "false",
			Description: "Approve supported upgrades that rebuild retained history during startup",
		},
		{
			Key:         "paths.runtime_dir",
			Type:        "string",
			Default:     "$XDG_RUNTIME_DIR/wake/<instance-directory hash>",
			Description: "Path to transient runtime directory",
		},
		{
			Key:         "log.level",
			Type:        "string",
			Default:     "info",
			Description: "Diagnostic log level: debug, info, warn, error",
		},
		{
			Key:         "credentials.key_path",
			Type:        "string",
			Default:     "credentials.key",
			Description: "Path to the private 32-byte credential encryption key",
		},
		{
			Key:         "admin.listen",
			Type:        "string",
			Default:     defaultAdmin.Listen,
			Description: "Browser HTTP listener IP address and port",
		},
		{
			Key:         "api.listen",
			Type:        "string",
			Default:     "127.0.0.1:8788",
			Description: "Token-authenticated financial HTTP listener IP address and port",
		},
		{
			Key:         "admin.public_origin",
			Type:        "string",
			Description: "Browser-visible HTTP or HTTPS origin; defaults to the loopback listener",
		},
		{
			Key:         "admin.currency_refresh",
			Type:        "bool",
			Default:     strconv.FormatBool(defaultAdmin.CurrencyRefresh),
			Description: "Refresh admin currency choices daily from SIX",
		},
	}

	return settings
}

// PollingDefaults describes the TOML-only table whose keys come from the registry.
var PollingDefaults = Setting{
	Key: "polling.defaults", Type: "table", Default: "registered stream defaults",
	Description: "Recurring interval overrides keyed by provider-id:stream-id",
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

// Resolve reads the selected instance configuration without writes.
func Resolve(command *cli.Command) (Resolution, error) {
	return resolve(command, true)
}

func ResolveReadOnly(command *cli.Command) (Resolution, error) {
	return resolve(command, false)
}

func resolve(command *cli.Command, runtimeRequired bool) (Resolution, error) {
	path, source, err := SelectConfigPath(command.String("config"), command.IsSet("config"))
	if err != nil {
		return Resolution{}, err
	}

	dir, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return Resolution{}, fmt.Errorf("%w: read config %s: %v", ErrInvalid, path, err)
	}

	path = filepath.Join(dir, filepath.Base(path))

	content, err := readConfigSnapshot(path)
	if err != nil {
		return Resolution{}, fmt.Errorf("%w: read config %s: %v", ErrInvalid, path, err)
	}

	file, metadata, err := decodeFileConfigSnapshot(content)
	if err != nil {
		return Resolution{}, err
	}

	file.Storage.WarehousePath = instancePath(dir, file.Storage.WarehousePath, "wake.db")
	file.Storage.ConnectionsPath = instancePath(dir, file.Storage.ConnectionsPath, "connections.db")
	file.Credentials.KeyPath = instancePath(dir, file.Credentials.KeyPath, "credentials.key")
	if file.Paths.RuntimeDir != "" {
		file.Paths.RuntimeDir = instancePath(dir, file.Paths.RuntimeDir, "")
	} else if root := os.Getenv("XDG_RUNTIME_DIR"); root != "" {
		if err := requireAbsolute("XDG_RUNTIME_DIR", root); err != nil {
			return Resolution{}, err
		}

		hash := sha256.Sum256([]byte(dir))
		file.Paths.RuntimeDir = filepath.Join(root, "wake", fmt.Sprintf("%x", hash))
	}

	if file.Log.Level == "" {
		file.Log.Level = "info"
	}

	if file.Admin.Listen == "" {
		file.Admin.Listen = defaultAdmin.Listen
	}

	if file.API.Listen == "" {
		file.API.Listen = "127.0.0.1:8788"
	}

	cfg, err := resolvedConfig(file, dir, runtimeRequired)
	if err != nil {
		return Resolution{}, err
	}

	cfg.Paths.ConfigFile = path

	if err := checkInstancePaths(cfg); err != nil {
		return Resolution{}, err
	}

	file.Admin.PublicOrigin = cfg.Admin.PublicOrigin
	if file.Polling.Defaults == nil {
		file.Polling.Defaults = map[string]string{}
	}

	values := fileSettingValues(file)
	result := Resolution{Config: cfg, ConfigPath: path, ConfigSource: source}
	for _, setting := range append(Settings(), PollingDefaults) {
		authority := "default"
		if metadata.IsDefined(strings.Split(setting.Key, ".")...) {
			authority = "toml:" + path
		}

		result.Values = append(result.Values, Value{setting.Key, values[setting.Key], authority})
	}

	return result, nil
}

func instancePath(dir, path, fallback string) string {
	if path == "" {
		path = fallback
	}

	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}

	return filepath.Join(dir, path)
}

func comparablePath(path string) string {
	if parent, err := filepath.EvalSymlinks(filepath.Dir(path)); err == nil {
		return filepath.Join(parent, filepath.Base(path))
	}

	return path
}

func checkInstancePaths(cfg Config) error {
	protected := []string{
		cfg.Paths.ConfigFile, filepath.Join(cfg.Paths.ConfigDir, "auth.json"), cfg.Credentials.KeyPath,
	}
	if cfg.Paths.RuntimeDir != "" {
		protected = append(protected, filepath.Join(cfg.Paths.RuntimeDir, "wake.lock"))
	}

	for i, path := range protected {
		path = comparablePath(path)
		for _, prior := range protected[:i] {
			if path == comparablePath(prior) {
				return fmt.Errorf("%w: instance files overlap at %s", ErrInvalid, path)
			}
		}

		for _, db := range []string{cfg.Storage.WarehousePath, cfg.Storage.ConnectionsPath} {
			db = comparablePath(db)
			for _, suffix := range []string{"", "-wal", "-shm", "-journal", ".upgrade-intent", ".upgrade-backup.tar"} {
				if path == db+suffix || strings.HasPrefix(path, db+"-mj") {
					return fmt.Errorf("%w: database overlaps instance files at %s", ErrInvalid, path)
				}
			}
		}
	}

	return nil
}

func fileSettingValues(file fileConfig) map[string]any {
	var currencyRefresh bool
	if file.Admin.CurrencyRefresh != nil {
		currencyRefresh = *file.Admin.CurrencyRefresh
	}

	return map[string]any{
		"storage.warehouse_path":     file.Storage.WarehousePath,
		"storage.connections_path":   file.Storage.ConnectionsPath,
		"storage.unattended_upgrade": file.Storage.UnattendedUpgrade,
		"paths.runtime_dir":          file.Paths.RuntimeDir,
		"log.level":                  file.Log.Level,
		"credentials.key_path":       file.Credentials.KeyPath,
		"admin.listen":               file.Admin.Listen,
		"api.listen":                 file.API.Listen,
		"admin.public_origin":        file.Admin.PublicOrigin,
		"admin.currency_refresh":     currencyRefresh,
		"polling.defaults":           file.Polling.Defaults,
	}
}

// Validate explicit TOML values before supplying defaults.
func checkFileSetting(setting Setting, value any) error {
	text := fmt.Sprint(value)
	switch setting.Key {
	case "storage.warehouse_path", "storage.connections_path":
		if text == "" {
			return fmt.Errorf("%w: %s requires a file path", ErrInvalid, setting.Key)
		}
	case "paths.runtime_dir":
		if text == "" {
			return fmt.Errorf("%w: %s requires a directory", ErrInvalid, setting.Key)
		}
	case "credentials.key_path":
		if text == "" {
			return fmt.Errorf("%w: %s requires a file path", ErrInvalid, setting.Key)
		}
	case "log.level":
		_, err := parseLogLevel(text)
		return err
	case "admin.listen", "api.listen":
		return checkListen(text)
	case "admin.public_origin":
		if text != "" {
			return validateAdminOrigin(text)
		}
	}

	return nil
}

func resolvedConfig(file fileConfig, dir string, runtimeRequired bool) (Config, error) {
	if key := file.Credentials.KeyPath; key != "" {
		if err := requireAbsolute("credential key path", key); err != nil {
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

	if err := checkListen(file.Admin.Listen); err != nil {
		return Config{}, err
	}

	admin, err := resolveAdmin(file.Admin)
	if err != nil {
		return Config{}, err
	}

	if err := checkListen(file.API.Listen); err != nil {
		return Config{}, err
	}

	defaults := make(map[string]time.Duration, len(file.Polling.Defaults))
	for key, value := range file.Polling.Defaults {
		interval, err := provider.ParsePollInterval(value)
		if err != nil {
			return Config{}, fmt.Errorf("%w: invalid polling interval for %q", ErrInvalid, key)
		}

		defaults[key] = interval
	}

	return Config{
		PollDefaults: defaults,
		Paths:        Paths{ConfigDir: dir, RuntimeDir: file.Paths.RuntimeDir},
		Storage:      file.Storage,
		Log:          Log{Level: level},
		Credentials:  Credentials{KeyPath: file.Credentials.KeyPath},
		Admin:        admin,
		API:          file.API,
	}, nil
}

func readConfigSnapshot(path string) ([]byte, error) {
	file, err := os.OpenFile(
		path,
		os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW,
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
