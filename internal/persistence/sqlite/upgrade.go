// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"go.trippwill.dev/wake/internal/application"
)

const (
	ErrUpgradeConfirmation Error = "upgrade confirmation required; stop Wake and run wake upgrade with the same configuration"
	ErrUpgradePlanChanged  Error = "upgrade plan changed; run wake upgrade again"
	ErrUpgradeRecovery     Error = "upgrade recovery required; use the matching Wake build and original stores or restore the paired backup"
	ErrUpgradeNotes        Error = "retained-history rule update is missing release notes"
	ErrUpgradePair         Error = "upgrade requires the matching warehouse and access store; recover the pair before upgrading"
)

// Migration SQL is embedded by the build that introduces a new format. The v1
// release has no incoming migrations.
type migration struct{ from, to, sql string }

type upgradeBaseline struct {
	formats, schemas [2]string
	migrations       [2][]migration
	rule             application.FinancialRule
	// Each entry describes the change introduced at that rule version.
	notes map[int64]string
}

func releaseBaseline() upgradeBaseline {
	return upgradeBaseline{
		formats: storeVersions,
		schemas: [2]string{schemaSQL, connectionsSQL},
		rule:    application.RetainedClaimsRule,
	}
}

type UpgradeStore struct {
	Kind    string `json:"kind"`
	Path    string `json:"path"`
	ID      string `json:"store_id,omitempty"`
	Current string `json:"current_format,omitempty"`
	Target  string `json:"target_format"`
}

type UpgradeStep struct {
	Store  string `json:"store"`
	From   string `json:"from"`
	To     string `json:"to"`
	Digest string `json:"sql_sha256"`
}

type RuleReleaseNote struct {
	Version int64  `json:"version"`
	Text    string `json:"text"`
}

type UpgradePlan struct {
	ID              string            `json:"plan_id,omitempty"`
	Stores          [2]UpgradeStore   `json:"stores"`
	Steps           []UpgradeStep     `json:"steps"`
	AppliedRule     int64             `json:"applied_rule,omitempty"`
	TargetRule      string            `json:"target_rule,omitempty"`
	Rebuild         string            `json:"rebuild,omitempty"`
	AffectedViews   []string          `json:"affected_views,omitempty"`
	ReleaseNotes    []RuleReleaseNote `json:"release_notes,omitempty"`
	BackupPath      string            `json:"backup_path,omitempty"`
	RecoveryPending bool              `json:"recovery_pending,omitempty"`
}

func (p UpgradePlan) needed() bool { return len(p.Steps) != 0 || p.Rebuild == "required" }

type upgradeOptions struct {
	preview bool
	backup  bool
	confirm string
	result  *UpgradePlan
}

// Backup creates a verified archive of a stopped, supported pair.
func Backup(ctx context.Context, profile Profile, logger *slog.Logger) (string, error) {
	var plan UpgradePlan

	_, err := openStore(ctx, profile, logger, releaseBaseline(), upgradeOptions{backup: true, result: &plan})

	return plan.BackupPath, err
}

// Upgrade previews or applies an exact offline upgrade plan.
func Upgrade(ctx context.Context, profile Profile, logger *slog.Logger, confirm string) (UpgradePlan, error) {
	var plan UpgradePlan

	profile.UnattendedUpgrade = false

	store, err := openStore(ctx, profile, logger, releaseBaseline(), upgradeOptions{
		preview: confirm == "", confirm: confirm, result: &plan,
	})
	if store != nil {
		err = store.Close()
	}

	return plan, err
}

func ruleVersion(rule application.FinancialRule) (int64, error) {
	_, suffix, _ := strings.Cut(string(rule), "/")

	version, err := strconv.ParseInt(suffix, 10, 64)
	if err != nil || version < 1 {
		return 0, ErrHistoryVersion
	}

	return version, nil
}

