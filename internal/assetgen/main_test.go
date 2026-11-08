// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package main

import (
	"bytes"
	"compress/gzip"
	"errors"
	"flag"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestAssetgenCommandUsesRepositoryAssetDirectories(t *testing.T) {
	t.Chdir(t.TempDir())

	source := "internal/transport/admin/assets"
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(source, "app.js"), []byte("const source = true;"), 0o600); err != nil {
		t.Fatal(err)
	}

	previousFlags, previousArgs := flag.CommandLine, os.Args
	t.Cleanup(func() {
		flag.CommandLine, os.Args = previousFlags, previousArgs
	})

	flag.CommandLine = flag.NewFlagSet("assetgen", flag.ContinueOnError)
	os.Args = []string{"assetgen"}

	main()

	data, err := fs.ReadFile(os.DirFS("internal/transport/admin/assetsgzip"), "app.js.gz")
	if err != nil {
		t.Fatal(err)
	}

	reader, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}

	decoded, err := io.ReadAll(reader)
	closeErr := reader.Close()
	if err != nil || closeErr != nil || string(decoded) != "const source = true;" {
		t.Fatal("command output differs from source", err, closeErr)
	}
}

func TestAssetgenCommandRejectsOutputOverrides(t *testing.T) {
	executable := filepath.Join(t.TempDir(), "assetgen")
	//nolint:gosec // Build only this package into a test-owned temporary executable.
	build := exec.Command("go", "build", "-o", executable, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build generator: %v\n%s", err, output)
	}

	for _, destination := range []string{
		"internal/transport/admin/assets",
		"internal/transport/admin",
		"source-alias",
		"parent-alias",
	} {
		t.Run(destination, func(t *testing.T) {
			root := t.TempDir()
			if err := os.MkdirAll(filepath.Join(root, "internal/transport/admin/assets"), 0o700); err != nil {
				t.Fatal(err)
			}

			for _, name := range []string{"internal/transport/admin/assets/app.js", "internal/transport/admin/handler.go"} {
				if err := os.WriteFile(filepath.Join(root, name), []byte("preserve original"), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			for alias, target := range map[string]string{
				"source-alias": "internal/transport/admin/assets",
				"parent-alias": "internal/transport/admin",
			} {
				if err := os.Symlink(target, filepath.Join(root, alias)); err != nil {
					t.Fatal(err)
				}
			}
			//nolint:gosec // Execute this package's generator in a disposable fixture.
			command := exec.Command(executable, "-output", destination)
			command.Dir = root

			output, err := command.CombinedOutput()

			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 2 {
				t.Fatalf("unsupported output override was accepted: %v\n%s", err, output)
			}

			for _, name := range []string{"internal/transport/admin/assets/app.js", "internal/transport/admin/handler.go"} {
				if data, err := fs.ReadFile(os.DirFS(root), name); err != nil || string(data) != "preserve original" {
					t.Fatal("original files changed", name, err)
				}
			}
		})
	}
}

func TestGenerateDeterministicAssetsAndRemoveStaleFiles(t *testing.T) {
	source, output := t.TempDir(), filepath.Join(t.TempDir(), "generated")
	if err := os.Mkdir(filepath.Join(source, "vendor"), 0o700); err != nil {
		t.Fatal(err)
	}

	file := filepath.Join(source, "vendor", "app.js")
	data := []byte("const message = 'exact original source';\n")
	if err := os.WriteFile(file, data, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := generate(source, output); err != nil {
		t.Fatal(err)
	}

	generated := filepath.Join(output, "vendor", "app.js.gz")

	first, err := fs.ReadFile(os.DirFS(output), "vendor/app.js.gz")
	if err != nil {
		t.Fatal(err)
	}

	reader, err := gzip.NewReader(bytes.NewReader(first))
	if err != nil {
		t.Fatal(err)
	}

	decoded, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}

	if err = reader.Close(); err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(decoded, data) || !reader.ModTime.IsZero() || reader.Name != "" {
		t.Fatal("source bytes or deterministic gzip metadata changed", reader.Header)
	}

	if err = os.Chtimes(file, time.Unix(123, 0), time.Unix(456, 0)); err != nil {
		t.Fatal(err)
	}

	if err = generate(source, output); err != nil {
		t.Fatal(err)
	}

	second, err := fs.ReadFile(os.DirFS(output), "vendor/app.js.gz")
	if err != nil || !bytes.Equal(first, second) {
		t.Fatal("generation depends on source timestamps or prior output", err)
	}

	if err = os.Remove(file); err != nil {
		t.Fatal(err)
	}

	if err = os.WriteFile(filepath.Join(source, "new.css"), []byte("body{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err = generate(source, output); err != nil {
		t.Fatal(err)
	}

	if _, err = os.Stat(generated); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("removed source left stale compressed output", err)
	}

	if _, err = os.Stat(filepath.Join(output, "new.css.gz")); err != nil {
		t.Fatal("new source was not generated", err)
	}
}

func TestGenerateRejectsSymlinksBeforeReplacingOutput(t *testing.T) {
	source, output := t.TempDir(), t.TempDir()
	retained := filepath.Join(output, "existing.gz")
	if err := os.WriteFile(retained, []byte("preserve on failure"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.Symlink(retained, filepath.Join(source, "linked")); err != nil {
		t.Fatal(err)
	}

	if err := generate(source, output); !errors.Is(err, fs.ErrInvalid) {
		t.Fatal("linked asset was not rejected", err)
	}

	if data, err := fs.ReadFile(
		os.DirFS(output),
		"existing.gz",
	); err != nil ||
		string(data) != "preserve on failure" {
		t.Fatal("failed generation replaced existing output", err)
	}
}
