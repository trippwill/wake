// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/application/calculation"
	"go.trippwill.dev/wake/internal/persistence/sqlite/sqlc"
	"go.trippwill.dev/wake/internal/reference/figi"
	"go.trippwill.dev/wake/source"
)

// loadActivityCalculation gathers retained inputs for explicit application selections.
func loadActivityCalculation(
	ctx context.Context,
	q *sqlc.Queries,
	row sqlc.Execution,
	state calculation.ActivityState,
	selections []calculation.FieldSelection,
) (calculation.ActivityCalculationInput, error) {
	in := calculation.ActivityCalculationInput{
		Rule:        calculation.RetainedClaimsRule,
		SourceState: state,
		Links: calculation.CalculationLinks{
			Instruments: map[string]source.InstrumentKind{},
			Listings:    map[string]string{},
		},
	}

	base, err := q.ActivityBase(ctx, row.ID)
	if err != nil {
		return in, err
	}

	in.Baseline, err = decodeActivityTerms(row.Kind, base.Data)
	if err != nil {
		return in, err
	}

	in.Binding = calculation.ActivityBinding{
		Instrument: base.InstrumentID.String,
		Listing:    base.ListingID.String,
	}

	in.Claims, err = retainedActivityClaims(ctx, q, row.ID)
	if err != nil {
		return in, err
	}

	selected, err := loadActivitySelection(ctx, q, row.ID)
	if err != nil {
		return in, err
	}

	in.BaselineFields = selected.Baseline

	if row.InstrumentID.Valid {
		root, e := q.Instrument(ctx, row.InstrumentID.String)
		if e != nil {
			return in, e
		}

		in.Links.OriginalFamily = source.InstrumentKind(root.Kind)
	}

	if base.ListingID.Valid {
		listing, e := q.Listing(ctx, base.ListingID.String)
		if e != nil {
			return in, e
		}

		in.Links.Listings[listing.ID] = listing.InstrumentID
	}

	for _, d := range selections {
		scope := d.Scope
		if d.Text != "" && (scope == "instrument" || scope == "listing") {
			kind := "instruments"
			if scope == "listing" {
				kind = "listings"
			}

			value, e := canonicalID(ctx, q, kind, d.Text)
			if e != nil {
				return in, e
			}

			d.Text = value
		}

		in.Decisions = append(
			in.Decisions,
			calculation.FieldSelection{Scope: scope, Text: d.Text, Currency: d.Currency},
		)
		selectionScope := scope
		if strings.HasPrefix(scope, "evidence/") {
			selectionScope = "observation"
		}

		switch selectionScope {
		case "observation":
			var key [3]string
			if json.Unmarshal([]byte(d.Text), &key) != nil {
				return in, calculation.ErrInvalid
			}

			selected, e := q.SelectedObservation(
				ctx,
				sqlc.SelectedObservationParams{
					ExecutionID: sql.NullString{String: row.ID, Valid: true},
					ArtifactID:  key[0],
					Version:     key[1],
					Location:    key[2],
				},
			)
			if e != nil {
				return in, calculation.ErrInvalid
			}

			data, e := claimData(selected.Kind, selected.Data, selected.Location)
			if e != nil {
				return in, e
			}

			value, e := decodeObservation(data)
			if e != nil {
				return in, e
			}

			if in.ObservationChoices == nil {
				in.ObservationChoices = map[string]source.ActivityObservation{}
			}

			in.ObservationChoices[d.Text] = value
		case "instrument":
			root, e := q.Instrument(ctx, d.Text)
			if e != nil {
				return in, calculation.ErrInvalid
			}

			in.Links.Instruments[root.ID] = source.InstrumentKind(root.Kind)
		case "listing":
			if d.Text != "" {
				listing, e := q.Listing(ctx, d.Text)
				if e != nil {
					return in, calculation.ErrInvalid
				}

				in.Links.Listings[listing.ID] = listing.InstrumentID
			}
		}
	}

	return in, nil
}

func projectActivity(
	ctx context.Context,
	q *sqlc.Queries,
	row sqlc.Execution,
	state calculation.ActivityState,
	rule calculation.FinancialRule,
) error {
	records, err := q.Decisions(ctx, sqlc.DecisionsParams{TargetKind: "activity", TargetID: row.ID})
	if err != nil {
		return err
	}

	selections := []calculation.FieldSelection{}
	for scope, record := range activeDecisions(records) {
		selections = append(
			selections,
			calculation.FieldSelection{Scope: scope, Text: record.Text, Currency: record.Currency},
		)
	}

	in, err := loadActivityCalculation(ctx, q, row, state, selections)
	if err != nil {
		return err
	}

	result, err := calculation.CalculateActivity(in)
	if err != nil {
		return err
	}

	result.Explanation.Rule = rule

	return persistActivityCalculation(ctx, q, row, result)
}

