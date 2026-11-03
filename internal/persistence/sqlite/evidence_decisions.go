// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"context"
	"database/sql"
	"encoding/json"
	"sort"
	"strings"

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/persistence/sqlite/sqlc"
)

// One review request selects precisely the fields present in its source record.
// Each resulting field decision has its own durable scope and can be released.
func activityDecisionFields(
	ctx context.Context,
	q *sqlc.Queries,
	f application.DecisionInput,
	active map[string]sqlc.OperatorDecision,
) ([]application.DecisionInput, error) {
	if f.TargetKind != "activity" || f.Scope != "observation" {
		return []application.DecisionInput{f}, nil
	}

	scopes := []string{}
	if f.Action == "release" {
		for scope := range active {
			if strings.HasPrefix(scope, "evidence/") {
				scopes = append(scopes, scope)
			}
		}
	} else {
		var key [3]string
		if json.Unmarshal([]byte(f.Text), &key) != nil {
			return nil, application.ErrInvalid
		}

		row, err := q.SelectedObservation(
			ctx,
			sqlc.SelectedObservationParams{
				ExecutionID: sql.NullString{String: f.TargetID, Valid: true},
				ArtifactID:  key[0],
				Version:     key[1],
				Location:    key[2],
			},
		)
		if err != nil {
			return nil, application.ErrInvalid
		}

		data, err := claimData(row.Kind, row.Data, row.Location)
		if err != nil {
			return nil, err
		}

		o, err := decodeObservation(data)
		if err != nil {
			return nil, err
		}

		for _, field := range o.Fields().Fields {
			scopes = append(scopes, "evidence/"+field.Key())
		}
	}

	if len(scopes) == 0 {
		return nil, application.ErrInvalid
	}

	sort.Strings(scopes)

	out := make([]application.DecisionInput, 0, len(scopes))
	for _, scope := range scopes {
		v := f
		v.Scope = scope
		out = append(out, v)
	}

	return out, nil
}
