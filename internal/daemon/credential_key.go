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

func GenerateCredentialKey(ctx context.Context, cfg config.Config) error {
	return WithStoppedProfile(cfg, func() error {
		path := cfg.Credentials.KeyPath
		if _, err := os.Lstat(path); err == nil {
			return credential.ErrMasterKeyExists
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}

		if err := persistence.CheckStoredCredentialKey(ctx, cfg.Storage.ConnectionsPath, nil); err != nil {
			return err
		}

		return credential.CreateMasterKey(path)
	})
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

	reason := "Restore a usable credential encryption key. If the key file is missing, generate one with wake credentials generate-key while Wake is stopped."
	if errors.Is(keyErr, persistence.ErrCredentialKeyRequired) {
		reason = "Restore the matching credential encryption key, or use wake reset credentials while Wake is stopped and then provision a usable key."
	}

	return nil, reason, store.PauseForCredentialKey(ctx)
}