func persistActivityCalculation(
	ctx context.Context, q *sqlc.Queries, row sqlc.Execution, result calculation.ActivityCalculation,
) error {
	if err := persistActivitySelection(ctx, q, row.ID, result.Fields); err != nil {
		return err
	}

	var err error

	if row.Kind == "cash" {
		result.Binding.Instrument, err = selectedCashInstrument(ctx, q, row.ID, result)
		if err != nil {
			return err
		}
	}

	if result.AutomaticListing && result.Binding.Instrument != "" && result.Binding.Listing == "" &&
		row.Kind == "trade" {
		result.Binding.Listing, err = acceptListing(
			ctx,
			q,
			result.Binding.Instrument,
			result.Automatic.Instrument(),
			result.Automatic.Trade().Fields().Price.Currency().String(),
		)
		if err != nil {
			return err
		}
	}

	data, err := encodeActivityTerms(result.Terms)
	if err != nil {
		return err
	}

	return q.ProjectActivity(
		ctx,
		sqlc.ProjectActivityParams{
			CalculationRule:    string(result.Explanation.Rule),
			ID:                 row.ID,
			Accepted:           data,
			Status:             string(result.State.Lifecycle()),
			ReviewReasons:      int64(result.State.ReviewReasons()),
			SelectionAuthority: string(result.State.Authority()),
			InstrumentID: sql.NullString{
				String: result.Binding.Instrument,
				Valid:  result.Binding.Instrument != "",
			},
			ListingID: sql.NullString{String: result.Binding.Listing, Valid: result.Binding.Listing != ""},
		},
	)
}

// Evidence choices bind accepted cash terms without replacing the automatic base.
func selectedCashInstrument(
	ctx context.Context,
	q *sqlc.Queries,
	activity string,
	result calculation.ActivityCalculation,
) (string, error) {
	if !slices.Contains(result.Fields.OperatorFields, "instrument") ||
		slices.Contains(result.Explanation.AppliedScopes, "instrument") {
		return result.Binding.Instrument, nil
	}

	for _, field := range result.Fields.Fields {
		if field.Name() != source.FieldInstrument || !field.Known() {
			continue
		}

		support := result.Fields.Sources[field.Key()]

		var origin [3]string
		if len(support) == 0 || json.Unmarshal([]byte(support[0]), &origin) != nil {
			return "", ErrCorrupt
		}

		record, err := q.SelectedObservation(
			ctx,
			sqlc.SelectedObservationParams{
				ExecutionID: sql.NullString{String: activity, Valid: true},
				ArtifactID:  origin[0],
				Version:     origin[1],
				Location:    origin[2],
			},
		)
		if err != nil {
			return "", err
		}

		canonical, err := q.ClaimProcessing(
			ctx,
			sqlc.ClaimProcessingParams{ClaimID: record.ClaimID, Ordinal: record.Ordinal},
		)
		if err != nil {
			return "", err
		}

		instrument, _, err := acceptInstrument(
			ctx,
			q,
			canonical.ArtifactID,
			canonical.Location,
			field.Fields().Instrument,
			"",
		)
		if err != nil {
			return "", err
		}

		claims := []sqlc.ClaimRootIDsParams{}
		for _, key := range support {
			var appearance [3]string
			if json.Unmarshal([]byte(key), &appearance) != nil {
				return "", ErrCorrupt
			}

			contributor, e := q.SelectedObservation(
				ctx,
				sqlc.SelectedObservationParams{
					ExecutionID: sql.NullString{String: activity, Valid: true},
					ArtifactID:  appearance[0],
					Version:     appearance[1],
					Location:    appearance[2],
				},
			)
			if e != nil {
				return "", e
			}

			if e = q.PutInstrumentClaim(
				ctx,
				sqlc.PutInstrumentClaimParams{
					InstrumentID: instrument,
					ClaimID:      contributor.ClaimID,
					Ordinal:      contributor.Ordinal,
					Role:         "subject",
				},
			); e != nil {
				return "", e
			}

			claims = append(
				claims,
				sqlc.ClaimRootIDsParams{ClaimID: contributor.ClaimID, Ordinal: contributor.Ordinal},
			)
		}

		if field.Fields().Instrument.Kind() == source.Option {
			if err = bindUnderlierInstrumentClaims(ctx, q, []string{instrument}); err != nil {
				return "", fmt.Errorf("selected cash underlier claims: %w", err)
			}
		}

		seen := map[string]bool{}
		roots := []string{}
		for _, claim := range claims {
			ids, e := q.ClaimRootIDs(ctx, claim)
			if e != nil {
				return "", e
			}

			for _, id := range ids {
				if !seen[id] {
					roots = append(roots, id)
					seen[id] = true
				}
			}
		}

		sort.Strings(roots)

		if err = projectRoots(ctx, q, roots); err != nil {
			return "", fmt.Errorf("selected cash instrument roots: %w", err)
		}

		return instrument, nil
	}

	return "", nil
}

func projectInstrument(
	ctx context.Context,
	q *sqlc.Queries,
	id string,
	rule calculation.FinancialRule,
) error {
	result, err := application.ProjectInstrument(ctx, correctionReader{q}, id)
	if err != nil {
		return err
	}

	result.Explanation.Rule = rule

	return persistInstrumentCalculation(ctx, q, id, result)
}