func digest(data []byte) string {
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

func (b upgradeBaseline) chain(i int, from string) ([]migration, error) {
	var steps []migration

	for from != b.formats[i] {
		var step migration
		for _, candidate := range b.migrations[i] {
			if candidate.from == from {
				if step.from != "" {
					return nil, ErrUnsupportedSchema
				}

				step = candidate
			}
		}

		if step.from == "" || !nextStoreFormat(step.from, step.to) || strings.TrimSpace(step.sql) == "" {
			return nil, ErrUnsupportedSchema
		}

		steps = append(steps, step)
		from = step.to
	}

	return steps, nil
}

func nextStoreFormat(from, to string) bool {
	fromFamily, fromSuffix, _ := strings.Cut(from, "/")
	toFamily, toSuffix, _ := strings.Cut(to, "/")
	previous, fromErr := strconv.ParseInt(fromSuffix, 10, 64)
	next, toErr := strconv.ParseInt(toSuffix, 10, 64)

	return fromFamily != "" && fromFamily == toFamily && fromErr == nil && toErr == nil &&
		previous > 0 && next > previous && next-previous == 1
}

type historyVersion struct{ applied, target int64 }

func (b upgradeBaseline) plan(
	paths [2]string,
	metadata [2]storeMetadata,
	present [2]bool,
	history historyVersion,
) (UpgradePlan, [2][]migration, error) {
	plan := UpgradePlan{Steps: []UpgradeStep{}}

	var chains [2][]migration

	for i, path := range paths {
		m := metadata[i]
		plan.Stores[i] = UpgradeStore{
			Kind:    storeKinds[i],
			Path:    path,
			ID:      m.id,
			Current: m.version,
			Target:  b.formats[i],
		}
		if !present[i] {
			continue
		}

		chain, err := b.chain(i, m.version)
		if err != nil {
			return plan, chains, err
		}

		chains[i] = chain
		for _, step := range chain {
			plan.Steps = append(
				plan.Steps,
				UpgradeStep{storeKinds[i], step.from, step.to, digest([]byte(step.sql))},
			)
		}
	}

	target, err := ruleVersion(b.rule)
	if err != nil {
		return plan, chains, err
	}

	if present[0] {
		if history.applied < 1 || history.applied > target || history.target != 0 && history.target != target {
			return plan, chains, ErrHistoryVersion
		}

		plan.AppliedRule, plan.TargetRule = history.applied, string(b.rule)
		if history.applied < target {
			plan.Rebuild = "required"
			if history.target == target {
				plan.Rebuild = "approved"
			}

			plan.AffectedViews = []string{
				"activity",
				"instruments",
				"listings",
				"positions",
				"financial snapshots and publication events",
			}
			for version := history.applied + 1; ; version++ {
				note := strings.TrimSpace(b.notes[version])
				if note == "" {
					return plan, chains, fmt.Errorf("%w: %d", ErrUpgradeNotes, version)
				}

				plan.ReleaseNotes = append(plan.ReleaseNotes, RuleReleaseNote{version, note})
				if version == target {
					break
				}
			}
		}
	}

	if len(plan.Steps) != 0 && history.target != 0 {
		return plan, chains, ErrHistoryVersion
	}

	if plan.needed() {
		plan.BackupPath = paths[0] + ".upgrade-backup-<UTC>-<random>.tar"

		encoded, err := json.Marshal(plan)
		if err != nil {
			return plan, chains, err
		}

		plan.ID = digest(encoded)
	}

	return plan, chains, nil
}

func applyMigrations(ctx context.Context, tx *sql.Tx, chains [2][]migration) error {
	for i, steps := range chains {
		for _, step := range steps {
			if _, err := tx.ExecContext(ctx, step.sql); err != nil {
				return fmt.Errorf("migrate %s to %s: %w", storeKinds[i], step.to, err)
			}

			result, err := tx.ExecContext(
				ctx,
				profileStatement("AdvanceFormat", storeSchemas[i]),
				step.to,
				step.from,
			)
			if err != nil {
				return err
			}

			affected, err := result.RowsAffected()
			if err != nil {
				return err
			}

			if affected != 1 {
				return ErrCorrupt
			}
		}
	}

	return nil
}

// UpgradeIntentSuffix is part of the warehouse's private file set.
const UpgradeIntentSuffix = ".upgrade-intent"

type upgradeFile struct {
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
}

type upgradeIntent struct {
	Plan         UpgradePlan    `json:"plan"`
	Files        [2]upgradeFile `json:"files"`
	BackupDigest string         `json:"backup_sha256"`
	ArchivePath  string         `json:"archive_path,omitempty"`
	Predecessor  upgradeArchive `json:"predecessor,omitempty"`
}

func (i upgradeIntent) archivePath() string {
	if i.ArchivePath != "" {
		return i.ArchivePath
	}

	return i.Plan.BackupPath
}

func readUpgradeIntent(path string) (upgradeIntent, error) {
	var intent upgradeIntent

	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return intent, nil
	}

	if err != nil || !info.Mode().IsRegular() || !privateOwned(info) || !singleLink(info) ||
		info.Size() > 1<<20 {
		return intent, ErrUpgradeRecovery
	}

	file, err := openUpgradeIntent(path, info)
	if err != nil {
		return intent, err
	}

	defer func() { _ = file.Close() }()

	data, err := io.ReadAll(io.LimitReader(file, 1<<20))
	if err != nil || json.Unmarshal(data, &intent) != nil || intent.Plan.ID == "" {
		return upgradeIntent{}, ErrUpgradeRecovery
	}

	return intent, nil
}

