// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

// Assetgen creates deterministic gzip copies of the admin's source assets.
package main

import (
	"bytes"
	"compress/gzip"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

func main() {
	output := flag.String("output", "internal/transport/admin/assetsgzip", "generated asset directory")

	flag.Parse()

	if err := generate("internal/transport/admin/assets", *output); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func generate(source, output string) error {
	files := make(map[string][]byte)

	err := fs.WalkDir(os.DirFS(source), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		if entry.IsDir() {
			return nil
		}

		if !entry.Type().IsRegular() {
			return fmt.Errorf("asset %s: %w", name, fs.ErrInvalid)
		}

		data, err := fs.ReadFile(os.DirFS(source), name)
		if err != nil {
			return err
		}

		var compressed bytes.Buffer

		writer, err := gzip.NewWriterLevel(&compressed, gzip.BestCompression)
		if err != nil {
			return err
		}

		if _, err = writer.Write(data); err != nil {
			return err
		}

		if err = writer.Close(); err != nil {
			return err
		}

		files[name+".gz"] = compressed.Bytes()

		return nil
	})
	if err != nil {
		return err
	}

	if err = os.RemoveAll(output); err != nil {
		return err
	}

	for name, data := range files {
		destination := filepath.Join(output, filepath.FromSlash(name))
		//nolint:gosec // Generated browser assets are public repository content.
		if err = os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			return err
		}
		//nolint:gosec // Generated browser assets are public repository content.
		if err = os.WriteFile(destination, data, 0o644); err != nil {
			return err
		}
	}

	return nil
}
