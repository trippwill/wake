// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

// Package persistence implements Wake's private two-store SQLite boundary.
package persistence

import (
	"context"
	"crypto/rand"
	"database/sql"
	"database/sql/driver"
	_ "embed"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/logging"
	"go.trippwill.dev/wake/internal/persistence/sqlite/sqlc"
	"go.trippwill.dev/wake/provider"
)

type Error string

func (e Error) Error() string { return string(e) }

const (
	ErrUnsupportedSchema Error = "unsupported store format; use a compatible Wake build or restore a matching backup; for an unsupported development store, stop Wake and preview wake reset warehouse or wake reset credentials"
	ErrUnsafeStore       Error = "unsafe or already owned store path"
	ErrCorrupt           Error = "invalid retained store data"
	ErrAssociation       Error = "store associations do not match; restore matching warehouse/access stores or explicitly recreate access"
	schemaVersion              = "wake-warehouse/1"
	accessSchemaVersion        = "wake-access/1"
)

// Profile selects the warehouse and independent encrypted access storage.
type Profile struct {
	WarehousePath, ConnectionsPath string
	Registrations                  []provider.Registration
	UnattendedUpgrade              bool
}

func (p Profile) paths() [2]string {
	return [2]string{p.WarehousePath, p.ConnectionsPath}
}

//go:embed schema.sql
var schemaSQL string

//go:embed activity_date_indexes.sql
var activityDateIndexesSQL string

//go:embed connections_schema.sql
var connectionsSQL string

//go:embed connection.sql
var connectionSQL string

//go:embed activity_read_connection.sql
var activityReadConnectionSQL string

//go:embed profile.sql
var profileSQL string

func profileStatement(name, schema string) string {
	start := strings.Index(profileSQL, "-- name: "+name+"\n")
	if start < 0 {
		panic("unknown profile statement")
	}

	statement := profileSQL[start+len("-- name: "+name+"\n"):]
	if end := strings.Index(statement, "-- name:"); end >= 0 {
		statement = statement[:end]
	}

	return strings.ReplaceAll(statement, "{{schema}}", schema)
}

type Store struct {
	historyRule            application.FinancialRule
	historyUpdating        atomic.Bool
	registrations          []provider.Registration
	db                     *sql.DB
	activityDB             *sql.DB
	activityQuery          *sql.Stmt
	queries                *sqlc.Queries
	owners                 [2]*os.File
	logger                 *slog.Logger
	closeOnce              sync.Once
	closeError             error
	publicationMu          sync.Mutex
	publicationSubscribers map[chan struct{}]struct{}
}

type storeMetadata struct{ kind, version, id string }

// WarehouseSize reads the owned database file, excluding access storage and journals.
func (s *Store) WarehouseSize() (int64, error) {
	if s.owners[0] == nil {
		return 0, os.ErrInvalid
	}

	info, err := s.owners[0].Stat()
	if err != nil {
		return 0, err
	}

	return info.Size(), nil
}

var (
	storeKinds    = [2]string{"warehouse", "connections"}
	storeSchemas  = [2]string{"main", "access"}
	storeVersions = [2]string{schemaVersion, accessSchemaVersion}
)

func Open(ctx context.Context, profile Profile, logger *slog.Logger) (*Store, error) {
	return openStore(ctx, profile, logger, releaseBaseline(), upgradeOptions{})
}