func writeUpgradeIntent(path string, intent upgradeIntent) error {
	return writePrivateRecord(path, intent)
}

func readPrivateRecord(path string, record any) error {
	f, err := openBackup(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	data, err := io.ReadAll(io.LimitReader(f, 1<<20|1))
	if err != nil {
		return err
	}

	if len(data) > 1<<20 || json.Unmarshal(data, record) != nil {
		return ErrCorrupt
	}

	return nil
}

func writePrivateRecord(path string, record any) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".wake-upgrade-")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(file.Name()) }()

	err = json.NewEncoder(file).Encode(record)
	if err == nil {
		err = file.Sync()
	}

	closeErr := file.Close()

	if err != nil {
		return err
	}

	if closeErr != nil {
		return closeErr
	}

	if err = os.Rename(file.Name(), path); err != nil {
		return err
	}

	return syncUpgradeDir(path)
}

func syncUpgradeDir(path string) error {
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()

	return dir.Sync()
}

func (b upgradeBaseline) checkIntent(paths [2]string, files [2]upgradeFile, intent upgradeIntent) error {
	if intent.Files != files {
		return ErrUpgradeRecovery
	}

	var metadata [2]storeMetadata
	for i, store := range intent.Plan.Stores {
		if store.Path != paths[i] || store.ID == "" {
			return ErrUpgradeRecovery
		}

		metadata[i] = storeMetadata{kind: storeKinds[i], id: store.ID, version: store.Current}
	}

	state := historyVersion{applied: intent.Plan.AppliedRule}

	plan, _, err := b.plan(paths, metadata, [2]bool{true, true}, state)
	if err != nil || !intent.matchesPlan(paths[0], plan) {
		return ErrUpgradeRecovery
	}

	canonical := intent.Plan
	canonical.ID = ""
	encoded, err := json.Marshal(canonical)
	if err != nil || digest(encoded) != intent.Plan.ID {
		return ErrUpgradeRecovery
	}

	return nil
}

func (i upgradeIntent) matchesPlan(warehouse string, plan UpgradePlan) bool {
	if plan.ID == "" {
		return false
	}

	if i.ArchivePath != "" {
		return validUpgradeArchive(warehouse, i.ArchivePath) && plan.ID == i.Plan.ID &&
			(i.Predecessor == (upgradeArchive{}) ||
				i.Predecessor.Warehouse == plan.Stores[0].ID &&
					validUpgradeArchive(warehouse, i.Predecessor.Path) && validBackupDigest(i.Predecessor.Digest))
	}

	plan.ID = ""
	plan.BackupPath = warehouse + upgradeBackupSuffix
	data, err := json.Marshal(plan)

	return err == nil && digest(data) == i.Plan.ID
}

