// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

//go:build frontend

package admin

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/a-h/templ"
	"go.trippwill.dev/wake/internal/application"
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

	for name, component := range map[string]templ.Component{
		"connections": ConnectionsPage("synthetic-csrf", application.Dashboard{}),
		"activity":    ActivityPage(application.Dashboard{}),
	} {
		//nolint:gosec // Fixed page names in the caller-selected synthetic output directory.
		output, e := os.Create(filepath.Join(dir, name+".html"))
		if e != nil {
			t.Fatal(e)
		}

		e = SectionShell(name, "synthetic-csrf", name, component).Render(context.Background(), output)
		closeErr := output.Close()
		if e != nil || closeErr != nil {
			t.Fatal(e, closeErr)
		}
	}
}
