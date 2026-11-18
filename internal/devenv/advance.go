// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package devenv

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/application/calculation"
	"go.trippwill.dev/wake/internal/daemon"
	persistence "go.trippwill.dev/wake/internal/persistence/sqlite"
)

// Advance changes the disposable installation only after its daemon has joined.
func Advance(ctx context.Context, dir, stage string) (result error) {
	cfg := configuration(dir)
	profile := persistence.Profile{
		WarehousePath:   cfg.Storage.WarehousePath,
		ConnectionsPath: cfg.Storage.ConnectionsPath,
		SyncPath:        cfg.Storage.SyncPath,
	}
	switch stage {
	case "acquisition", "source-conflict", "source-resolution", "identity-merge":
		return advanceReport(ctx, cfg, profile, stage)
	case "restart":
		return nil
	case "empty-warehouse":
		return os.Rename(cfg.Storage.WarehousePath, cfg.Storage.WarehousePath+".replaced")
	case "access-recovery":
		archive := filepath.Join(dir, "installation.tar")
		if err := persistence.Backup(ctx, profile, archive); err != nil {
			return err
		}

		return os.Remove(cfg.Storage.ConnectionsPath)
	case "financial-restore":
		archive := filepath.Join(dir, "history.tar")
		if _, err := daemon.SnapshotWarehouse(ctx, cfg, "", archive); err != nil {
			return err
		}

		if err := os.Rename(cfg.Storage.WarehousePath, cfg.Storage.WarehousePath+".original"); err != nil {
			return err
		}

		_, err := daemon.RestoreStorage(ctx, cfg, "", archive, false)

		return err
	case "replacement":
		archive := filepath.Join(dir, "installation.tar")
		_, err := daemon.ReplaceStorage(ctx, cfg, "", archive)

		return err
	case "correction":
		store, err := persistence.Open(ctx, profile, nil)
		if err != nil {
			return err
		}
		defer func() { result = errors.Join(result, store.Close()) }()

		q, _ := application.NewListQuery(application.ListOptions{}, application.ActivityScope)

		activities, err := store.ActivityList(ctx, q)
		if err != nil {
			return err
		}

		for _, item := range activities.Items {
			trade := item.Resource.Activity.Trade
			if trade == nil || trade.Price == nil {
				continue
			}

			id := item.Resource.ID

			form, e := store.CorrectionForm(ctx, "activity", id)
			if e != nil {
				return e
			}

			decision, e := calculation.NewDecision(
				calculation.DecisionInput{
					TargetKind:       "activity",
					TargetID:         id,
					Scope:            "price",
					Text:             "402.0000",
					Currency:         trade.Price.Currency,
					Action:           "set",
					ExpectedRevision: form.Revision,
					Submission:       "synthetic-acceptance-correction",
				},
			)
			if e != nil {
				return e
			}

			return application.SaveDecision(
				ctx,
				store,
				decision,
				"synthetic/operator",
				time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC),
			)
		}
	}

	return errors.New("invalid synthetic transition")
}