func openStore(
	ctx context.Context, profile Profile, logger *slog.Logger, baseline upgradeBaseline, upgrade upgradeOptions,
) (result *Store, resultErr error) {
	started := time.Now()

	if err := registerCollations(); err != nil {
		return nil, err
	}

	logger = logging.Component(logger, "storage")
	logger.InfoContext(ctx, "validating storage profile", "event", "storage_opening", "stage", "preflight")

	stage, storeKind := "preflight", "profile"
	defer func() {
		if resultErr != nil {
			logger.ErrorContext(
				ctx,
				"storage profile refused",
				"event",
				"storage_open_failed",
				"stage", stage, "store", storeKind,
				"code",
				storageCode(resultErr),
				"outcome",
				"failed",
				"elapsed",
				time.Since(started),
			)
		}
	}()

	for _, dir := range []string{filepath.Dir(profile.WarehousePath), filepath.Dir(profile.ConnectionsPath)} {
		if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir {
			return nil, ErrUnsafeStore
		}

		info, err := os.Lstat(dir)
		if upgrade.preview && os.IsNotExist(err) {
			continue
		}

		if err != nil || !info.IsDir() || !privateOwned(info) {
			return nil, ErrUnsafeStore
		}
	}

	paths := profile.paths()
	for i, path := range paths {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return nil, ErrUnsafeStore
		}

		for _, other := range paths[:i] {
			for _, suffix := range []string{"", "-journal", "-wal", "-shm", UpgradeIntentSuffix, upgradeBackupSuffix} {
				if path == other+suffix || other == path+suffix {
					return nil, ErrUnsafeStore
				}
			}

			if strings.HasPrefix(path, other+"-mj") || strings.HasPrefix(other, path+"-mj") {
				return nil, ErrUnsafeStore
			}
		}
	}

	s := &Store{logger: logger, registrations: slices.Clone(profile.Registrations), historyRule: baseline.rule}
	defer func() {
		if result == nil {
			_ = s.Close()
		}
	}()

	intentPath := paths[0] + UpgradeIntentSuffix

	intent, err := readUpgradeIntent(intentPath)
	if err != nil {
		return nil, err
	}

	var (
		history  historyVersion
		metadata [2]storeMetadata
		present  [2]bool
		previous string
	)
	for i, path := range paths {
		storeKind = storeKinds[i]

		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return nil, ErrUnsafeStore
		}

		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			sidecars, e := storeSidecars(path)
			if upgrade.preview && os.IsNotExist(e) {
				continue
			}

			if e != nil {
				return nil, e
			}

			for _, sidecar := range sidecars {
				if _, e := os.Lstat(sidecar); !os.IsNotExist(e) {
					return nil, ErrUnsupportedSchema
				}
			}

			continue
		}

		if err != nil || !info.Mode().IsRegular() {
			return nil, ErrUnsafeStore
		}

		s.owners[i], err = lockStoreFile(path, false)
		if err != nil {
			return nil, err
		}

		present[i] = true
		// Unsupported files must be rejected before SQLite can recover any journal.
		for _, suffix := range []string{"-wal", "-shm"} {
			if _, e := os.Lstat(path + suffix); e == nil {
				return nil, ErrUnsupportedSchema
			} else if !os.IsNotExist(e) {
				return nil, e
			}
		}

		if intent.Plan.ID != "" {
			continue
		}

		probe := sql.OpenDB(&connector{paths: [2]string{path}, owners: [2]*os.File{s.owners[i]}, probe: true})

		var count int

		err = probe.QueryRowContext(ctx, profileStatement("ObjectCount", "main")).Scan(&count)
		if err == nil && count > 0 {
			m := &metadata[i]
			err = probe.QueryRowContext(ctx, profileStatement("Metadata", "main")).
				Scan(&m.kind, &m.version, &m.id)
			expected := strings.Count(baseline.schemas[i], "CREATE TABLE")
			if err == nil && (m.kind != storeKinds[i] || m.id == "" ||
				m.version == baseline.formats[i] && count != expected) {
				err = ErrUnsupportedSchema
			}

			if err == nil && i == 0 {
				err = probe.QueryRowContext(ctx, profileStatement("Profile", "main")).Scan(&previous)
				if err == nil {
					err = probe.QueryRowContext(ctx, profileStatement("HistoryVersion", "main")).
						Scan(&history.applied, &history.target)
				}
			}
		} else if err == nil {
			if info.Size() != 0 {
				err = ErrUnsupportedSchema
			}

			if j, e := os.Lstat(path + "-journal"); e == nil && j.Size() > 0 {
				err = ErrUnsupportedSchema
			}

			present[i] = false
		}

		_ = probe.Close()

		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}

			return nil, ErrUnsupportedSchema
		}

		logger.DebugContext(
			ctx,
			"store preflight complete",
			"event",
			"storage_validated",
			"stage",
			"preflight",
			"store",
			storeKinds[i],
		)
	}

	stage, storeKind = "upgrade_plan", "profile"

	// Read again under the store locks to reject an intent that changed while
	// ownership was being acquired.
	lockedIntent, err := readUpgradeIntent(intentPath)
	if err != nil {
		return nil, err
	}

	if lockedIntent.Plan.ID != intent.Plan.ID {
		return nil, ErrUpgradeRecovery
	}

	var (
		plan   UpgradePlan
		chains [2][]migration
	)
	if intent.Plan.ID != "" {
		files, e := upgradeFiles(s.owners)
		if e != nil {
			return nil, e
		}

		if e = baseline.checkIntent(paths, files, lockedIntent); e != nil {
			return nil, e
		}

		intent = lockedIntent
		plan = intent.Plan
		plan.RecoveryPending = true
	} else {
		plan, chains, err = baseline.plan(paths, metadata, present, history)
		if err != nil {
			return nil, err
		}

		if plan.needed() && (!present[0] || !present[1] || metadata[1].id != previous) {
			return nil, ErrUpgradePair
		}

		if plan.needed() {
			for _, path := range paths {
				if err = checkUpgradeJournals(path); err != nil {
					return nil, err
				}
			}
		}
	}

	if upgrade.result != nil {
		*upgrade.result = plan
	}

	if upgrade.preview {
		return nil, nil
	}

	if upgrade.confirm != "" && upgrade.confirm != plan.ID {
		return nil, ErrUpgradePlanChanged
	}

	if intent.Plan.ID == "" && plan.Rebuild == "required" && upgrade.confirm == "" &&
		!profile.UnattendedUpgrade {
		return nil, ErrUpgradeConfirmation
	}

	if intent.Plan.ID == "" && plan.needed() {
		files, e := upgradeFiles(s.owners)
		if e != nil {
			return nil, e
		}
		// Log the same plan and notes as the preview before any store writes.
		logger.InfoContext(ctx, "applying storage upgrade", "event", "daemon_upgrade_plan", "plan", plan)

		stage = "upgrade_backup"

		backupDigest, backupErr := createUpgradeBackup(ctx, plan, s.owners)
		if backupErr != nil {
			return nil, fmt.Errorf("%w: %s: %w", ErrUpgradeBackup, plan.BackupPath, backupErr)
		}

		intent = upgradeIntent{Plan: plan, Files: files, BackupDigest: backupDigest}
		if err = writeUpgradeIntent(intentPath, intent); err != nil {
			return nil, err
		}
	}

	if plan.RecoveryPending {
		stage = "upgrade_backup"

		if err = verifyUpgradeBackup(ctx, plan.BackupPath, intent.BackupDigest); err != nil {
			return nil, fmt.Errorf("%w: %s: %w", ErrUpgradeBackup, plan.BackupPath, err)
		}
	}

	stage, storeKind = "ownership", "profile"
	// Do not create missing files until every existing store has passed preflight.
	for i, path := range paths {
		if s.owners[i] == nil {
			var err error

			s.owners[i], err = claimFile(path)
			storeKind = storeKinds[i]

			if err != nil {
				return nil, err
			}
		}
	}

	s.db = sql.OpenDB(&connector{paths: paths, owners: s.owners})
	s.db.SetMaxOpenConns(1)
	s.db.SetMaxIdleConns(1)
	s.queries = sqlc.New(s.db)

	stage, storeKind = "initialize", "profile"

	stopProgress := s.progress(ctx, "storage_open_progress", "open")
	defer stopProgress()

	if plan.RecoveryPending {
		logger.InfoContext(
			ctx,
			"recovering approved storage upgrade",
			"event",
			"daemon_upgrade_recovery",
			"plan_id",
			plan.ID,
		)
	}

	if intent.Plan.ID != "" {
		if err = s.recoverOrApplyUpgrade(ctx, paths, baseline, intent, intentPath); err != nil {
			return nil, err
		}
	} else {
		accessChanged := present[0] && present[1] && metadata[1].id != previous
		if err = s.initialize(ctx, present, metadata, accessChanged, baseline, chains); err != nil {
			return nil, err
		}
	}

	if s.HistoryUpdating() && (plan.Rebuild == "approved" || plan.RecoveryPending) {
		logger.InfoContext(
			ctx,
			"continuing approved history rebuild",
			"event",
			"daemon_history_upgrade",
			"target_rule",
			baseline.rule,
		)
	}

	if upgrade.confirm != "" {
		return s, nil
	}

	s.activityDB = sql.OpenDB(&connector{
		paths: [2]string{paths[0]}, owners: [2]*os.File{s.owners[0]}, activityRead: true,
	})
	s.activityDB.SetMaxOpenConns(1)
	s.activityDB.SetMaxIdleConns(1)

	s.activityQuery, err = s.activityDB.PrepareContext(ctx, adminActivitySQL)
	if err != nil {
		return nil, err
	}

	logger.InfoContext(
		ctx,
		"storage profile opened",
		"event",
		"storage_opened",
		"outcome",
		"succeeded",
		"elapsed",
		time.Since(started),
	)

	return s, nil
}

