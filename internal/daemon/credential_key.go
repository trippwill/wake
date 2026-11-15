// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package daemon

import (
	"context"
	"errors"
	"os"

	"go.trippwill.dev/wake/internal/config"
	"go.trippwill.dev/wake/internal/credential"
	persistence "go.trippwill.dev/wake/internal/persistence/sqlite"
)

// EnsureCredentialKey checks an existing key or creates a missing key offline.
func EnsureCredentialKey(ctx context.Context, cfg config.Config) (bool, error) {
	generated := false
	stores := []string{cfg.Storage.WarehousePath, cfg.Storage.ConnectionsPath, cfg.Storage.SyncPath}
	err := withStoppedStores(cfg, stores, func() error {
		path := cfg.Credentials.KeyPath

		envelope, err := openCredentialKey(path)
		if err == nil {
			defer envelope.Clear()

			return persistence.CheckStoredCredentialKey(ctx, cfg.Storage.ConnectionsPath, envelope)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}

		if err := persistence.CheckStoredCredentialKey(ctx, cfg.Storage.ConnectionsPath, nil); err != nil {
			return err
		}

		if err := credential.CreateMasterKey(path); err != nil {
			return err
		}

		generated = true

		return nil
	})

	return generated, err
}

func openCredentialKey(path string) (*credential.Envelope, error) {
	key, err := credential.LoadMasterKey(path)
	if err != nil {
		return nil, err
	}
	defer key.Clear()

	return credential.NewEnvelope(key)
}

func loadSyncCredentials(
	ctx context.Context,
	cfg config.Config,
	store *persistence.Store,
) (*credential.Envelope, string, error) {
	// Direct composition tests may supply a config with no credential path.
	if cfg.Credentials.KeyPath == "" {
		return nil, "", nil
	}

	envelope, keyErr := openCredentialKey(cfg.Credentials.KeyPath)
	if err := store.CheckCredentialKey(ctx, envelope); err != nil {
		envelope.Clear()

		if !errors.Is(err, persistence.ErrCredentialKeyRequired) {
			return nil, "", err
		}

		keyErr = err
	}

	if keyErr == nil {
		return envelope, "", nil
	}

	reason := "Restore a usable credential encryption key at the configured path. For a new credentials store, stop Wake and run wake access provision credential-key."
	if errors.Is(keyErr, persistence.ErrCredentialKeyRequired) {
		reason = "Restore the matching credential encryption key, or stop Wake, run wake storage reset credentials, then run wake access provision credential-key."
	}

	return nil, reason, store.PauseForCredentialKey(ctx)
}
