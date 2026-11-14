// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package daemon

import (
	"bufio"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/clientaccess"
	"go.trippwill.dev/wake/internal/config"
	"go.trippwill.dev/wake/internal/credential"
	"go.trippwill.dev/wake/internal/host"
	persistence "go.trippwill.dev/wake/internal/persistence/sqlite"
	"go.trippwill.dev/wake/internal/reference"
	"go.trippwill.dev/wake/internal/transport/admin"
)

func TestCredentialLossPreservesFinancialAccess(t *testing.T) {
	for _, kind := range []string{"key missing", "key mismatched", "connections missing"} {
		t.Run(kind, func(t *testing.T) {
			cfg := shellConfig(t)
			cfg.Credentials.KeyPath = filepath.Join(cfg.Paths.ConfigDir, "credentials.key")
			if err := GenerateCredentialKey(t.Context(), cfg); err != nil {
				t.Fatal(err)
			}

			if err := admin.SetUnlock(cfg.Paths.ConfigDir, []byte("original synthetic unlock")); err != nil {
				t.Fatal(err)
			}

			seedAPIClient(t, cfg)
			seedSavedCredential(t, cfg, kind == "key mismatched")

			// #nosec G304 -- Synthetic key in the test instance.
			matchingKey, err := os.ReadFile(cfg.Credentials.KeyPath)
			if err != nil {
				t.Fatal(err)
			}
			defer clear(matchingKey)

			switch kind {
			case "key missing":
				if err := os.Remove(cfg.Credentials.KeyPath); err != nil {
					t.Fatal(err)
				}
			case "key mismatched":
				if err := os.Remove(cfg.Credentials.KeyPath); err != nil {
					t.Fatal(err)
				}

				if err := credential.CreateMasterKey(cfg.Credentials.KeyPath); err != nil {
					t.Fatal(err)
				}
			case "connections missing":
				if err := os.Remove(cfg.Storage.ConnectionsPath); err != nil {
					t.Fatal(err)
				}
			}

			if kind == "key missing" {
				if err := GenerateCredentialKey(
					t.Context(),
					cfg,
				); !errors.Is(
					err,
					persistence.ErrCredentialKeyRequired,
				) {
					t.Fatal("generated a key while saved ciphertext still required the original", err)
				}
			}

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()

			ready, done := make(chan struct{}), make(chan error, 1)
			go func() {
				done <- Run(ctx, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), func(p string) {
					if p == "ready" {
						close(ready)
					}
				})
			}()

			select {
			case <-ready:
			case err := <-done:
				t.Fatal(err)
			case <-time.After(5 * time.Second):
				t.Fatal("startup stalled")
			}

			client := &http.Client{Transport: financialTransport(t, cfg), Timeout: 2 * time.Second}
			for _, path := range []string{"/v1/snapshot", "/v1/events"} {
				request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://wake"+path, nil)
				if err != nil {
					t.Fatal(err)
				}

				response, err := client.Do(request)
				if err != nil {
					t.Fatal(err)
				}

				if path == "/v1/events" {
					assertStreamSurvivesUnlock(t, cfg, response.Body)
				}

				_ = response.Body.Close()
				if response.StatusCode != http.StatusOK {
					t.Fatal(path, response.StatusCode)
				}
			}

			response, err := client.Get("http://wake/v1/snapshot")
			if err != nil {
				t.Fatal(err)
			}

			_ = response.Body.Close()
			if response.StatusCode != http.StatusOK {
				t.Fatal("unlock change revoked financial access", response.Status)
			}

			cancel()

			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("shutdown stalled")
			}

			open, err := persistence.Open(t.Context(), persistence.Profile{
				WarehousePath: cfg.Storage.WarehousePath, ConnectionsPath: cfg.Storage.ConnectionsPath,
			}, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = open.Close() }()

			access, err := open.SourceAccess(t.Context())
			if err != nil || !access.Storage.Paused {
				t.Fatal("upstream work still enabled", access.Storage, err)
			}

			grants, err := clientaccess.NewFileStore(cfg.Paths.ConfigDir).Clients(t.Context())
			if err != nil || len(grants) != 1 {
				t.Fatal("financial grant lost", err)
			}

			if kind != "connections missing" {
				verifyCredentialRepair(t, cfg, open, matchingKey)
			}
		})
	}
}

