// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package clientaccess

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"go.trippwill.dev/wake/internal/config"
)

const (
	FileName                   = "auth.json"
	ErrInvalidSecretFile Error = "invalid private authentication state"
	maxAuthBytes               = 128 << 10
	authLockTimeout            = time.Second
	authLockRetry              = 10 * time.Millisecond
)

type FileStore struct{ dir string }

func NewFileStore(dir string) FileStore { return FileStore{dir: dir} }

type authRecord struct {
	Version    int           `json:"version"`
	Unlock     string        `json:"unlock_verifier,omitempty"`
	SessionKey []byte        `json:"session_key,omitempty"`
	Grants     []grantRecord `json:"grants"`
}

type grantRecord struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Created  time.Time `json:"created"`
	Verifier []byte    `json:"verifier"`
}

func (s FileStore) read() (authRecord, error) {
	path := filepath.Join(s.dir, FileName)

	data, err := readPrivateFile(path, maxAuthBytes)
	if errors.Is(err, os.ErrNotExist) {
		return authRecord{Version: 1}, nil
	}

	if err != nil {
		return authRecord{}, fmt.Errorf("read %s: %w", path, err)
	}

	defer clear(data)

	var record authRecord
	if err := json.Unmarshal(data, &record); err != nil || !record.valid() {
		return authRecord{}, fmt.Errorf("read %s: %w", path, ErrInvalidSecretFile)
	}

	return record, nil
}

func (r authRecord) valid() bool {
	if r.Version != 1 || len(r.Grants) > MaxClients {
		return false
	}

	if r.Unlock != "" {
		// Match the v1 Argon2 verifier encoding and sizes.
		if len(r.Unlock) != 69 || r.Unlock[:3] != "v1." || r.Unlock[25] != '.' || len(r.SessionKey) != 32 {
			return false
		}

		for _, value := range []string{r.Unlock[3:25], r.Unlock[26:]} {
			if decoded, err := base64.RawURLEncoding.Strict().
				DecodeString(value); err != nil ||
				base64.RawURLEncoding.EncodeToString(decoded) != value {
				return false
			}
		}
	} else if len(r.SessionKey) != 0 {
		return false
	}

	seen := map[string]bool{}
	for _, grant := range r.Grants {
		if _, err := NewClient(grant.ID, grant.Name, grant.Created); err != nil ||
			len(grant.Verifier) != sha256.Size || seen[grant.ID] {
			return false
		}

		seen[grant.ID] = true
	}

	return true
}

func (r authRecord) clear() {
	clear(r.SessionKey)

	for _, g := range r.Grants {
		clear(g.Verifier)
	}
}

func lockAuthDirectory(ctx context.Context, dir *os.File) error {
	ctx, cancel := context.WithTimeout(ctx, authLockTimeout)
	defer cancel()

	retry := time.NewTicker(authLockRetry)
	defer retry.Stop()

	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		err := syscall.Flock(int(dir.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EINTR) {
			return err
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-retry.C:
		}
	}
}

func (s FileStore) update(ctx context.Context, change func(*authRecord) error) error {
	if err := config.CheckPrivateDir(s.dir); err != nil {
		return err
	}

	dir, err := os.Open(s.dir)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()

	// Lock the directory inode, which survives atomic auth file replacement.
	if err := lockAuthDirectory(ctx, dir); err != nil {
		return fmt.Errorf("lock private authentication state: %w", err)
	}

	record, err := s.read()
	if err != nil {
		return err
	}
	defer func() { record.clear() }()

	if err := change(&record); err != nil {
		return err
	}

	if !record.valid() {
		return ErrInvalidSecretFile
	}

	// #nosec G117 -- This record is written only to the private authentication file.
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	defer clear(data)

	if len(data) >= maxAuthBytes {
		return ErrInvalidSecretFile
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	return writePrivateFile(filepath.Join(s.dir, FileName), data)
}

func (s FileStore) Browser() (string, [32]byte, error) {
	record, err := s.read()
	defer record.clear()

	var key [32]byte
	copy(key[:], record.SessionKey)

	return record.Unlock, key, err
}

func (s FileStore) SetBrowser(verifier string) error {
	return s.update(context.Background(), func(record *authRecord) error {
		record.Unlock = verifier
		record.rotate()

		return nil
	})
}

func (s FileStore) Reset(ctx context.Context) error {
	return s.update(ctx, func(record *authRecord) error {
		record.clear()
		record.Grants = nil
		record.rotate()

		return nil
	})
}

func (r *authRecord) rotate() {
	clear(r.SessionKey)

	r.SessionKey = nil
	if r.Unlock != "" {
		r.SessionKey = make([]byte, 32)
		_, _ = rand.Read(r.SessionKey)
	}
}

func (s FileStore) Clients(context.Context) ([]Client, error) {
	record, err := s.read()
	if err != nil {
		return nil, err
	}
	defer record.clear()

	clients := make([]Client, 0, len(record.Grants))
	for _, g := range record.Grants {
		clients = append(clients, Client{id: g.ID, name: g.Name, created: g.Created})
	}

	return clients, nil
}

func (s FileStore) CreateClient(ctx context.Context, client Client, verifier []byte) error {
	return s.update(ctx, func(record *authRecord) error {
		if len(record.Grants) >= MaxClients {
			return ErrBusy
		}

		record.Grants = append(record.Grants, grantRecord{
			ID: client.id, Name: client.name, Created: client.created, Verifier: append([]byte(nil), verifier...),
		})

		return nil
	})
}

func (s FileStore) ClientVerifier(_ context.Context, id string) ([]byte, error) {
	record, err := s.read()
	if err != nil {
		return nil, err
	}
	defer record.clear()

	for _, g := range record.Grants {
		if g.ID == id {
			return append([]byte(nil), g.Verifier...), nil
		}
	}

	return nil, ErrUnauthorized
}

func (s FileStore) RevokeClient(ctx context.Context, id string) error {
	return s.update(ctx, func(record *authRecord) error {
		for i, g := range record.Grants {
			if g.ID == id {
				clear(g.Verifier)

				record.Grants = append(record.Grants[:i], record.Grants[i+1:]...)

				break
			}
		}

		return nil
	})
}