func storageCode(err error) string {
	switch {
	case errors.Is(err, ErrUnsafeStore):
		return "unsafe_or_owned"
	case errors.Is(err, ErrUpgradeConfirmation):
		return "upgrade_confirmation_required"
	case errors.Is(err, ErrUpgradeRecovery):
		return "upgrade_recovery_required"
	case errors.Is(err, ErrUpgradeBackup):
		return "upgrade_backup_failed"
	case errors.Is(err, ErrUpgradeNotes):
		return "upgrade_release_notes_missing"
	case errors.Is(err, ErrHistoryVersion):
		return "unsupported_history_version"
	case errors.Is(err, ErrUnsupportedSchema):
		return "unsupported_format"
	case errors.Is(err, ErrAssociation):
		return "association_mismatch"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline"
	default:
		return "storage_failure"
	}
}

func (s *Store) Close() error {
	s.closeOnce.Do(func() {
		if s.activityQuery != nil {
			s.closeError = s.activityQuery.Close()
		}

		if s.activityDB != nil {
			s.closeError = errors.Join(s.closeError, s.activityDB.Close())
		}

		if s.db != nil {
			s.closeError = errors.Join(s.closeError, s.db.Close())
		}

		for i := len(s.owners) - 1; i >= 0; i-- {
			if s.owners[i] != nil {
				s.closeError = errors.Join(s.closeError, s.owners[i].Close())
			}
		}

		if s.logger != nil && s.db != nil {
			s.logger.Info(
				"storage closed",
				"event",
				"storage_closed",
				"outcome",
				map[bool]string{true: "failed", false: "succeeded"}[s.closeError != nil],
			)
		}
	})

	return s.closeError
}

