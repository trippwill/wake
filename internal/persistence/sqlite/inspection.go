// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"context"
	"database/sql"
	"errors"

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/application/calculation"
	"go.trippwill.dev/wake/internal/persistence/sqlite/sqlc"
)

func sourceInstructions(
	ctx context.Context,
	q *sqlc.Queries,
	id string,
) ([]application.CorrectionReview, error) {
	rows, err := q.RelatedSourceCorrections(ctx, id)
	if err != nil {
		return nil, err
	}

	out := []application.CorrectionReview{}
	for _, r := range rows {
		targets, e := q.EventLinks(
			ctx,
			sqlc.EventLinksParams{AccountID: r.AccountID, Reference: r.Target},
		)
		if e != nil {
			return nil, e
		}

		replacements, e := q.EventLinks(
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
				Status:         calculation.CorrectionStatus(r.Status),
				TargetIDs:      targets,
				ReplacementIDs: replacements,
			},
		)
	}

	return out, nil
}

func (s *Store) EvidenceContext(ctx context.Context, id string) (application.EvidenceContext, error) {
	row, err := s.queries.AdminEvidenceMetadata(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return application.EvidenceContext{}, application.ErrNotFound
	}

	if err != nil {
		return application.EvidenceContext{}, err
	}

	out := application.EvidenceContext{
		ID:            row.ID,
		Provider:      row.Provider,
		Stream:        row.Stream,
		Format:        row.Format,
		MediaType:     row.MediaType,
		Retrieved:     row.Retrieved,
		Hash:          row.Hash,
		Operation:     row.Operation,
		RequestedFrom: row.RequestedFrom,
		RequestedTo:   row.RequestedTo,
		ReportedFrom:  row.ReportedFrom,
		ReportedTo:    row.ReportedTo,
	}

	version := s.interpretationVersion(row.Provider)
	out.CanProcess = version != ""
	if out.CanProcess {
		out.Processed, err = s.queries.ProcessingComplete(
			ctx,
			sqlc.ProcessingCompleteParams{ArtifactID: id, Version: version},
		)
		if err != nil {
			return out, err
		}

		out.ProcessingFailed = !out.Processed && s.processingFailure(id) != ""
	}

	out.ReceiptCount, err = s.queries.CountAcquisitionReceipts(ctx, id)
	if err != nil {
		return out, err
	}

	receipts, err := s.queries.RecentAcquisitionReceipts(ctx, id)
	if err != nil {
		return out, err
	}

	for _, r := range receipts {
		out.Receipts = append(out.Receipts, receiptValue(r, row.Hash))
	}

	out.ConnectionName = row.ConnectionName

	return out, nil
}

func receiptValue(r sqlc.AcquisitionReceipt, retainedHash string) application.AcquisitionReceipt {
	return application.AcquisitionReceipt{
		ID:            r.ID,
		Operation:     r.Operation,
		Retrieved:     r.Retrieved,
		Hash:          r.Hash,
		Policy:        r.Policy,
		RequestedFrom: r.RequestedFrom,
		RequestedTo:   r.RequestedTo,
		ReportedFrom:  r.ReportedFrom,
		ReportedTo:    r.ReportedTo,
		Equivalent:    r.Hash != retainedHash,
	}
}

func (s *Store) SelectedReceipt(
	ctx context.Context,
	artifact, receipt string,
) (application.AcquisitionReceipt, error) {
	row, err := s.queries.AcquisitionReceiptByID(
		ctx,
		sqlc.AcquisitionReceiptByIDParams{ID: receipt, ArtifactID: artifact},
	)
	if errors.Is(err, sql.ErrNoRows) {
		return application.AcquisitionReceipt{}, application.ErrNotFound
	}

	if err != nil {
		return application.AcquisitionReceipt{}, err
	}

	return receiptValue(row.AcquisitionReceipt, row.RetainedHash), nil
}
