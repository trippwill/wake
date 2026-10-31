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
	"testing"

	"github.com/BurntSushi/toml"
)

func TestEverySettingResolvesAllSources(t *testing.T) {
	for _, setting := range Settings() {
		t.Run(setting.Flag, func(t *testing.T) {
			roots := configureEnvironment(t, true)
			values := []string{"2", "3", "4"}
			switch setting.Type {
			case "bool":
				values = []string{"false", "true", "false"}
			case "duration":
				values = []string{"20s", "21s", "22s"}
			case "string":
				switch setting.Flag {
				case "log-level":
					values = []string{"debug", "warn", "error"}
				case "admin-auth-mode":
					values = []string{"proxy", "proxy", "proxy"}
				case "admin-public-origin":
					values = []string{"https://one.example", "https://two.example", "https://three.example"}
				default:
					values = []string{
						filepath.Join(roots.base, "file"),
						filepath.Join(roots.base, "env"),
						filepath.Join(roots.base, "flag"),
					}
				}
			}

			parts := strings.Split(setting.Key, ".")
			tree := map[string]any{parts[0]: map[string]any{parts[1]: tomlValue(t, setting.Type, values[0])}}
			if strings.HasPrefix(setting.Flag, "admin-") && setting.Flag != "admin-port" {
				admin := tree["admin"].(map[string]any)
				admin["auth_mode"] = "proxy"
				if setting.Flag != "admin-public-origin" {
					admin["public_origin"] = "https://wake.example"
				}
			}

			var contents bytes.Buffer
			if err := toml.NewEncoder(&contents).Encode(tree); err != nil {
				t.Fatal(err)
			}

			path := filepath.Join(roots.base, "settings.toml")
			if err := os.WriteFile(path, contents.Bytes(), 0o600); err != nil {
				t.Fatal(err)
			}

			override := Overrides{ConfigPath: path, ConfigPathExplicit: true}
			assertResolvedValue(t, override, setting.Key, values[0], "toml:"+path)
			t.Setenv(setting.Environment, values[1])
			assertResolvedValue(t, override, setting.Key, values[1], "environment:"+setting.Environment)
			override.Values = map[string]string{setting.Flag: values[2]}
			assertResolvedValue(t, override, setting.Key, values[2], "flag")

			for _, dir := range []string{filepath.Join(roots.config, "wake"), filepath.Join(roots.data, "wake"), filepath.Join(roots.runtime, "wake")} {
				if _, err := os.Stat(dir); !os.IsNotExist(err) {
					t.Fatalf("resolution prepared %s: %v", dir, err)
				}
			}
		})
	}
}

func tomlValue(t *testing.T, kind, value string) any {
	t.Helper()

	switch kind {
	case "bool":
		v, err := strconv.ParseBool(value)
		if err != nil {
			t.Fatal(err)
		}

		return v
	case "int64", "uint16":
		v, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			t.Fatal(err)
		}

		return v
	default:
		return value
	}
}

func assertResolvedValue(t *testing.T, o Overrides, key, want, source string) {
	t.Helper()

	out, err := resolveForTest(o)
	if err != nil {
		t.Fatal(err)
	}

	for _, value := range out.Values {
		if value.Key == key {
			if fmt.Sprint(value.Value) != want || value.Source != source {
				t.Fatalf("%s = %#v; want %s from %s", key, value, want, source)
			}

			return
		}
	}

	t.Fatalf("setting %s missing", key)
}

func TestInvalidSourcesDoNotFallBack(t *testing.T) {
	for _, test := range []struct{ name, value string }{{"WAKE_LOG_LEVEL", ""}, {"WAKE_RUNTIME_DIR", ""}, {"WAKE_ADMIN_PORT", "0"}, {"WAKE_ADMIN_AUTH_MODE", ""}} {
		t.Run(test.name+test.value, func(t *testing.T) {
			configureEnvironment(t, true)
			t.Setenv(test.name, test.value)

			if _, err := resolveForTest(Overrides{}); err == nil {
				t.Fatal("invalid explicit environment value fell back")
			}
		})
	}

	roots := configureEnvironment(t, true)
	path := filepath.Join(roots.base, "bad.toml")
	for _, contents := range []string{"broken = [", "[unknown]\nvalue = 1\n", "[storage]\nwarehouse_path = 'relative'\n"} {
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}

		if _, err := resolveForTest(
			Overrides{ConfigPath: path, ConfigPathExplicit: true},
		); !errors.Is(
			err,
			ErrInvalid,
		) {
			t.Fatalf("config error = %v", err)
		}
	}
}

