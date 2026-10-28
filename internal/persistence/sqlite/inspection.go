// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"context"

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/persistence/sqlite/sqlc"
)

func (s *Store) sourceInstructions(ctx context.Context, id string) ([]application.CorrectionReview, error) {
	rows, err := s.queries.RelatedSourceCorrections(ctx, id)
	if err != nil {
		return nil, err
	}

	out := []application.CorrectionReview{}
	for _, r := range rows {
		targets, e := s.queries.EventLinks(
			ctx,
			sqlc.EventLinksParams{AccountID: r.AccountID, Reference: r.Target},
		)
		if e != nil {
			return nil, e
		}

		replacements, e := s.queries.EventLinks(
			ctx,
			sqlc.EventLinksParams{AccountID: r.AccountID, Reference: r.Replacement},
		)
		if e != nil {
			return nil, e
		}

		out = append(
			out,
			application.CorrectionReview{
				Artifact:       r.ArtifactID,
				Version:        r.Version,
				Location:       r.Location,
				Kind:           r.Kind,
				Target:         r.Target,
				Replacement:    r.Replacement,
				Status:         r.Status,
				TargetIDs:      targets,
				ReplacementIDs: replacements,
			},
		)
	}

	return out, nil
}

func (s *Store) EvidenceContext(ctx context.Context, id string) (application.EvidenceContext, error) {
	row, err := s.queries.Retained(ctx, id)
	if err != nil {
		return application.EvidenceContext{}, err
	}

	out := application.EvidenceContext{
		Operation:     row.Operation,
		RequestedFrom: row.RequestedFrom,
		RequestedTo:   row.RequestedTo,
		ReportedFrom:  row.ReportedFrom,
		ReportedTo:    row.ReportedTo,
	}

	connection, err := s.Connection(ctx, row.ConnectionID)
	if err != nil {
		return out, err
	}

	out.ConnectionName, _ = connection.Settings.Get("name")

	rows, err := s.queries.ArtifactObservations(ctx, id)
	if err != nil {
		return out, err
	}

	for _, o := range rows {
		out.Observations = append(
			out.Observations,
			application.EvidenceObservation{
				ActivityID: o.ExecutionID.String,
				Location:   o.Location,
				Outcome:    o.Outcome,
				Rule:       o.Rule,
				Version:    o.Version,
			},
		)
	}

	return out, nil
}