func seedSavedCredential(t *testing.T, cfg config.Config, reference bool) {
	t.Helper()

	envelope, err := openCredentialKey(cfg.Credentials.KeyPath)
	if err != nil {
		t.Fatal(err)
	}
	defer envelope.Clear()

	db, err := persistence.Open(t.Context(), persistence.Profile{
		WarehousePath: cfg.Storage.WarehousePath, ConnectionsPath: cfg.Storage.ConnectionsPath,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	conn := host.Connection{
		ID:       "source-id",
		Provider: "ibkr-flex",
		Name:     "Synthetic source",
		Settings: confirmationSettings("123"),
	}

	const field = "token"

	aad := credential.SourceSecretAAD(conn.ID, conn.Provider, field)
	if reference {
		aad = credential.ReferenceKeyAAD()
	}

	secret := credential.NewSecret([]byte("synthetic-provider-secret"))
	defer secret.Clear()

	encrypted, err := envelope.Encrypt(secret, aad)
	if err != nil {
		t.Fatal(err)
	}

	payload, err := encrypted.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}

	if reference {
		err = db.SetReferenceKey(t.Context(), payload)
	} else {
		err = db.Configure(t.Context(), conn, map[string][]byte{field: payload})
	}

	if err != nil {
		t.Fatal(err)
	}
}

// An idle stream would return EOF immediately if rotation revoked its grant.
func assertStreamSurvivesUnlock(t *testing.T, cfg config.Config, body io.ReadCloser) {
	t.Helper()

	r := bufio.NewReader(body)
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}

		if line == "\n" {
			break
		}
	}

	if err := admin.SetUnlock(cfg.Paths.ConfigDir, []byte("replacement synthetic unlock")); err != nil {
		t.Fatal(err)
	}

	next := make(chan error, 1)
	go func() { _, err := r.ReadByte(); next <- err }()

	select {
	case err := <-next:
		t.Fatal("stream ended after unlock rotation", err)
	case <-time.After(100 * time.Millisecond):
		_ = body.Close()

		if err := <-next; err == nil {
			t.Fatal("expected closed response body")
		}
	}
}

func verifyCredentialRepair(t *testing.T, cfg config.Config, db *persistence.Store, key []byte) {
	t.Helper()

	_, reason, err := loadSyncCredentials(t.Context(), cfg, db)
	if err != nil || reason == "" {
		t.Fatal("credential loss was not diagnosed", reason, err)
	}

	access, err := db.SourceAccess(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if err := db.ResumeStorage(
		t.Context(),
		access.Storage.Revision,
		"test",
		time.Now(),
	); !errors.Is(
		err,
		application.ErrAccessReview,
	) {
		t.Fatal("resumed without a usable key", err)
	}

	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(cfg.Credentials.KeyPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}

	// #nosec G703 -- Restore a synthetic key into the test's private instance.
	if err := os.WriteFile(cfg.Credentials.KeyPath, key, 0o400); err != nil {
		t.Fatal(err)
	}

	reopened, err := persistence.Open(t.Context(), persistence.Profile{
		WarehousePath: cfg.Storage.WarehousePath, ConnectionsPath: cfg.Storage.ConnectionsPath,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()

	envelope, reason, err := loadSyncCredentials(t.Context(), cfg, reopened)
	if err != nil || reason != "" || envelope == nil {
		t.Fatal("matching key did not repair protection", reason, err)
	}
	defer envelope.Clear()

	options := sourceOptions{}

	registrations, err := options.registrations()
	if err != nil {
		t.Fatal(err)
	}

	providers, err := host.New(t.Context(), reopened, envelope, nil, nil, nil, registrations...)
	if err != nil {
		t.Fatal(err)
	}
	defer providers.Close()

	ref, err := reference.New(reopened, envelope, nil, "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	access, err = reopened.SourceAccess(t.Context())
	if err != nil || !access.Storage.Paused {
		t.Fatal("repair resumed upstream work without review", err)
	}

	workflow := application.New(reopened, providers, ref, nil, nil)
	if err := workflow.ResumeStorage(t.Context(), access.Storage.Revision, "test"); err != nil {
		t.Fatal("repair could not resume upstream work", err)
	}

	access, err = reopened.SourceAccess(t.Context())
	if err != nil || access.Storage.Paused {
		t.Fatal("explicit resume did not clear recovery pause", err)
	}
}
