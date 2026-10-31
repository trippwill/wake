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
	"go.trippwill.dev/wake/provider"
	"go.trippwill.dev/wake/providers/ibkrflex"
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

	registration, err := ibkrflex.Registration(ibkrflex.Options{})
	if err != nil {
		t.Fatal(err)
	}

	source := application.ConnectionView{
		Descriptor:       registration.Descriptor(),
		ID:               "synthetic-source",
		Name:             "Synthetic IBKR",
		SettingsStored:   true,
		CredentialStored: true,
	}
	view := application.Dashboard{Connections: []application.ConnectionView{source}}
	for name, component := range map[string]templ.Component{
		"connections":    ConnectionsPage("synthetic-csrf", application.Dashboard{}),
		"activity":       ActivityPage(application.Dashboard{}),
		"source":         ConnectionPage("synthetic-csrf", view, source),
		"source-access":  ConnectionAccess("synthetic-csrf", source),
		"source-reports": ConnectionReports("synthetic-csrf", source),
		"source-new":     ConnectionSetup("synthetic-csrf", []provider.Registration{registration}),
		"reference":      ReferencePage("synthetic-csrf", view),
		"recovery":       RecoveryPage("synthetic-csrf", view),
		"maintenance":    MaintenancePage("synthetic-csrf", view),
		"error":          ErrorPage("Temporarily unavailable", "Retry the current operation."),
		"correction":     CorrectionPage("synthetic-csrf", application.CorrectionForm{Kind: "activity", ID: "synthetic", Label: "Synthetic activity", Revision: 1, Fields: []string{"price", "quantity"}, Candidates: []application.CorrectionCandidate{{Value: "synthetic-candidate", Label: "Synthetic retained observation"}}, Decisions: []application.DecisionRecord{{Scope: "price", Text: "1.25", Currency: "USD", Active: true}}}),
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