func TestConfigSelectorAndExplicitEmptyValues(t *testing.T) {
	roots := configureEnvironment(t, true)
	t.Setenv("WAKE_CONFIG", filepath.Join(roots.base, "missing"))

	if _, err := resolveForTest(Overrides{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("explicit environment file was optional: %v", err)
	}

	path := filepath.Join(roots.base, "selected.toml")
	if err := os.WriteFile(path, []byte("[log]\nlevel='debug'\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	out, err := resolveForTest(
		Overrides{
			ConfigPath:         path,
			ConfigPathExplicit: true,
			Values: map[string]string{
				"master-key-path":     "",
				"admin-public-origin": "",
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	if out.ConfigSource != "flag" ||
		out.Config.Credentials.MasterKeyPath != "" {
		t.Fatalf("resolution = %#v", out)
	}

	if _, err := resolveForTest(
		Overrides{ConfigPath: "", ConfigPathExplicit: true},
	); !errors.Is(
		err,
		ErrInvalid,
	) {
		t.Fatalf("empty config selection = %v", err)
	}
}

func TestResolutionRejectsNonRegularConfig(t *testing.T) {
	roots := configureEnvironment(t, true)
	for _, path := range []string{roots.base, os.DevNull} {
		if _, err := resolveForTest(
			Overrides{ConfigPath: path, ConfigPathExplicit: true},
		); !errors.Is(
			err,
			ErrInvalid,
		) {
			t.Fatalf("non-regular config %s = %v", path, err)
		}
	}
}

func TestStorePathsHaveIndependentXDGDefaults(t *testing.T) {
	roots := configureEnvironment(t, true)
	warehouse := filepath.Join(roots.base, "custom-data", "history.sqlite")
	overrides := Overrides{Values: map[string]string{"warehouse-path": warehouse}}
	assertResolvedValue(
		t,
		overrides,
		"storage.connections_path",
		filepath.Join(roots.config, "wake", "connections.db"),
		"XDG/default",
	)

	custom := filepath.Join(roots.base, "private-access", "credentials.sqlite")
	overrides.Values["connections-path"] = custom

	cfg, err := loadForTest(overrides)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Storage.ConnectionsPath != custom || cfg.Storage.WarehousePath != warehouse {
		t.Fatal(cfg.Storage)
	}

	info, err := os.Stat(filepath.Dir(custom))
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatal("access directory not private", info, err)
	}

	if _, err := os.Stat(custom); !os.IsNotExist(err) {
		t.Fatal("configuration opened the credential database", err)
	}
}

func TestConnectionsDatabaseRejectsInvalidAndCollidingPaths(t *testing.T) {
	roots := configureEnvironment(t, true)
	for _, value := range []string{"", "relative.db", filepath.Join(roots.data, "wake", "wake.db"), filepath.Join(roots.data, "wake", "wake.db-journal"), filepath.Join(roots.data, "wake", "wake.db-mjABCDEF912")} {
		if _, err := resolveForTest(
			Overrides{Values: map[string]string{"connections-path": value}},
		); !errors.Is(
			err,
			ErrInvalid,
		) {
			t.Fatalf("accepted credential path %q: %v", value, err)
		}
	}

	for _, value := range []string{"", "relative.db"} {
		t.Setenv("WAKE_CONNECTIONS_PATH", value)

		if _, err := resolveForTest(Overrides{}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("accepted credential environment %q: %v", value, err)
		}
	}
}

func TestStrictFileValidationPrecedesExplicitFlags(t *testing.T) {
	roots := configureEnvironment(t, true)
	path := filepath.Join(roots.base, "selected.toml")
	for _, content := range []string{
		"broken = [", "unknown = true", "[log]\nlevel='verbose'", "[log]\nlevel=''",
		"[admin]\nport=0", "[admin]\nauth_mode='password'", "[admin]\nauth_mode=''",
		"[admin]\npublic_origin='https://wake.example/path'", "[storage]\nwarehouse_path=''",
		"[credentials]\nmaster_key_path='relative'",
	} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}

		_, err := resolveForTest(Overrides{ConfigPath: path, ConfigPathExplicit: true, Values: map[string]string{
			"log-level": "info", "admin-port": "8787", "admin-auth-mode": "local", "admin-public-origin": "",
			"warehouse-path": filepath.Join(roots.base, "valid.db"), "master-key-path": "",
		}})
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid selected file was hidden by flags: %q: %v", content, err)
		}
	}
}

func TestExplicitFlagsIgnoreUnusedInvalidEnvironment(t *testing.T) {
	configureEnvironment(t, true)
	t.Setenv("WAKE_ADMIN_PORT", "not-a-number")
	t.Setenv("WAKE_LOG_LEVEL", "invalid")

	out, err := resolveForTest(Overrides{Values: map[string]string{"admin-port": "9876", "log-level": "warn"}})
	if err != nil || out.Config.Admin.Port != 9876 {
		t.Fatal(out, err)
	}
}

func TestProxySettingsCanCombinePartialFileAndEnvironment(t *testing.T) {
	roots := configureEnvironment(t, true)
	path := filepath.Join(roots.base, "partial.toml")
	if err := os.WriteFile(
		path,
		[]byte("[admin]\npublic_origin='https://wake.example.test'\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	t.Setenv("WAKE_ADMIN_AUTH_MODE", "proxy")

	out, err := resolveForTest(Overrides{ConfigPath: path, ConfigPathExplicit: true})
	if err != nil || out.Config.Admin.AuthMode != "proxy" ||
		out.Config.Admin.PublicOrigin != "https://wake.example.test" {
		t.Fatal("valid effective configuration was rejected before source resolution", out, err)
	}
}

func TestSelectedFileRejectsNoncanonicalKeysBeforeOverrides(t *testing.T) {
	roots := configureEnvironment(t, true)
	path := filepath.Join(roots.base, "noncanonical.toml")
	for _, content := range []string{
		"[log]\nLEVEL='verbose'", "[LOG]\nlevel='info'", "[Log]\nlevel='debug'",
		"[log]\nlevel='info'\nLEVEL='verbose'", "[ADMIN]\nport=0",
	} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}

		_, err := resolveForTest(
			Overrides{
				ConfigPath:         path,
				ConfigPathExplicit: true,
				Values:             map[string]string{"log-level": "info", "admin-port": "8787"},
			},
		)
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("noncanonical TOML key silently fell back: %q: %v", content, err)
		}
	}
}
