// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"

	"go.trippwill.dev/wake/internal/credential"
	"go.trippwill.dev/wake/internal/persistence/sqlite/sqlc"
)

// #nosec G101 -- Error text, not key material.
const ErrCredentialKeyRequired Error = "saved credentials require their matching credential encryption key"

// CheckStoredCredentialKey inspects the stopped access store without creating
// SQLite files or recovering a pending transaction.
func CheckStoredCredentialKey(ctx context.Context, path string, envelope *credential.Envelope) error {
	if _, err := os.Lstat(path); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}

		sidecars, err := storeSidecars(path)
		if err != nil {
			return err
		}

		for _, sidecar := range sidecars {
			if _, err := os.Lstat(sidecar); !errors.Is(err, os.ErrNotExist) {
				return ErrUpgradeRecovery
			}
		}

		return nil
	}

	if err := checkUpgradeJournals(path); err != nil {
		return err
	}

	if err := registerCollations(); err != nil {
		return err
	}

	db := sql.OpenDB(&connector{paths: [2]string{path}, probe: true, accessProbe: true})
	defer func() { _ = db.Close() }()

	var kind, format, id string
	if err := db.QueryRowContext(ctx, profileStatement("Metadata", "main")).
		Scan(&kind, &format, &id); err != nil ||
		kind != storeKinds[1] || format != accessSchemaVersion ||
		id == "" {
		return ErrUnsupportedSchema
	}

	var count int
	if err := db.QueryRowContext(ctx, profileStatement("ObjectCount", "main")).Scan(&count); err != nil ||
		count != strings.Count(releaseBaseline().schemas[1], "CREATE TABLE") {
		return ErrUnsupportedSchema
	}

	return checkCredentialKey(ctx, sqlc.New(db), envelope)
}

func (s *Store) CheckCredentialKey(ctx context.Context, envelope *credential.Envelope) error {
	return checkCredentialKey(ctx, s.queries, envelope)
}

func (s *Store) PauseForCredentialKey(ctx context.Context) error {
	if err := s.queries.PauseForCredentialKey(ctx); err != nil {
		return err
	}

	s.credentialKeyUnavailable.Store(true)

	return nil
}

func checkCredentialKey(ctx context.Context, q *sqlc.Queries, envelope *credential.Envelope) error {
	saved, err := q.SavedCredentials(ctx)
	if err != nil {
		return err
	}

	for _, row := range saved {
		if err := checkEncryptedKey(
			envelope,
			row.Encrypted,
			credential.SourceSecretAAD(row.ConnectionID, row.Provider, row.Key),
		); err != nil {
			return err
		}
	}

	key, err := q.SavedReferenceCredential(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}

	if err != nil {
		return err
	}

	if len(key) == 0 {
		return nil
	}

	return checkEncryptedKey(envelope, key, credential.ReferenceKeyAAD())
}

func checkEncryptedKey(envelope *credential.Envelope, data, aad []byte) error {
	if envelope == nil {
		return ErrCredentialKeyRequired
	}

	encrypted, err := credential.ParseEncryptedSecret(data)
	if err != nil {
		return ErrCredentialKeyRequired
	}

	secret, err := envelope.Decrypt(encrypted, aad)
	if err != nil {
		return ErrCredentialKeyRequired
	}

	secret.Clear()

	return nil
}