func persistInstrumentCalculation(
	ctx context.Context,
	q *sqlc.Queries,
	id string,
	result calculation.InstrumentCalculation,
) error {
	if err := q.EnsureInstrumentSelection(ctx, id); err != nil {
		return err
	}

	selected := result.Selection

	root, err := q.Instrument(ctx, id)
	if err != nil {
		return err
	}

	if !slices.Contains(result.Explanation.AppliedScopes, "reference") {
		switch figi.Reason(root.ReferenceReason) {
		case figi.OperatorConflict, figi.SourceConflict, figi.ConvergenceBlocked:
			selected.Status = "conflict"
		}
	}

	if selected.FIGI == "" && root.ReferenceAllocation != "" {
		if err = q.IdentityMetadata(
			ctx,
			sqlc.IdentityMetadataParams{ID: id, Allocation: "", Reason: root.ReferenceReason, Family: root.Kind},
		); err != nil {
			return err
		}
	}

	if slices.Contains(result.Explanation.AppliedScopes, "reference") {
		root, e := q.Instrument(ctx, id)
		if e != nil {
			return e
		}

		allocation := figi.ShareClassAllocation
		switch source.InstrumentKind(root.Kind) {
		case source.Option:
			allocation = figi.CompositeAllocation
		case source.Debt:
			allocation = figi.InstrumentAllocation
		}

		if e = q.IdentityMetadata(
			ctx,
			sqlc.IdentityMetadataParams{ID: id, Allocation: string(allocation), Reason: "", Family: root.Kind},
		); e != nil {
			return e
		}
	}

	return q.ProjectInstrument(
		ctx,
		sqlc.ProjectInstrumentParams{
			CalculationRule:    string(result.Explanation.Rule),
			ID:                 id,
			Name:               selected.Name,
			Ticker:             selected.Ticker,
			Figi:               selected.FIGI,
			Evidence:           selected.Evidence,
			Status:             string(selected.Status),
			SelectionAuthority: string(result.Authority),
			UnderlierID:        sql.NullString{String: selected.Underlier, Valid: selected.Underlier != ""},
		},
	)
}

// A cash posting can establish financial terms before its optional instrument.
// Bind only compatible automatic evidence; operator decisions remain independent.
func bindCashInstrument(ctx context.Context, q *sqlc.Queries, row sqlc.Execution) (sqlc.Execution, error) {
	if row.Kind != "cash" {
		return row, nil
	}

	base, err := q.ActivityBase(ctx, row.ID)
	if err != nil || base.InstrumentID.Valid {
		return row, err
	}

	claims, err := retainedActivityClaims(ctx, q, row.ID)
	if err != nil {
		return row, err
	}

	previous, err := loadActivitySelection(ctx, q, row.ID)
	if err != nil {
		return row, err
	}

	selected, err := calculation.SelectActivityFields(previous.Baseline, claims, nil)
	if err != nil {
		return row, err
	}

	for _, conflict := range selected.Conflicts {
		if conflict == "identity" || conflict == "family" || conflict == "instrument" {
			return row, nil
		}
	}

	for _, field := range selected.Fields {
		if field.Name() != source.FieldInstrument || !field.Known() {
			continue
		}

		support := selected.Sources[field.Key()]

		var origin [3]string
		if len(support) == 0 || json.Unmarshal([]byte(support[0]), &origin) != nil {
			return row, ErrCorrupt
		}

		instrument, _, err := acceptInstrument(ctx, q, origin[0], origin[2], field.Fields().Instrument, "")
		if err != nil {
			return row, err
		}

		row.InstrumentID = sql.NullString{String: instrument, Valid: true}
		if err = q.BindActivityInstrument(
			ctx,
			sqlc.BindActivityInstrumentParams{ActivityID: row.ID, InstrumentID: row.InstrumentID},
		); err != nil {
			return row, err
		}

		return row, nil
	}

	return row, nil
}

// Recalculate only groups reached by genuinely new claims or changed source
// correction state. Known appearances keep provenance without revisiting terms.
func retainedActivityClaims(
	ctx context.Context,
	q *sqlc.Queries,
	id string,
) ([]calculation.ActivityClaim, error) {
	inputs, err := q.CalculationInputs(ctx, sql.NullString{String: id, Valid: true})
	if err != nil {
		return nil, err
	}

	claims := make([]calculation.ActivityClaim, 0, len(inputs))
	for _, input := range inputs {
		data, e := claimData(input.Kind, input.Data, input.Location)
		if e != nil {
			return nil, e
		}

		value, e := decodeObservation(data)
		if e != nil {
			return nil, e
		}

		claims = append(
			claims,
			calculation.ActivityClaim{
				Key: observationKey(
					sqlc.RecordValue{ArtifactID: input.ArtifactID, Version: input.Version, Location: input.Location},
				),
				Value:        value,
				LinkConflict: input.LinkOutcome == "native-review" || input.LinkOutcome == "kind-conflict",
			},
		)
	}

	return claims, nil
}