func (s *Store) initialize(
	ctx context.Context,
	present [2]bool,
	metadata [2]storeMetadata,
	accessChanged bool,
	baseline upgradeBaseline,
	chains [2][]migration,
) error {
	started := time.Now()

	recovering := present[0] && (!present[1] || accessChanged)
	if recovering {
		s.logger.WarnContext(
			ctx,
			"storage recovery required; reconstructing with upstream work paused",
			"event",
			"storage_recovery_started",
			"stage",
			"reconstruct",
			"code",
			"recovery_required",
		)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}

	defer func() { _ = tx.Rollback() }()

	for i, exists := range present {
		if exists {
			continue
		}

		if _, err = tx.ExecContext(ctx, baseline.schemas[i]); err != nil {
			return err
		}

		metadata[i] = storeMetadata{kind: storeKinds[i], version: baseline.formats[i], id: rand.Text()}

		m := metadata[i]
		if _, err = tx.ExecContext(
			ctx,
			profileStatement("PutMetadata", storeSchemas[i]),
			m.kind,
			m.version,
			m.id,
		); err != nil {
			return err
		}
	}

	if len(chains[0])+len(chains[1]) != 0 {
		if err = applyMigrations(ctx, tx, chains); err != nil {
			return err
		}

		if err = verifyFormats(ctx, tx, baseline, metadata); err != nil {
			return err
		}
	}

	if err = s.prepareHistoryUpdate(ctx, s.queries.WithTx(tx), !present[0]); err != nil {
		return err
	}

	switch {
	case !present[0] && present[1]:
		if _, err = tx.ExecContext(
			ctx,
			profileStatement("InitializeRetainedAccessProfile", ""),
			metadata[1].id,
		); err != nil {
			return err
		}
	case !present[0]:
		if _, err = tx.ExecContext(ctx, profileStatement("InitializeProfile", ""), metadata[1].id); err != nil {
			return err
		}
	case recovering:
		review := 0
		if !present[1] || accessChanged {
			review = 1
		}

		if _, err = tx.ExecContext(
			ctx,
			profileStatement("RecoverProfile", ""),
			metadata[1].id,
			review,
		); err != nil {
			return err
		}
	}

	if _, err = tx.ExecContext(ctx, profileStatement("RetainConnections", "")); err != nil {
		return err
	}

	if _, err = tx.ExecContext(ctx, profileStatement("InterruptAttempts", "")); err != nil {
		return err
	}

	var invalid int
	if err = tx.QueryRowContext(ctx, profileStatement("AssociationCheck", "")).Scan(&invalid); err != nil {
		return err
	}

	if invalid != 0 {
		return ErrAssociation
	}

	for _, schema := range storeSchemas {
		var integrity string
		if err = tx.QueryRowContext(ctx, profileStatement("Integrity", schema)).Scan(&integrity); err != nil {
			return err
		}

		if integrity != "ok" {
			return ErrCorrupt
		}

		rows, e := tx.QueryContext(ctx, profileStatement("ForeignCheck", schema))
		if e != nil {
			return e
		}

		invalid := rows.Next()
		e = rows.Err()
		_ = rows.Close()

		if e != nil {
			return e
		}

		if invalid {
			return ErrCorrupt
		}
	}

	if _, err = tx.ExecContext(ctx, activityDateIndexesSQL); err != nil {
		return err
	}

	if _, err = tx.ExecContext(ctx, activitySearchIndexSQL); err != nil {
		return err
	}

	if err = s.commit(ctx, tx); err != nil {
		return err
	}

	if !present[0] && present[1] {
		s.logger.InfoContext(
			ctx,
			"fresh warehouse created with retained access; account discovery requires operator resume",
			"event",
			"storage_warehouse_recreated",
			"stage",
			"reconstruct",
			"outcome",
			"paused",
		)
	}

	for i, exists := range present {
		if !exists {
			s.logger.InfoContext(
				ctx,
				"store initialized",
				"event",
				"storage_initialized",
				"store",
				storeKinds[i],
				"outcome",
				"succeeded",
			)
		}
	}

	if recovering {
		s.logger.InfoContext(
			ctx,
			"execution obligations reconstructed; operator review required",
			"event",
			"storage_recovery_completed",
			"stage",
			"reconstruct",
			"outcome",
			"paused",
			"elapsed",
			time.Since(started),
		)
	}

	profile, err := s.queries.Profile(ctx)
	if err == nil && profile.Paused != 0 {
		s.logger.WarnContext(
			ctx,
			"upstream work paused; review access and resume in admin",
			"event",
			"storage_paused",
			"code",
			"operator_review_required",
		)
	}

	return err
}

