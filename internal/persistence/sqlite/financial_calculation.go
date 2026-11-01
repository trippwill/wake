// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"sort"

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/persistence/sqlite/sqlc"
	"go.trippwill.dev/wake/internal/reference"
	"go.trippwill.dev/wake/source"
)

// loadActivityCalculation gathers a consistent retained input set. A prospective
// decision replaces its active scope in memory, without inserting or projecting it.
func loadActivityCalculation(
	ctx context.Context,
	q *sqlc.Queries,
	row sqlc.Execution,
	state application.ActivityState,
	proposed *application.DecisionInput,
) (application.ActivityCalculationInput, error) {
	in := application.ActivityCalculationInput{
		Rule:        application.RetainedClaimsRule,
		SourceState: state,
		Links: application.CalculationLinks{
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

	in.Binding = application.ActivityBinding{
		Instrument: base.InstrumentID.String,
		Listing:    base.ListingID.String,
	}

	in.Claims, err = retainedActivityClaims(ctx, q, row.ID)
	if err != nil {
		return in, err
	}

	rows, err := q.Decisions(ctx, sqlc.DecisionsParams{TargetKind: "activity", TargetID: row.ID})
	if err != nil {
		return in, err
	}

	active := activeDecisions(rows)
	if proposed != nil {
		if proposed.Action == "release" {
			if _, ok := active[proposed.Scope]; !ok {
				return in, application.ErrInvalid
			}

			delete(active, proposed.Scope)
		} else {
			active[proposed.Scope] = sqlc.OperatorDecision{
				Scope:    proposed.Scope,
				Text:     proposed.Text,
				Currency: proposed.Currency,
			}
		}
	}

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

	for scope, d := range active {
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
			application.FieldSelection{Scope: scope, Text: d.Text, Currency: d.Currency},
		)
		switch scope {
		case "observation":
			var key [3]string
			if json.Unmarshal([]byte(d.Text), &key) != nil {
				return in, application.ErrInvalid
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
				return in, application.ErrInvalid
			}

			data, e := claimData(selected.Kind, selected.Data, selected.Location)
			if e != nil {
				return in, e
			}

			value, e := decodeActivityTerms(selected.Kind, data)
			if e != nil {
				return in, e
			}

			in.ObservationChoices = map[string]application.ActivityTerms{d.Text: value}
		case "instrument":
			root, e := q.Instrument(ctx, d.Text)
			if e != nil {
				return in, application.ErrInvalid
			}

			in.Links.Instruments[root.ID] = source.InstrumentKind(root.Kind)
		case "listing":
			if d.Text != "" {
				listing, e := q.Listing(ctx, d.Text)
				if e != nil {
					return in, application.ErrInvalid
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
	state application.ActivityState,
) error {
	in, err := loadActivityCalculation(ctx, q, row, state, nil)
	if err != nil {
		return err
	}

	result, err := application.CalculateActivity(in)
	if err != nil {
		return err
	}

	return persistActivityCalculation(ctx, q, row, result)
}

func persistActivityCalculation(
	ctx context.Context, q *sqlc.Queries, row sqlc.Execution, result application.ActivityCalculation,
) error {
	var err error

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

func loadInstrumentCalculation(
	ctx context.Context,
	q *sqlc.Queries,
	id string,
	proposed *application.DecisionInput,
) (application.InstrumentCalculationInput, error) {
	in := application.InstrumentCalculationInput{
		Rule:           application.RetainedClaimsRule,
		ID:             id,
		References:     map[string]application.ReferenceSelection{},
		CompetingFIGIs: map[string]bool{},
		Underliers:     map[string]source.InstrumentKind{},
	}

	root, err := q.Instrument(ctx, id)
	if err != nil {
		return in, err
	}

	in.Family = source.InstrumentKind(root.Kind)
	base, err := q.InstrumentSelection(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		base = sqlc.InstrumentSelection{
			Name:        root.Name,
			Ticker:      root.Ticker,
			Figi:        root.ReferenceFigi,
			Evidence:    root.EvidenceID,
			Status:      root.ReferenceStatus,
			UnderlierID: root.UnderlierID,
		}
	} else if err != nil {
		return in, err
	}

	in.Baseline = application.InstrumentSelection{
		Name:      base.Name,
		Ticker:    base.Ticker,
		FIGI:      base.Figi,
		Evidence:  base.Evidence,
		Status:    application.ReferenceStatus(base.Status),
		Underlier: base.UnderlierID.String,
	}

	rows, err := q.Decisions(ctx, sqlc.DecisionsParams{TargetKind: "instrument", TargetID: id})
	if err != nil {
		return in, err
	}

	active := activeDecisions(rows)
	if proposed != nil {
		if proposed.Action == "release" {
			if _, ok := active[proposed.Scope]; !ok {
				return in, application.ErrInvalid
			}

			delete(active, proposed.Scope)
		} else {
			active[proposed.Scope] = sqlc.OperatorDecision{
				Scope:    proposed.Scope,
				Text:     proposed.Text,
				Currency: proposed.Currency,
			}
		}
	}

	for scope, d := range active {
		if scope == "underlier" && d.Text != "" {
			value, e := canonicalID(ctx, q, "instruments", d.Text)
			if e != nil {
				return in, e
			}

			d.Text = value
		}

		in.Decisions = append(
			in.Decisions,
			application.FieldSelection{Scope: scope, Text: d.Text, Currency: d.Currency},
		)
		switch scope {
		case "reference":
			record, evidence, e := referenceCandidate(ctx, q, id, d.Text)
			if e != nil {
				return in, e
			}

			selected, figi, compatible := reference.Select(in.Family, []reference.Record{record})
			in.References[d.Text] = application.ReferenceSelection{
				InstrumentSelection: application.InstrumentSelection{
					Name:     selected.Name,
					Ticker:   selected.Ticker,
					FIGI:     figi,
					Evidence: evidence,
				},
				Eligible: compatible,
			}

			others, e := q.CompetingFIGI(ctx, sqlc.CompetingFIGIParams{CurrentID: id, Figi: figi})
			if e != nil {
				return in, e
			}

			for _, other := range others {
				if !application.CompatibleInstrumentKind(in.Family, source.InstrumentKind(other.Kind)) {
					in.CompetingFIGIs[figi] = true
				}
			}
		case "underlier":
			if d.Text != "" {
				underlier, e := q.Instrument(ctx, d.Text)
				if e != nil {
					return in, application.ErrInvalid
				}

				in.Underliers[underlier.ID] = source.InstrumentKind(underlier.Kind)
			}
		}
	}

	return in, nil
}

func projectInstrument(ctx context.Context, q *sqlc.Queries, id string) error {
	in, err := loadInstrumentCalculation(ctx, q, id, nil)
	if err != nil {
		return err
	}

	result, err := application.CalculateInstrument(in)
	if err != nil {
		return err
	}

	return persistInstrumentCalculation(ctx, q, id, result)
}

func persistInstrumentCalculation(
	ctx context.Context,
	q *sqlc.Queries,
	id string,
	result application.InstrumentCalculation,
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
		switch reference.Reason(root.ReferenceReason) {
		case reference.OperatorConflict, reference.SourceConflict, reference.ConvergenceBlocked:
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

		allocation := reference.ShareClassAllocation
		switch source.InstrumentKind(root.Kind) {
		case source.Option:
			allocation = reference.CompositeAllocation
		case source.Debt:
			allocation = reference.InstrumentAllocation
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

// Recalculate only groups reached by genuinely new claims or changed source
// correction state. Known appearances keep provenance without revisiting terms.
func projectArtifact(ctx context.Context, q *sqlc.Queries, artifact string) error {
	if err := q.SeedArtifactBases(ctx, artifact); err != nil {
		return err
	}

	states, err := correctionStates(ctx, q)
	if err != nil {
		return err
	}

	ids, err := q.NewClaimActivityIDs(ctx, artifact)
	if err != nil {
		return err
	}

	affected := map[string]sqlc.Execution{}
	for _, id := range ids {
		row, e := q.Execution(ctx, id.String)
		if e != nil {
			return e
		}

		affected[row.ID] = row
	}

	for id, state := range states {
		if _, ok := affected[id]; ok {
			continue
		}

		row, e := q.Execution(ctx, id)
		if e != nil {
			return e
		}

		current, e := activityState(row)
		if e != nil {
			return e
		}

		if current.Lifecycle() != state.Lifecycle() ||
			current.ReviewReasons()&application.AmbiguousCorrection != state.ReviewReasons()&application.AmbiguousCorrection {
			affected[id] = row
		}
	}

	ordered := make([]string, 0, len(affected))
	for id := range affected {
		ordered = append(ordered, id)
	}

	sort.Strings(ordered)

	if err = projectArtifactRoots(ctx, q, artifact); err != nil {
		return err
	}

	for _, id := range ordered {
		if err = projectActivity(ctx, q, affected[id], states[id]); err != nil {
			return err
		}
	}

	return nil
}

func retainedActivityClaims(
	ctx context.Context,
	q *sqlc.Queries,
	id string,
) ([]application.ActivityClaim, error) {
	inputs, err := materializeRecords(q.CalculationInputs(ctx, sql.NullString{String: id, Valid: true}))
	if err != nil {
		return nil, err
	}

	claims := make([]application.ActivityClaim, 0, len(inputs))
	for _, input := range inputs {
		terms, e := decodeActivityTerms(input.Kind, input.Data)
		if e != nil {
			return nil, e
		}

		claims = append(claims, application.ActivityClaim{Key: observationKey(input), Terms: terms})
	}

	return claims, nil
}
