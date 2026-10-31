// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0
package persistence

import (
	"context"
	"database/sql"
	"errors"

	"go.trippwill.dev/wake/financial"
	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/persistence/sqlite/sqlc"
	"go.trippwill.dev/wake/provider"
	"go.trippwill.dev/wake/source"
)

// Dashboard is exhaustive inspection for storage conformance tests only.
func (s *Store) Dashboard(ctx context.Context) (inspectionView, error) {
	out := inspectionView{}

	profile, profileErr := s.queries.Profile(ctx)
	if profileErr != nil {
		return out, profileErr
	}

	out.Storage = application.StorageStatus{
		Paused:          profile.Paused != 0,
		ReferenceReview: profile.ReferenceReview != 0,
		WarehouseReset:  profile.WarehouseReset != 0,
		Revision:        profile.RecoveryRevision,
	}

	jobs, recoveryErr := s.queries.RecoveryJobs(ctx)
	if recoveryErr != nil {
		return out, recoveryErr
	}

	for _, j := range jobs {
		out.Recovery = append(
			out.Recovery,
			application.RecoveryJob{
				ID:        j.ID,
				Status:    j.Status,
				Failure:   j.Failure,
				Actor:     j.Actor,
				Created:   j.Created,
				Completed: j.Completed,
				Total:     j.Total,
			},
		)
	}

	account, err := s.queries.Account(ctx)
	if err == nil {
		out.Account, err = accountValue(account)
	}

	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}

	ids, err := s.queries.Connections(ctx)
	if err != nil {
		return out, err
	}

	for _, id := range ids {
		c, e := s.Connection(ctx, id)
		if e != nil {
			return out, e
		}

		view := application.ConnectionView{ID: id, Name: c.Name, Settings: c.Settings, Provider: c.Provider}
		if c.Name == "" {
			view.Name = "Retained source connection"
			if out.Account.Connection() == id {
				view.Name = "Source for " + out.Account.Details().Name()
			}
		}

		access, accessErr := s.queries.ConnectionAccess(ctx, id)
		if accessErr != nil {
			return out, accessErr
		}

		view.MissingAccess = access == 0

		readiness, e := s.queries.AccessReadiness(ctx, id)
		if e != nil {
			return out, e
		}

		view.SettingsStored = readiness.SettingsStored
		view.CredentialStored = readiness.CredentialStored

		view.CredentialKeys, e = s.queries.CredentialKeys(ctx, id)
		if e != nil {
			return out, e
		}

		rows, e := s.queries.CandidateProfiles(ctx, id)
		if e != nil {
			return out, e
		}

		seen := map[source.Reference]bool{}
		for _, r := range rows {
			ref, e := source.NewReference(r.Authority, r.Kind, r.Scope, r.Value)
			if e != nil {
				return out, ErrCorrupt
			}

			if seen[ref] {
				continue
			}

			seen[ref] = true

			c := financial.Currency{}
			if r.Currency != "" {
				c, e = financial.ParseCurrency(r.Currency)
				if e != nil {
					return out, ErrCorrupt
				}
			}

			candidate, e := provider.NewCandidate(ref, r.Name, c)
			if e != nil {
				return out, ErrCorrupt
			}

			view.Candidates = append(
				view.Candidates,
				application.Candidate{Artifact: r.ArtifactID, Candidate: candidate},
			)
		}

		statuses, e := s.queries.WorkStatuses(ctx, id)
		if e != nil {
			return out, e
		}

		for _, r := range statuses {
			view.Work = append(
				view.Work,
				application.WorkStatus{
					Operation: r.Operation,
					Progress:  provider.ProgressPhase(r.Observed),
					Failure:   r.Failure,
					Next:      r.Next,
				},
			)
		}

		processing, e := s.queries.ProcessingStatuses(ctx, id)
		if e != nil {
			return out, e
		}

		for _, r := range processing {
			view.Processing = append(
				view.Processing,
				application.ProcessingStatus{
					Artifact: r.ID,
					Receipt:  r.ReceiptID, Retrieved: r.Retrieved, Hash: r.Hash, ComparisonPolicy: r.Policy,
					Complete:         r.Processed == 1,
					RequestedFrom:    r.RequestedFrom,
					RequestedTo:      r.RequestedTo,
					ReportedFrom:     r.ReportedFrom,
					ReportedTo:       r.ReportedTo,
					Failure:          r.ProcessingFailure,
					TradeClaims:      r.TradeClaims,
					LifecycleClaims:  r.LifecycleClaims,
					ConversionClaims: r.ConversionClaims,
					ExcludedRows:     r.ExcludedRows,
					NeedsReview:      r.NeedsReview,
				},
			)
		}

		out.Connections = append(out.Connections, view)
	}

	corrections, err := s.queries.SourceCorrections(ctx)
	if err != nil {
		return out, err
	}

	for _, r := range corrections {
		targets, e := s.queries.EventLinks(
			ctx,
			sqlc.EventLinksParams{AccountID: r.AccountID, Reference: r.Target},
		)
		if e != nil {
			return out, e
		}

		replacements, e := s.queries.EventLinks(
			ctx,
			sqlc.EventLinksParams{AccountID: r.AccountID, Reference: r.Replacement},
		)
		if e != nil {
			return out, e
		}

		out.Corrections = append(
			out.Corrections,
			application.CorrectionReview{
				Artifact:       r.ArtifactID,
				Version:        r.Version,
				Location:       r.Location,
				Kind:           r.Kind,
				Target:         r.Target,
				Replacement:    r.Replacement,
				Status:         application.CorrectionStatus(r.Status),
				TargetIDs:      targets,
				ReplacementIDs: replacements,
			},
		)
	}

	executions, err := s.queries.Executions(ctx)
	if err != nil {
		return out, err
	}

	for _, r := range executions {
		if r.Kind == "conversion" {
			c, e := conversionValue(r)
			if e != nil {
				return out, e
			}

			out.Conversions = append(out.Conversions, c)

			continue
		}

		if r.Kind == "lifecycle" {
			l, e := lifecycleValue(r)
			if e != nil {
				return out, e
			}

			out.Lifecycles = append(out.Lifecycles, l)

			continue
		}

		e, err := executionValue(r)
		if err != nil {
			return out, err
		}

		out.Executions = append(out.Executions, e)
	}

	instruments, err := s.queries.Instruments(ctx)
	if err != nil {
		return out, err
	}

	for _, r := range instruments {
		i, err := instrumentValue(r)
		if err != nil {
			return out, err
		}

		out.Instruments = append(out.Instruments, i)
	}

	setting, err := s.queries.ReferenceSetting(ctx)
	if err == nil {
		out.ReferenceKey = len(setting.Encrypted) > 0
		out.ReferenceFailure = setting.Failure
	} else if !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}

	return out, nil
}

func inspectWorkflow(ctx context.Context, s *Store, w *application.Workflow) (inspectionView, error) {
	view, err := s.Dashboard(ctx)
	if err != nil {
		return view, err
	}

	query, _ := application.NewListQuery(application.ListOptions{}, application.SourceScope)
	for index, connection := range view.Connections {
		detail, e := w.SourceDetail(ctx, connection.ID, query)
		if e != nil {
			return view, e
		}

		view.Connections[index] = detail.Connection
	}

	return view, nil
}

type inspectionView struct {
	Storage          application.StorageStatus
	Connections      []application.ConnectionView
	Account          application.Account
	Executions       []application.Execution
	Lifecycles       []application.Lifecycle
	Conversions      []application.Conversion
	Corrections      []application.CorrectionReview
	Recovery         []application.RecoveryJob
	Instruments      []application.Instrument
	ReferenceKey     bool
	ReferenceFailure string
}
