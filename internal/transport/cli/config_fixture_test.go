// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/urfave/cli/v3"
	"go.trippwill.dev/wake/internal/config"
)

func launchForTest(path string, prepare bool) (config.Resolution, error) {
	var out config.Resolution

	command := &cli.Command{
		Name: "wake-test", Writer: io.Discard, ErrWriter: io.Discard,
		Flags: []cli.Flag{&cli.StringFlag{Name: "config"}},
		Action: func(_ context.Context, c *cli.Command) error {
			var err error

			out, err = config.Resolve(c)
			if err == nil && prepare {
				err = config.Prepare(out.Config)
			}

			return err
		},
	}

	args := []string{"wake-test"}
	if path != "" {
		args = append(args, "--config="+path)
	}

	err := command.Run(context.Background(), args)

	return out, err
}

func setupDaemonFixture(t *testing.T) (config.Config, string, []byte) {
	t.Helper()

	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(root, "runtime"))

	writeTestConfig(t, filepath.Join(root, "config", "wake", "config.toml"), nil)

	resolved, err := launchForTest("", true)
	cfg := resolved.Config

	if err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(cfg.Paths.ConfigDir, "config.toml")
	content := fmt.Appendf(
		nil,
		"[storage]\nwarehouse_path=%q\nconnections_path=%q\n[paths]\nruntime_dir=%q\n",
		cfg.Storage.WarehousePath, cfg.Storage.ConnectionsPath,
		cfg.Paths.RuntimeDir,
	)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}

	return cfg, path, content
}

func writeTestConfig(t *testing.T, path string, content []byte) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
}
