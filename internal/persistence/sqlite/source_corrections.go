// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"context"
	"encoding/json"

	"go.trippwill.dev/wake/internal/application/calculation"
	"go.trippwill.dev/wake/internal/persistence/sqlite/sqlc"
	"go.trippwill.dev/wake/source"
)

func (p retainedProcessing) ReconcileSourceCorrections(
	ctx context.Context,
	activityIDs []string,
) (map[string]calculation.ActivityState, error) {
	seeds, err := json.Marshal(activityIDs)
	if err != nil {
		return nil, err
	}

	rows, err := p.q.AffectedSourceCorrections(ctx, sqlc.AffectedSourceCorrectionsParams{
		ActivityIds: string(seeds), ArtifactID: p.id, Version: p.version,
	})
	if err != nil {
		return nil, err
	}

	edges, updates := correctionEdges(rows)
	states, statuses := calculation.CorrectionStates(edges)
	for i, status := range statuses {
		update := updates[i]
		if update.Status == string(status) {
			continue
		}

		update.Status = string(status)
		if err := p.q.CorrectionStatus(ctx, update); err != nil {
			return nil, err
		}
	}

	return states, nil
}

// The query orders the endpoint rows by occurrence. An empty activity ID
// represents an unresolved endpoint and still contributes an occurrence.
func correctionEdges(
	rows []sqlc.AffectedSourceCorrectionsRow,
) ([]calculation.CorrectionEdge, []sqlc.CorrectionStatusParams) {
	var (
		edges       []calculation.CorrectionEdge
		occurrences []sqlc.CorrectionStatusParams
	)

	for _, row := range rows {
		occurrence := sqlc.CorrectionStatusParams{
			ArtifactID: row.ArtifactID, Version: row.Version, Location: row.Location, Status: row.Status,
		}
		if len(occurrences) == 0 || occurrences[len(occurrences)-1] != occurrence {
			occurrences = append(occurrences, occurrence)
			edges = append(edges, calculation.CorrectionEdge{Kind: source.CorrectionKind(row.Kind)})
		}

		if row.ActivityID == "" {
			continue
		}

		edge := &edges[len(edges)-1]
		if row.Role == "target" {
			edge.Targets = append(edge.Targets, row.ActivityID)
		} else {
			edge.Replacements = append(edge.Replacements, row.ActivityID)
		}
	}

	return edges, occurrences
}
