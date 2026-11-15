// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

// Package config loads and validates Wake's process configuration.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/urfave/cli/v3"
	"go.trippwill.dev/wake/provider"
)

const directoryMode = 0o700

// Error is a classifiable configuration error.
type Error string

func (e Error) Error() string { return string(e) }

const (
	// ErrInvalid is returned for invalid configuration input.
	ErrInvalid Error = "invalid configuration"
	// ErrRuntimeDirRequired is returned when no safe runtime root is available.
	ErrRuntimeDirRequired Error = "runtime directory required"
	// ErrInsecurePath is returned when a managed directory is not private to the
	// process user.
	ErrInsecurePath Error = "insecure path"
)

// Paths contains the resolved paths used by the Wake process.
type Paths struct {
	ConfigDir, RuntimeDir string
	// ConfigFile is the path of the TOML snapshot loaded at startup, or empty when absent.
	ConfigFile string
}

// Storage contains independently resolved database file locations.
type Storage struct {
	WarehousePath     string `toml:"warehouse_path"`
	ConnectionsPath   string `toml:"connections_path"`
	SyncPath          string `toml:"sync_path"`
	UnattendedUpgrade bool   `toml:"unattended_upgrade"`
}

// Log contains structured logging configuration.
type Log struct {
	Level slog.Level
}

// Credentials contains non-secret credential-protection configuration. The
// referenced key is read and validated by the composition layer.
type Credentials struct {
	KeyPath string
}

// Admin contains browser listener and origin settings. Secrets remain in private files.
type Admin struct {
	Port            uint16
	CurrencyRefresh bool
}

func loopbackAddress(port uint16) string {
	return net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port)))
}

func (value Admin) Address() string { return loopbackAddress(value.Port) }

func (value Admin) Origin() string {
	if value.Port == 80 {
		return "http://127.0.0.1"
	}

	return "http://" + value.Address()
}

// API contains the financial HTTP listener address.
type API struct {
	Port uint16 `toml:"port"`
}

func (value API) Address() string { return loopbackAddress(value.Port) }

// Config is the fully resolved and validated process configuration.
type Config struct {
	Paths        Paths
	Storage      Storage
	Log          Log
	Credentials  Credentials
	Admin        Admin
	API          API
	PollDefaults map[string]time.Duration
}

type fileConfig struct {
	Paths       filePaths       `toml:"paths"`
	Storage     Storage         `toml:"storage"`
	Log         fileLog         `toml:"log"`
	Credentials fileCredentials `toml:"credentials"`
	Admin       fileAdmin       `toml:"admin"`
	API         API             `toml:"api"`
	Polling     filePolling     `toml:"polling"`
}

type filePolling struct {
	Defaults map[string]string `toml:"defaults"`
}

type filePaths struct {
	RuntimeDir string `toml:"runtime_dir"`
}

type fileLog struct {
	Level string `toml:"level"`
}

type fileCredentials struct {
	KeyPath string `toml:"key_path"`
}

type fileAdmin struct {
	Port            uint16 `toml:"port"`
	CurrencyRefresh *bool  `toml:"currency_refresh"`
}

// Load resolves configuration and prepares the daemon's private directories.
func Load(command *cli.Command) (Config, error) {
	resolved, err := Resolve(command)
	if err != nil {
		return Config{}, err
	}

	if err := Prepare(resolved.Config); err != nil {
		return Config{}, err
	}

	return resolved.Config, nil
}

// Prepare creates and checks directories only for an operation that needs them.
func Prepare(cfg Config) error {
	for _, path := range []string{cfg.Paths.ConfigDir, filepath.Dir(cfg.Storage.WarehousePath), cfg.Paths.RuntimeDir} {
		if err := ensurePrivateDir(path); err != nil {
			return err
		}
	}

	return nil
}

func PrepareInstallationRestore(cfg Config) error {
	if err := Prepare(cfg); err != nil {
		return err
	}

	for _, path := range []string{cfg.Storage.ConnectionsPath, cfg.Storage.SyncPath} {
		if err := ensurePrivateDir(filepath.Dir(path)); err != nil {
			return err
		}
	}

	return nil
}

// ResolveConfigDir resolves the user-scoped path without creating it.
func ResolveConfigDir() (string, error) {
	root, err := xdgRoot("XDG_CONFIG_HOME", ".config")
	if err != nil {
		return "", err
	}

	return filepath.Join(root, "wake"), nil
}

