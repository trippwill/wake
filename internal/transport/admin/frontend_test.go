// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

//go:build frontend

package admin

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"go.trippwill.dev/wake/internal/config"
)

func TestRenderFrontend(t *testing.T) {
	dir := os.Getenv("WAKE_HTML_DIR")
	if dir == "" {
		t.Fatal("WAKE_HTML_DIR required")
	}
	//nolint:gosec // Caller-selected test artifact directory.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	//nolint:gosec // Caller-selected test artifact directory.
	f, err := os.Create(filepath.Join(dir, "shell.html"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	if err = Overview(
		"synthetic-csrf",
		"/status",
		Status{},
		config.Config{},
	).Render(context.Background(), f); err != nil {
		t.Fatal(err)
	}
}
