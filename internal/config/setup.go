// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// LocalSetup describes local provisioning without requiring daemon runtime or
// data directories. ConfigPath is the only configuration file it may update.
type LocalSetup struct {
	ConfigDir     string
	ConfigPath    string
	MasterKeyPath string
	KeyConfigured bool
	original      []byte
	existed       bool
	launch        *SetupGuidance
}

// ReadLocalSetup reads and validates local-mode configuration. An empty path
// selects the default file; an explicit path may select a new destination.
func ReadLocalSetup(path string) (LocalSetup, error) {
	dir, err := ConfigDir()
	if err != nil {
		return LocalSetup{}, err
	}

	explicit := path != ""
	if !explicit {
		path = filepath.Join(dir, "config.toml")
	}

	if err := requireAbsolute("config path", path); err != nil {
		return LocalSetup{}, err
	}

	contents, existed, err := readSetupFile(path)
	if err != nil {
		return LocalSetup{}, err
	}

	cfg, err := decodeFileConfig(contents)
	if err != nil {
		return LocalSetup{}, err
	}

	admin, err := resolveAdmin(cfg.Admin)
	if err != nil {
		return LocalSetup{}, err
	}

	if admin.AuthMode != "local" {
		return LocalSetup{}, fmt.Errorf(
			"%w: wake setup requires local admin mode; use wake admin set-proxy-guard for proxy access",
			ErrInvalid,
		)
	}

	keyPath := cfg.Credentials.MasterKeyPath
	configured := keyPath != ""
	if !configured {
		keyPath = filepath.Join(dir, "master.key")

		if err := ensurePrivateDir(filepath.Dir(path)); err != nil {
			return LocalSetup{}, err
		}
	}

	return LocalSetup{
		ConfigDir: dir, ConfigPath: path, MasterKeyPath: keyPath,
		KeyConfigured: configured, original: contents, existed: existed,
	}, nil
}

// ConfigExists reports whether the selected destination existed when read.
func (s LocalSetup) ConfigExists() bool { return s.existed }

// Save records the verified key path if it was absent. Existing configuration
// values are preserved; TOML formatting/comments are not retained on a rewrite.
// A configured path needs no rewrite. All saves reject changed configuration.
func (s LocalSetup) Save() error {
	current, existed, err := readSetupFile(s.ConfigPath)
	if err != nil {
		return err
	}

	if existed != s.existed || !bytes.Equal(current, s.original) {
		return fmt.Errorf("%w: configuration changed during setup; rerun setup", ErrInvalid)
	}

	if s.KeyConfigured && s.launch == nil {
		return nil
	}

	values := make(map[string]any)
	if _, err := toml.Decode(string(s.original), &values); err != nil {
		return fmt.Errorf("%w: decode setup configuration", ErrInvalid)
	}

	if err := ensurePrivateDir(filepath.Dir(s.ConfigPath)); err != nil {
		return err
	}

	credentials, ok := values["credentials"].(map[string]any)
	if !ok {
		credentials = map[string]any{}
		values["credentials"] = credentials
	}

	credentials["master_key_path"] = s.MasterKeyPath
	if s.launch != nil {
		paths, ok := values["paths"].(map[string]any)
		if !ok {
			paths = map[string]any{}
			values["paths"] = paths
		}

		paths["data_dir"] = s.launch.DataDir
		paths["state_dir"] = s.launch.StateDir
		paths["runtime_dir"] = s.launch.RuntimeDir

		admin, ok := values["admin"].(map[string]any)
		if !ok {
			admin = map[string]any{}
			values["admin"] = admin
		}

		admin["port"] = s.launch.AdminPort
	}

	file, err := os.CreateTemp(filepath.Dir(s.ConfigPath), ".wake-setup-")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(file.Name()) }()

	if err = toml.NewEncoder(file).Encode(values); err == nil {
		err = file.Sync()
	}

	if closeErr := file.Close(); err == nil {
		err = closeErr
	}

	if err != nil {
		return err
	}

	if !s.existed {
		// Publish a new destination exclusively: a concurrently created file must
		// never be replaced between the initial change check and final publication.
		if err := os.Link(file.Name(), s.ConfigPath); err != nil {
			if errors.Is(err, os.ErrExist) {
				return fmt.Errorf("%w: configuration created during setup; rerun setup", ErrInvalid)
			}

			return err
		}

		return nil
	}

	return os.Rename(file.Name(), s.ConfigPath)
}

func readSetupFile(path string) ([]byte, bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}

	if err != nil {
		return nil, false, err
	}

	if !info.Mode().IsRegular() {
		return nil, false, fmt.Errorf("%w: setup config must be a non-symlink regular file", ErrInsecurePath)
	}
	// #nosec G304,G703 -- the absolute operator-selected config path is checked above.
	contents, err := os.ReadFile(path)

	return contents, true, err
}

// SetupGuidance carries the small set of settings presented during local setup.
type SetupGuidance struct {
	DataDir, StateDir, RuntimeDir string
	AdminPort                     uint16
}

// Guidance reads existing TOML and XDG defaults, excluding WAKE_* launch overrides.
func (s LocalSetup) Guidance() (SetupGuidance, error) {
	file, err := decodeFileConfig(s.original)
	if err != nil {
		return SetupGuidance{}, err
	}

	defaults, _, err := settingDefaults()
	if err != nil {
		return SetupGuidance{}, err
	}

	data, state, runtime := file.Paths.DataDir, file.Paths.StateDir, file.Paths.RuntimeDir
	if data == "" {
		data = defaults["data-dir"]
	}

	if state == "" {
		state = defaults["state-dir"]
	}

	if runtime == "" {
		runtime = defaults["runtime-dir"]
	}

	admin, err := resolveAdmin(file.Admin)
	if err != nil {
		return SetupGuidance{}, err
	}

	return SetupGuidance{DataDir: data, StateDir: state, RuntimeDir: runtime, AdminPort: admin.Port}, nil
}

// WithLaunchSettings validates a proposed update before any provisioning writes.
func (s LocalSetup) WithLaunchSettings(data, runtime string, port uint16) (LocalSetup, error) {
	if err := requireAbsolute("data directory", data); err != nil {
		return s, err
	}

	if err := requireAbsolute("runtime directory", runtime); err != nil {
		return s, err
	}

	file, err := decodeFileConfig(s.original)
	if err != nil {
		return s, err
	}

	file.Paths.DataDir, file.Paths.RuntimeDir = data, runtime
	file.Admin.Port = &port
	if _, err := resolveAdmin(file.Admin); err != nil {
		return s, err
	}

	guide, err := s.Guidance()
	if err != nil {
		return s, err
	}

	s.launch = &SetupGuidance{DataDir: data, StateDir: guide.StateDir, RuntimeDir: runtime, AdminPort: port}

	return s, nil
}
