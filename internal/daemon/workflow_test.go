// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package daemon

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/credential"
	"go.trippwill.dev/wake/internal/host"
	persistence "go.trippwill.dev/wake/internal/persistence/sqlite"
	"go.trippwill.dev/wake/provider"
	"go.trippwill.dev/wake/providers/ibkrflex"
)

func TestDurableDiscoveryResumesAndJoinsCancellation(t *testing.T) {
	cfg := shellConfig(t)
	cfg.Credentials.MasterKeyPath = filepath.Join(cfg.Paths.ConfigDir, "master.key")
	if err := credential.CreateMasterKey(cfg.Credentials.MasterKeyPath); err != nil {
		t.Fatal(err)
	}

	key, err := credential.LoadMasterKey(cfg.Credentials.MasterKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	defer key.Clear()

	envelope, err := credential.NewEnvelope(key)
	if err != nil {
		t.Fatal(err)
	}
	defer envelope.Clear()

	started := make(chan struct{})
	canceled := make(chan struct{})

	server := httptest.NewTLSServer(
		http.HandlerFunc(
			func(w http.ResponseWriter, r *http.Request) { close(started); <-r.Context().Done(); close(canceled) },
		),
	)
	defer server.Close()

	options := ibkrflex.Options{HTTPClient: server.Client(), BaseURL: server.URL}

	registration, err := ibkrflex.Registration(options)
	if err != nil {
		t.Fatal(err)
	}

	store, err := persistence.Open(
		context.Background(),
		persistence.Profile{
			WarehousePath:   cfg.Storage.WarehousePath,
			ConnectionsPath: cfg.Storage.ConnectionsPath,
		},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}

	h, err := host.New(context.Background(), store, envelope, nil, nil, registration)
	if err != nil {
		t.Fatal(err)
	}

	workflow := application.New(store, h, nil, nil, nil)

	token := credential.NewSecret([]byte("synthetic-token"))
	defer token.Clear()

	id, err := workflow.CreateConnection(
		context.Background(),
		"Durable discovery",
		"ibkr-flex",
		confirmationSettings("123"),
		map[string]*credential.Secret{"token": token},
	)
	if err != nil {
		t.Fatal(err)
	}

	h.Close()

	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	// The daemon must pick up intent created before its process lifetime.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- run(ctx, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), sourceOptions{IBKR: options})
	}()

	select {
	case <-started:
	case err = <-done:
		t.Fatal(err)
	case <-time.After(10 * time.Second):
		t.Fatal("durable work did not resume")
	}

	cancel()

	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("daemon did not join source cancellation")
	}

	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("upstream request remained alive")
	}

	store, err = persistence.Open(
		context.Background(),
		persistence.Profile{
			WarehousePath:   cfg.Storage.WarehousePath,
			ConnectionsPath: cfg.Storage.ConnectionsPath,
		},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = store.Close() }()

	checkpoint, err := store.Checkpoint(
		context.Background(),
		host.WorkKey{Connection: id, Stream: ibkrflex.Confirmations, Operation: "discover"},
	)
	if err != nil || checkpoint.Revision != 0 || checkpoint.Acknowledged.Phase() != "" {
		t.Fatal("cancellation acknowledged uncommitted work", checkpoint, err)
	}
}

func confirmationSettings(query string) provider.Settings {
	settings, _ := provider.NewSettings(map[string]string{"query_id": query})
	return settings
}
