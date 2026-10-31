// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"time"

	"go.trippwill.dev/wake/internal/host"
	"go.trippwill.dev/wake/internal/persistence/sqlite/sqlc"
	"go.trippwill.dev/wake/provider"
)

func retainReport(
	ctx context.Context,
	q *sqlc.Queries,
	key host.WorkKey,
	step provider.Step,
) (string, string, error) {
	a := step.Artifact()
	if a.IsZero() {
		return "", "", nil
	}

	policy, digest := a.Equivalence()

	exact := sqlc.ReportComparisonParams{
		ConnectionID: key.Connection,
		Stream:       key.Stream,
		Format:       a.Format(),
		MediaType:    a.MediaType(),
		Policy:       "exact-bytes/1",
		Digest:       a.Hash(),
	}
	id, err := q.ReportComparison(ctx, exact)
	storedExact := err == nil
	if errors.Is(err, sql.ErrNoRows) && policy != "exact-bytes/1" {
		comparison := exact
		comparison.Policy = policy
		comparison.Digest = digest
		id, err = q.ReportComparison(ctx, comparison)
	}

	if errors.Is(err, sql.ErrNoRows) {
		id = rand.Text()
		storedExact = true
		err = q.PutArtifact(
			ctx,
			sqlc.PutArtifactParams{
				ID:            id,
				ConnectionID:  key.Connection,
				Stream:        key.Stream,
				Operation:     key.Operation,
				Format:        a.Format(),
				MediaType:     a.MediaType(),
				Retrieved:     a.RetrievedAt().Format(time.RFC3339Nano),
				Hash:          a.Hash(),
				Content:       a.Content(),
				RequestedFrom: step.Coverage().RequestedFrom(),
				RequestedTo:   step.Coverage().RequestedTo(),
				ReportedFrom:  step.Coverage().ReportedFrom(),
				ReportedTo:    step.Coverage().ReportedTo(),
			},
		)
	}

	if err != nil {
		return "", "", err
	}
	// Exact comparisons identify retained bytes. Discarded window variants never
	// become exact matches for a later acquisition without an equivalence policy.
	for _, comparison := range []sqlc.PutReportComparisonParams{
		{ConnectionID: key.Connection, Stream: key.Stream, Format: a.Format(), MediaType: a.MediaType(), Policy: exact.Policy, Digest: exact.Digest, ArtifactID: id},
		{ConnectionID: key.Connection, Stream: key.Stream, Format: a.Format(), MediaType: a.MediaType(), Policy: policy, Digest: digest, ArtifactID: id},
	} {
		if comparison.Policy == "exact-bytes/1" && !storedExact {
			continue
		}

		if err = q.PutReportComparison(ctx, comparison); err != nil {
			return "", "", err
		}
	}

	receipt := rand.Text()
	err = q.PutAcquisitionReceipt(
		ctx,
		sqlc.PutAcquisitionReceiptParams{
			ID:            receipt,
			ArtifactID:    id,
			Operation:     key.Operation,
			Retrieved:     a.RetrievedAt().Format(time.RFC3339Nano),
			Hash:          a.Hash(),
			Policy:        policy,
			Digest:        digest,
			RequestedFrom: step.Coverage().RequestedFrom(),
			RequestedTo:   step.Coverage().RequestedTo(),
			ReportedFrom:  step.Coverage().ReportedFrom(),
			ReportedTo:    step.Coverage().ReportedTo(),
		},
	)

	return id, receipt, err
}
