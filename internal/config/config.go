// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

// Package config loads and validates Wake's process configuration.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/BurntSushi/toml"
	"github.com/urfave/cli/v3"
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
type Paths struct{ ConfigDir, RuntimeDir string }

// Storage contains independently resolved database file locations.
type Storage struct {
	WarehousePath   string `toml:"warehouse_path"`
	ConnectionsPath string `toml:"connections_path"`
}

// Log contains structured logging configuration.
type Log struct {
	Level slog.Level
}

// Credentials contains non-secret credential-protection configuration. The
// referenced key is read and validated by the composition layer.
type Credentials struct {
	MasterKeyPath string
}

// Admin contains loopback-only browser administration settings. Secrets remain
// in files and are not loaded by this package.
type Admin struct {
	Port         uint16
	AuthMode     string
	PublicOrigin string
}

// Address returns the fixed IPv4 localhost listener address.
func (value Admin) Address() string {
	return net.JoinHostPort("127.0.0.1", strconv.Itoa(int(value.Port)))
}

// Origin returns the browser-visible origin for the administration UI.
func (value Admin) Origin() string {
	if value.PublicOrigin != "" {
		return value.PublicOrigin
	}

	return "http://" + value.Address()
}

// Config is the fully resolved and validated process configuration.
type Config struct {
	Paths       Paths
	Storage     Storage
	Log         Log
	Credentials Credentials
	Admin       Admin
}

type fileConfig struct {
	Paths       filePaths       `toml:"paths"`
	Storage     Storage         `toml:"storage"`
	Log         fileLog         `toml:"log"`
	Credentials fileCredentials `toml:"credentials"`
	Admin       fileAdmin       `toml:"admin"`
}

type filePaths struct {
	RuntimeDir string `toml:"runtime_dir"`
}

type fileLog struct {
	Level string `toml:"level"`
}

type fileCredentials struct {
	MasterKeyPath string `toml:"master_key_path"`
}

type fileAdmin struct {
	Port         *uint16 `toml:"port"`
	AuthMode     string  `toml:"auth_mode"`
	PublicOrigin string  `toml:"public_origin"`
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
	for _, path := range []string{cfg.Paths.ConfigDir, filepath.Dir(cfg.Storage.WarehousePath), filepath.Dir(cfg.Storage.ConnectionsPath), cfg.Paths.RuntimeDir} {
		if err := ensurePrivateDir(path); err != nil {
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

// ConfigDir resolves and prepares the user-scoped private configuration directory.
func ConfigDir() (string, error) {
	dir, err := ResolveConfigDir()
	if err != nil {
		return "", err
	}

	if err := ensurePrivateDir(dir); err != nil {
		return "", err
	}

	return dir, nil
}

func decodeFileConfig(contents []byte) (fileConfig, error) {
	cfg, _, err := decodeFileConfigSnapshot(contents)
	return cfg, err
}

func decodeFileConfigSnapshot(contents []byte) (fileConfig, toml.MetaData, error) {
	var cfg fileConfig

	metadata, err := toml.Decode(string(contents), &cfg)
	if err != nil {
		return cfg, metadata, fmt.Errorf("%w: read config file: %v", ErrInvalid, err)
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
			if err := checkFileSetting(setting, values[setting.Flag]); err != nil {
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

func validateAdminOrigin(value string, requireHTTPS bool) error {
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" ||
		parsed.User != nil || parsed.Opaque != "" || parsed.Path != "" || parsed.RawQuery != "" ||
		parsed.ForceQuery || parsed.Fragment != "" || parsed.Scheme != strings.ToLower(parsed.Scheme) ||
		parsed.Host != strings.ToLower(parsed.Host) {
		return fmt.Errorf("%w: admin public_origin must be an http or https origin", ErrInvalid)
	}

	if port := parsed.Port(); port != "" {
		portNumber, portErr := strconv.Atoi(port)
		if portErr != nil || strconv.Itoa(portNumber) != port {
			return fmt.Errorf("%w: admin public_origin must use a canonical port", ErrInvalid)
		}

		if (parsed.Scheme == "http" && portNumber == 80) ||
			(parsed.Scheme == "https" && portNumber == 443) {
			return fmt.Errorf("%w: admin public_origin must omit the default port", ErrInvalid)
		}
	}

	if requireHTTPS && parsed.Scheme != "https" {
		return fmt.Errorf("%w: proxy admin public_origin must use https", ErrInvalid)
	}

	return nil
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

func resolveAdmin(fromFile fileAdmin) (Admin, error) {
	adminPort := defaultAdmin.Port
	if fromFile.Port != nil {
		adminPort = *fromFile.Port
	}

	if adminPort == 0 {
		return Admin{}, fmt.Errorf("%w: admin port must be between 1 and 65535", ErrInvalid)
	}

	adminMode := defaultAdmin.AuthMode
	if fromFile.AuthMode != "" {
		adminMode = fromFile.AuthMode
	}

	if adminMode != "local" && adminMode != "proxy" {
		return Admin{}, fmt.Errorf("%w: unsupported admin auth mode %q", ErrInvalid, adminMode)
	}

	adminOrigin := fromFile.PublicOrigin
	if adminOrigin != "" {
		if err := validateAdminOrigin(adminOrigin, adminMode == "proxy"); err != nil {
			return Admin{}, err
		}
	}

	if adminMode == "proxy" && adminOrigin == "" {
		return Admin{}, fmt.Errorf("%w: proxy admin mode requires public_origin", ErrInvalid)
	}

	if adminMode == "local" && adminOrigin != "" {
		return Admin{}, fmt.Errorf("%w: local admin mode does not allow public_origin", ErrInvalid)
	}

	return Admin{Port: adminPort, AuthMode: adminMode, PublicOrigin: adminOrigin}, nil
}
