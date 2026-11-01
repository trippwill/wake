// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"context"
	"database/sql"
	"errors"
	"slices"

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/persistence/sqlite/sqlc"
	"go.trippwill.dev/wake/internal/reference"
	"go.trippwill.dev/wake/source"
)

func canonicalID(ctx context.Context, q *sqlc.Queries, kind, id string) (string, error) {
	target, err := q.ResourceAlias(ctx, sqlc.ResourceAliasParams{Kind: kind, ID: id})
	if errors.Is(err, sql.ErrNoRows) {
		return id, nil
	}

	return target, err
}

func aliasResource(ctx context.Context, q *sqlc.Queries, kind, previous, target string) error {
	if previous == target {
		return application.ErrInvalid
	}

	if err := q.FlattenAliases(
		ctx,
		sqlc.FlattenAliasesParams{Kind: kind, Previous: previous, Target: target},
	); err != nil {
		return err
	}

	return q.PutResourceAlias(ctx, sqlc.PutResourceAliasParams{Kind: kind, ID: previous, TargetID: target})
}

func decisionIdentity(
	ctx context.Context,
	q *sqlc.Queries,
	id string,
	d sqlc.OperatorDecision,
) (string, error) {
	switch d.Scope {
	case "reference":
		r, _, err := referenceCandidate(ctx, q, id, d.Text)
		if err != nil {
			return "", err
		}

		root, err := q.Instrument(ctx, id)
		if err != nil {
			return "", err
		}

		selected := reference.SelectIdentity(source.InstrumentKind(root.Kind), []reference.Record{r})

		return selected.FIGI, nil
	case "underlier":
		return canonicalID(ctx, q, "instruments", d.Text)
	default:
		return d.Text + "\x00" + d.Currency, nil
	}
}

// Resolve duplicates only after comparing active choices for every current root.
// No raw identifier, term, observation, or operator event is rewritten or deleted.
func combineIdentity(ctx context.Context, q *sqlc.Queries, id string) error {
	root, err := q.Instrument(ctx, id)
	if err != nil {
		return err
	}

	base, err := q.InstrumentSelection(ctx, id)
	if err != nil {
		return err
	}

	figi := root.ReferenceFigi
	if figi == "" {
		figi = base.Figi
	}

	if figi == "" {
		return nil
	}

	roots, err := q.IdentityRoots(ctx, figi)
	if err != nil || len(roots) < 2 {
		return err
	}

	family := source.InstrumentKind(root.Kind)

	choices := map[string]string{}
	blocked := false
	blockReason := reference.OperatorConflict
	for _, r := range roots {
		if !application.CompatibleInstrumentKind(
			family,
			source.InstrumentKind(r.Kind),
		) {
			blocked = true
			blockReason = reference.IncompatibleSubject
		}

		if family == source.UnknownInstrument {
			family = source.InstrumentKind(r.Kind)
		}

		automatic, e := q.InstrumentSelection(ctx, r.ID)
		if e != nil {
			return e
		}

		if automatic.Figi != "" && automatic.Figi != figi {
			blocked = true
			blockReason = reference.CompetingIdentity
		}

		if base.Figi == "" && automatic.Figi == figi {
			base = automatic
		}

		rows, e := q.Decisions(ctx, sqlc.DecisionsParams{TargetKind: "instrument", TargetID: r.ID})
		if e != nil {
			return e
		}

		active := activeDecisions(rows)
		if r.ReferenceReason == string(reference.CompetingIdentity) ||
			r.ReferenceReason == string(reference.ConflictingParents) ||
			r.ReferenceReason == string(
				reference.IncompatibleSubject,
			) || r.ReferenceReason == string(reference.SourceConflict) {
			if _, chosen := active["reference"]; !chosen {
				blocked = true
				blockReason = reference.Reason(r.ReferenceReason)
			}
		}

		for scope, d := range active {
			value, e := decisionIdentity(ctx, q, r.ID, d)
			if e != nil {
				return e
			}

			if scope == "reference" && value != figi {
				blocked = true
			}

			if previous, ok := choices[scope]; ok && previous != value {
				blocked = true
			}

			choices[scope] = value
		}
	}

	if blocked {
		for _, r := range roots {
			if err = q.IdentityMetadata(
				ctx,
				sqlc.IdentityMetadataParams{
					ID:         r.ID,
					Allocation: r.ReferenceAllocation,
					Reason:     string(blockReason),
					Family:     r.Kind,
				},
			); err != nil {
				return err
			}

			if err = q.ResolveInstrument(
				ctx,
				sqlc.ResolveInstrumentParams{
					ID:              r.ID,
					Name:            r.Name,
					Ticker:          r.Ticker,
					ReferenceFigi:   r.ReferenceFigi,
					EvidenceID:      r.EvidenceID,
					ReferenceStatus: "conflict",
					ReferenceNext:   r.ReferenceNext,
				},
			); err != nil {
				return err
			}
		}

		return nil
	}

	target := roots[0].ID

	owner, err := q.InstrumentSelection(ctx, target)
	if err != nil {
		return err
	}

	if owner.Name != "" {
		base.Name = owner.Name
	}

	if owner.Ticker != "" {
		base.Ticker = owner.Ticker
	}

	if owner.UnderlierID.Valid {
		base.UnderlierID = owner.UnderlierID
	}

	if base.UnderlierID.Valid {
		canonical, e := canonicalID(ctx, q, "instruments", base.UnderlierID.String)
		if e != nil {
			return e
		}

		base.UnderlierID.String = canonical
	}

	for _, r := range roots[1:] {
		if err = mergeInstrument(ctx, q, r.ID, target); err != nil {
			return err
		}
	}
	// Prefer the latest verified automatic selection; manual choices are projected
	// from their immutable events after the aliases have joined their histories.
	if err = q.SaveInstrumentSelection(
		ctx,
		sqlc.SaveInstrumentSelectionParams{
			InstrumentID: target,
			Name:         base.Name,
			Ticker:       base.Ticker,
			Figi:         base.Figi,
			Evidence:     base.Evidence,
			Status:       base.Status,
			UnderlierID:  base.UnderlierID,
		},
	); err != nil {
		return err
	}

	reason := root.ReferenceReason
	if reason == string(reference.OperatorConflict) {
		reason = ""
	}

	if err = q.IdentityMetadata(
		ctx,
		sqlc.IdentityMetadataParams{
			ID:         target,
			Allocation: root.ReferenceAllocation,
			Reason:     reason,
			Family:     string(family),
		},
	); err != nil {
		return err
	}

	if err = q.AdvanceInstrumentRevision(ctx, target); err != nil {
		return err
	}

	if err = projectInstrument(ctx, q, target); err != nil {
		return err
	}

	affected, err := q.IdentityDependents(ctx, target)
	if err != nil {
		return err
	}

	return projectRoots(ctx, q, affected)
}