func decodeFileConfigSnapshot(contents []byte) (fileConfig, toml.MetaData, error) {
	var cfg fileConfig

	metadata, err := toml.Decode(string(contents), &cfg)
	if err != nil {
		return cfg, metadata, fmt.Errorf("%w: read config file: %v", ErrInvalid, err)
	}

	// Struct decoding is case-insensitive; source admission and attribution are not.
	// Admit the catalog's exact table/key paths before checking or selecting values.
	admitted := map[string]bool{"polling": true, "polling.defaults": true}
	for _, setting := range Settings() {
		parts := strings.Split(setting.Key, ".")
		for n := 1; n <= len(parts); n++ {
			admitted[toml.Key(parts[:n]).String()] = true
		}
	}

	for _, key := range metadata.Keys() {
		if len(key) == 3 && key[0] == "polling" && key[1] == "defaults" {
			if _, err := provider.ParsePollInterval(cfg.Polling.Defaults[key[2]]); err != nil {
				return cfg, metadata, fmt.Errorf("%w: invalid interval for %s", ErrInvalid, key.String())
			}

			continue
		}

		if !admitted[key.String()] {
			return cfg, metadata, fmt.Errorf("%w: unknown or noncanonical TOML key: %s", ErrInvalid, key.String())
		}
	}

	if undecoded := metadata.Undecoded(); len(undecoded) != 0 {
		keys := make([]string, 0, len(undecoded))
		for _, key := range undecoded {
			keys = append(keys, key.String())
		}

		return cfg, metadata, fmt.Errorf(
			"%w: unknown TOML keys: %s",
			ErrInvalid,
			strings.Join(keys, ", "),
		)
	}

	values := fileSettingValues(cfg)
	for _, setting := range Settings() {
		if metadata.IsDefined(strings.Split(setting.Key, ".")...) {
			if err := checkFileSetting(setting, values[setting.Key]); err != nil {
				return cfg, metadata, err
			}
		}
	}

	return cfg, metadata, nil
}

func xdgRoot(environment, fallback string) (string, error) {
	if value := os.Getenv(environment); value != "" {
		if !filepath.IsAbs(value) {
			return "", fmt.Errorf("%w: %s must be absolute", ErrInvalid, environment)
		}

		return value, nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("%w: resolve home directory: %v", ErrInvalid, err)
	}

	return filepath.Join(home, fallback), nil
}

func requireAbsolute(name, path string) error {
	if path == "" || !filepath.IsAbs(path) {
		return fmt.Errorf("%w: %s must be absolute", ErrInvalid, name)
	}

	return nil
}

func parseLogLevel(value string) (slog.Level, error) {
	switch strings.ToLower(value) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("%w: unsupported log level %q", ErrInvalid, value)
	}
}

func ensurePrivateDir(path string) error { return checkPrivateDir(path, true) }

// CheckPrivateDir verifies an existing private directory without creating it.
func CheckPrivateDir(path string) error {
	if err := requireAbsolute("private directory", path); err != nil {
		return err
	}

	return checkPrivateDir(path, false)
}

func checkPrivateDir(path string, create bool) error {
	info, err := os.Lstat(
		path,
	) // #nosec G703 -- Load validates caller-selected XDG and config paths as absolute.
	if errors.Is(err, os.ErrNotExist) && create {
		// #nosec G703 -- create a private directory at a validated caller-selected path.
		if err := os.MkdirAll(path, directoryMode); err != nil {
			return err
		}

		info, err = os.Lstat(path) // #nosec G703 -- path is the validated directory created above.
	}

	if err != nil {
		return err
	}

	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("%w: %s is not a directory", ErrInsecurePath, path)
	}

	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf(
			"%w: %s must not be accessible by group or other users",
			ErrInsecurePath,
			path,
		)
	}

	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() {
		return fmt.Errorf(
			"%w: %s is not owned by the effective user",
			ErrInsecurePath,
			path,
		)
	}

	return nil
}

func resolveAdmin(fromFile fileAdmin) Admin {
	admin := Admin{Port: fromFile.Port}
	if fromFile.CurrencyRefresh != nil {
		admin.CurrencyRefresh = *fromFile.CurrencyRefresh
	}

	return admin
}
