// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

// Package persistence implements the port's private SQLite storage boundary.
package persistence

import (
	"context"
	"database/sql"
	"database/sql/driver"
	_ "embed"
	"errors"
	"net/url"
	"os"
	"sync"

	"go.trippwill.dev/wake/internal/persistence/sqlite/sqlc"
	modernsqlite "modernc.org/sqlite"
)

type Error string

func (e Error) Error() string { return string(e) }

const (
	ErrUnsupportedSchema Error = "unsupported development store; recreate explicitly"
	ErrUnsafeStore       Error = "unsafe or already owned store path"
	ErrCorrupt           Error = "invalid retained store data"
	schemaVersion              = "wake-m2-1"
)

//go:embed schema.sql
var schemaSQL string

//go:embed connection.sql
var connectionSQL string

//go:embed journal.sql
var journalSQL string

type Store struct {
	db         *sql.DB
	queries    *sqlc.Queries
	owner      *os.File
	closeOnce  sync.Once
	closeError error
}

func Open(ctx context.Context, path string) (*Store, error) {
	owner, err := claimFile(path)
	if err != nil {
		return nil, err
	}

	// This format uses rollback journals. Never open a retired WAL namespace.
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, e := os.Lstat(path + suffix); e == nil {
			_ = owner.Close()
			return nil, ErrUnsupportedSchema
		} else if !os.IsNotExist(e) {
			_ = owner.Close()
			return nil, e
		}
	}
	// Probe immutable main-file metadata before SQLite may recover a journal.
	// This format never mutates its schema after initialization.
	probe := sql.OpenDB(&connector{path: path, owner: owner, probe: true})

	queries := sqlc.New(probe)
	count, probeErr := queries.CountTables(ctx)
	if probeErr == nil && count != 0 {
		version, e := queries.ReadFormat(ctx)
		if e != nil || version != schemaVersion || count != 21 {
			probeErr = ErrUnsupportedSchema
		}
	}

	_ = probe.Close()

	if probeErr == nil && count == 0 {
		if info, e := os.Lstat(path + "-journal"); e == nil && info.Size() > 0 {
			probeErr = ErrUnsupportedSchema
		}
	}

	if probeErr != nil {
		_ = owner.Close()

		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		return nil, ErrUnsupportedSchema
	}

	s := &Store{owner: owner}
	s.db = sql.OpenDB(&connector{path: path, owner: owner})
	s.db.SetMaxOpenConns(1)
	s.db.SetMaxIdleConns(1)

	s.queries = sqlc.New(s.db)
	if err = s.initialize(ctx); err != nil {
		_ = s.Close()
		return nil, err
	}

	return s, nil
}

func (s *Store) Close() error {
	s.closeOnce.Do(func() { s.closeError = errors.Join(s.db.Close(), s.owner.Close()) })
	return s.closeError
}

func (s *Store) initialize(ctx context.Context) error {
	count, err := s.queries.CountTables(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		return ErrUnsupportedSchema
	}

	if count != 0 {
		version, versionErr := s.queries.ReadFormat(ctx)
		if versionErr != nil || version != schemaVersion || count != 21 {
			return ErrUnsupportedSchema
		}
	}

	if _, err = s.db.ExecContext(ctx, journalSQL); err != nil {
		return err
	}

	if count != 0 {
		return s.queries.InterruptAttempts(ctx)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}

	defer func() { _ = tx.Rollback() }()

	if _, err = tx.ExecContext(ctx, schemaSQL); err != nil {
		return err
	}

	if err = s.queries.WithTx(tx).
		SetFormat(ctx, schemaVersion); err != nil {
		return err
	}

	return tx.Commit()
}

// Every physical connection receives its safety settings, including replacements
// after a driver error. The held file's identity is rechecked before SQLite opens.
type connector struct {
	path  string
	owner *os.File
	probe bool
}

func (c *connector) Driver() driver.Driver { return &modernsqlite.Driver{} }

func (c *connector) Connect(ctx context.Context) (driver.Conn, error) {
	if err := checkFile(c.path, c.owner); err != nil {
		return nil, err
	}

	query := "mode=rw&_txlock=immediate"
	if c.probe {
		query = "mode=ro&immutable=1"
	}

	uri := (&url.URL{Scheme: "file", Path: c.path, RawQuery: query}).String()

	connection, err := c.Driver().Open(uri)
	if err != nil {
		return nil, err
	}

	executor, ok := connection.(driver.ExecerContext)
	if !ok {
		_ = connection.Close()
		return nil, ErrUnsafeStore
	}

	if _, err = executor.ExecContext(ctx, connectionSQL, nil); err != nil {
		_ = connection.Close()
		return nil, err
	}

	return connection, nil
}