func mergeInstrument(ctx context.Context, q *sqlc.Queries, previous, target string) error {
	if err := q.MoveInstrumentClaims(
		ctx,
		sqlc.MoveInstrumentClaimsParams{Target: target, Previous: previous},
	); err != nil {
		return err
	}

	if err := q.MoveReferenceAttempts(
		ctx,
		sqlc.MoveReferenceAttemptsParams{Target: target, Previous: previous},
	); err != nil {
		return err
	}

	listings, err := q.Listings(ctx, previous)
	if err != nil {
		return err
	}

	for _, l := range listings {
		candidates, e := q.ListingsByTerms(
			ctx,
			sqlc.ListingsByTermsParams{
				InstrumentID: target,
				Venue:        l.Venue,
				Ticker:       l.Ticker,
				Currency:     l.Currency,
			},
		)
		if e != nil {
			return e
		}

		index := slices.IndexFunc(
			candidates,
			func(candidate sqlc.Listing) bool { return candidate.Figi == l.Figi },
		)
		if index < 0 {
			for i, candidate := range candidates {
				if candidate.Figi != "" && l.Figi != "" {
					continue
				}

				if index >= 0 {
					index = -1
					break
				}

				index = i
			}
		}

		if index < 0 {
			if e = q.MoveListing(ctx, sqlc.MoveListingParams{InstrumentID: target, ID: l.ID}); e != nil {
				return e
			}

			continue
		}

		other := candidates[index]

		if other.Figi == "" && l.Figi != "" {
			if e = q.SetListingFIGI(ctx, sqlc.SetListingFIGIParams{ID: other.ID, Figi: l.Figi}); e != nil {
				return e
			}
		}

		from, to := sql.NullString{String: l.ID, Valid: true}, sql.NullString{String: other.ID, Valid: true}
		if e = q.MoveActivityListing(ctx, sqlc.MoveActivityListingParams{Target: to, Previous: from}); e != nil {
			return e
		}

		if e = q.MoveBaseListing(ctx, sqlc.MoveBaseListingParams{Target: to, Previous: from}); e != nil {
			return e
		}

		if e = aliasResource(ctx, q, "listings", l.ID, other.ID); e != nil {
			return e
		}

		if e = q.RetireListing(ctx, l.ID); e != nil {
			return e
		}
	}

	from, to := sql.NullString{String: previous, Valid: true}, sql.NullString{String: target, Valid: true}
	if err = q.MoveActivityInstrument(
		ctx,
		sqlc.MoveActivityInstrumentParams{Target: to, Previous: from},
	); err != nil {
		return err
	}

	if err = q.MoveBaseInstrument(ctx, sqlc.MoveBaseInstrumentParams{Target: to, Previous: from}); err != nil {
		return err
	}

	if err = q.MoveUnderliers(ctx, sqlc.MoveUnderliersParams{Target: to, Previous: from}); err != nil {
		return err
	}

	if err = q.MoveSelectedUnderliers(
		ctx,
		sqlc.MoveSelectedUnderliersParams{Target: to, Previous: from},
	); err != nil {
		return err
	}

	return aliasResource(ctx, q, "instruments", previous, target)
}