// Every physical writer connection attaches the same owned files and establishes
// rollback-journal safety before it can participate in a cross-store transaction.
type connector struct {
	paths        [2]string
	owners       [2]*os.File
	probe        bool
	activityRead bool
}

func (c *connector) Driver() driver.Driver { return sqliteDriver }
func (c *connector) Connect(ctx context.Context) (driver.Conn, error) {
	for i, owner := range c.owners {
		if owner != nil {
			if err := checkFile(c.paths[i], owner); err != nil {
				return nil, err
			}
		}
	}

	query := "mode=rw&_txlock=immediate"
	switch {
	case c.probe:
		query = "mode=ro&immutable=1"
	case c.activityRead:
		query = "mode=ro"
	}

	connection, err := c.Driver().Open((&url.URL{Scheme: "file", Path: c.paths[0], RawQuery: query}).String())
	if err != nil {
		return nil, err
	}

	executor, ok := connection.(driver.ExecerContext)
	if !ok {
		_ = connection.Close()
		return nil, ErrUnsafeStore
	}

	fail := func(err error) (driver.Conn, error) { _ = connection.Close(); return nil, err }

	if c.probe {
		return connection, nil
	}

	if c.activityRead {
		if _, err = executor.ExecContext(ctx, activityReadConnectionSQL, nil); err != nil {
			return fail(err)
		}

		return connection, nil
	}

	for i, name := range []string{"AttachAccess"} {
		uri := (&url.URL{Scheme: "file", Path: c.paths[i+1], RawQuery: "mode=rw"}).String()
		if _, err = executor.ExecContext(
			ctx,
			profileStatement(name, ""),
			[]driver.NamedValue{{Ordinal: 1, Value: uri}},
		); err != nil {
			return fail(err)
		}
	}

	if _, err = executor.ExecContext(ctx, connectionSQL, nil); err != nil {
		return fail(err)
	}

	querier, ok := connection.(driver.QueryerContext)
	if !ok {
		return fail(ErrUnsafeStore)
	}

	for _, schema := range storeSchemas {
		rows, e := querier.QueryContext(ctx, profileStatement("Journal", schema), nil)
		if e != nil {
			return fail(e)
		}

		values := make([]driver.Value, 1)
		e = rows.Next(values)
		_ = rows.Close()

		if e != nil || values[0] != "delete" {
			return fail(ErrUnsafeStore)
		}

		if _, e = executor.ExecContext(ctx, profileStatement("Sync", schema), nil); e != nil {
			return fail(e)
		}

		rows, e = querier.QueryContext(ctx, profileStatement("SyncCheck", schema), nil)
		if e != nil {
			return fail(e)
		}

		e = rows.Next(values)
		_ = rows.Close()

		if e != nil || values[0] != int64(3) {
			return fail(ErrUnsafeStore)
		}
	}

	return connection, nil
}

func (s *Store) progress(ctx context.Context, event, stage string) func() {
	return logging.Progress(ctx, s.logger.With("stage", stage), event, 30*time.Second)
}