func readUpgradeState(
	ctx context.Context,
	db *sql.DB,
) (metadata [2]storeMetadata, history historyVersion, accessID string, err error) {
	for i, schema := range storeSchemas {
		m := &metadata[i]
		if err = db.QueryRowContext(ctx, profileStatement("Metadata", schema)).
			Scan(&m.kind, &m.version, &m.id); err != nil {
			return
		}
	}

	if err = db.QueryRowContext(ctx, profileStatement("Profile", "")).Scan(&accessID); err != nil {
		return
	}

	err = db.QueryRowContext(ctx, profileStatement("HistoryVersion", "main")).
		Scan(&history.applied, &history.target)

	return
}

func (b upgradeBaseline) recoveredMigrations(
	paths [2]string,
	metadata [2]storeMetadata,
	history historyVersion,
	accessID string,
	intent upgradeIntent,
) ([2][]migration, error) {
	var noMigrations [2][]migration
	if accessID != metadata[1].id {
		return noMigrations, ErrUpgradeRecovery
	}

	committed := true
	for i, store := range intent.Plan.Stores {
		if store.ID != metadata[i].id || store.Kind != metadata[i].kind {
			return noMigrations, ErrUpgradeRecovery
		}

		committed = committed && metadata[i].version == store.Target
	}

	plan, chains, err := b.plan(paths, metadata, [2]bool{true, true}, history)
	if err != nil {
		return noMigrations, ErrUpgradeRecovery
	}

	if committed && !plan.needed() {
		return noMigrations, nil
	}

	if !intent.matchesPlan(paths[0], plan) {
		return noMigrations, ErrUpgradeRecovery
	}

	return chains, nil
}

func (s *Store) recoverOrApplyUpgrade(
	ctx context.Context,
	paths [2]string,
	baseline upgradeBaseline,
	intent upgradeIntent,
	path string,
) error {
	metadata, history, accessID, err := readUpgradeState(ctx, s.db)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUpgradeRecovery, err)
	}

	chains, err := baseline.recoveredMigrations(paths, metadata, history, accessID, intent)
	if err != nil {
		return err
	}

	if err = s.initialize(ctx, [2]bool{true, true}, metadata, false, baseline, chains); err != nil {
		return fmt.Errorf("%w: %w", ErrUpgradeRecovery, err)
	}

	if intent.ArchivePath != "" {
		if err = s.retainUpgradeArchive(ctx, paths[0], intent); err != nil {
			return err
		}
	}

	if err = os.Remove(path); err != nil {
		return err
	}

	return syncUpgradeDir(path)
}

func verifyFormats(
	ctx context.Context,
	tx *sql.Tx,
	baseline upgradeBaseline,
	metadata [2]storeMetadata,
) error {
	var err error
	for i, schema := range storeSchemas {
		var kind, version, id string
		if err = tx.QueryRowContext(ctx, profileStatement("Metadata", schema)).
			Scan(&kind, &version, &id); err != nil {
			return err
		}

		if kind != storeKinds[i] || version != baseline.formats[i] || id != metadata[i].id {
			return ErrCorrupt
		}

		var count int
		if err = tx.QueryRowContext(ctx, profileStatement("ObjectCount", schema)).Scan(&count); err != nil {
			return err
		}

		if count != strings.Count(baseline.schemas[i], "CREATE TABLE") {
			return ErrCorrupt
		}
	}

	var accessID string
	if err = tx.QueryRowContext(ctx, profileStatement("Profile", "")).Scan(&accessID); err != nil {
		return err
	}

	if accessID != metadata[1].id {
		return ErrAssociation
	}

	return nil
}

func checkUpgradeJournals(path string) error {
	paths, err := storeSidecars(path)
	if err != nil {
		return err
	}

	for _, sidecar := range paths {
		info, err := os.Lstat(sidecar)
		if os.IsNotExist(err) {
			continue
		}

		if err != nil || sidecar != path+"-journal" || info.Size() != 0 {
			return ErrUpgradeRecovery
		}
	}

	return nil
}
