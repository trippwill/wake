// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/urfave/cli/v3"
)

const ErrConfigExists Error = "configuration already exists"

// Initialize creates the selected TOML once, using the scalar settings passed
// to --set. An occupied destination is left untouched.
func Initialize(command *cli.Command) (Resolution, error) {
	path, source, err := SelectConfigPath(command.String("config"), command.IsSet("config"))
	if err != nil {
		return Resolution{}, err
	}

	path = filepath.Clean(path)
	if _, err := os.Lstat(path); err == nil {
		return Resolution{}, fmt.Errorf("%w at %s", ErrConfigExists, path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return Resolution{}, err
	}

	content, err := initialConfig(command.StringSlice("set"))
	if err != nil {
		return Resolution{}, err
	}

	runtimeRequired := command.Bool("generate-key")

	resolved, err := resolveSnapshot(path, source, content, runtimeRequired)
	if err != nil {
		return Resolution{}, err
	}

	dir := filepath.Dir(path)
	if err := ensurePrivateDir(dir); err != nil {
		return Resolution{}, err
	}

	canonical, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return Resolution{}, err
	}

	if canonical != dir {
		path = filepath.Join(canonical, filepath.Base(path))

		resolved, err = resolveSnapshot(path, source, content, runtimeRequired)
		if err != nil {
			return Resolution{}, err
		}
	}

	if err := publishNewConfig(path, content); err != nil {
		if errors.Is(err, os.ErrExist) {
			return Resolution{}, fmt.Errorf("%w at %s", ErrConfigExists, path)
		}

		return Resolution{}, err
	}

	return resolved, nil
}

func initialConfig(overrides []string) ([]byte, error) {
	types := make(map[string]string)
	for _, setting := range Settings() {
		types[setting.Key] = setting.Type
	}

	tables := make(map[string]map[string]any)
	for _, assignment := range overrides {
		key, value, ok := strings.Cut(assignment, "=")
		if !ok || key == "" {
			return nil, fmt.Errorf("%w: --set requires KEY=VALUE", ErrInvalid)
		}

		kind, ok := types[key]
		if !ok {
			return nil, fmt.Errorf("%w: unknown --set key %q; see wake config describe", ErrInvalid, key)
		}

		table, name, _ := strings.Cut(key, ".")
		if tables[table] == nil {
			tables[table] = make(map[string]any)
		}

		if _, exists := tables[table][name]; exists {
			return nil, fmt.Errorf("%w: duplicate --set key %q", ErrInvalid, key)
		}

		entry, err := parseInitialSetting(key, kind, value)
		if err != nil {
			return nil, err
		}

		tables[table][name] = entry
	}

	var content bytes.Buffer
	content.WriteString("# Wake configuration. See wake config describe for settings and defaults.\n")

	if err := toml.NewEncoder(&content).Encode(tables); err != nil {
		return nil, err
	}

	return content.Bytes(), nil
}

func parseInitialSetting(key, kind, value string) (any, error) {
	switch kind {
	case "string":
		return value, nil
	case "bool":
		if value == "true" || value == "false" {
			return value == "true", nil
		}
	case "integer":
		if number, err := strconv.ParseInt(value, 10, 64); err == nil {
			return number, nil
		}
	}

	return nil, fmt.Errorf("%w: --set %s requires a %s value", ErrInvalid, key, kind)
}

func publishNewConfig(path string, content []byte) error {
	file, err := os.CreateTemp(
		filepath.Dir(path),
		".wake-config-",
	) // #nosec G703 -- validated private instance directory.
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(file.Name()) }()

	if _, err = file.Write(content); err == nil {
		err = file.Sync()
	}

	if closeErr := file.Close(); err == nil {
		err = closeErr
	}

	if err != nil {
		return err
	}

	// Link publishes the complete snapshot and fails if a concurrent writer
	// took the destination after the initial check.
	return os.Link(
		file.Name(),
		path,
	) // #nosec G703 -- validated absolute config path inside the private instance directory.
}
