// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"context"
	"database/sql"
	"errors"

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

func identityRoot(row sqlc.Instrument) application.IdentityRoot {
	return application.IdentityRoot{
		ID:         row.ID,
		Name:       row.Name,
		Ticker:     row.Ticker,
		FIGI:       row.ReferenceFigi,
		Evidence:   row.EvidenceID,
		Allocation: row.ReferenceAllocation,
		Family:     source.InstrumentKind(row.Kind),
		Reason:     reference.Reason(row.ReferenceReason),
	}
}

func (r correctionReader) IdentityRoot(ctx context.Context, id string) (application.IdentityRoot, error) {
	row, err := r.q.Instrument(ctx, id)
	return identityRoot(row), err
}

func (r correctionReader) IdentityRoots(
	ctx context.Context,
	figi string,
) ([]application.IdentityRoot, error) {
	rows, err := r.q.IdentityRoots(ctx, figi)
	if err != nil {
		return nil, err
	}

	roots := make([]application.IdentityRoot, 0, len(rows))
	for _, row := range rows {
		roots = append(roots, identityRoot(row))
	}

	return roots, nil
}

func (r correctionReader) AutomaticInstrument(
	ctx context.Context,
	id string,
) (application.InstrumentSelection, error) {
	row, err := r.q.InstrumentSelection(ctx, id)
	return instrumentSelection(row), err
}

func combineIdentity(ctx context.Context, q *sqlc.Queries, id string) error {
	plan, err := application.PlanIdentityConvergence(ctx, correctionReader{q}, id)
	if err != nil {
		return err
	}

	for _, conflict := range plan.Conflicts {
		r := conflict.Root
		if err := q.IdentityMetadata(ctx, sqlc.IdentityMetadataParams{
			ID: r.ID, Allocation: r.Allocation, Reason: string(conflict.Reason), Family: string(r.Family),
		}); err != nil {
			return err
		}

		if err := q.ResolveInstrument(ctx, sqlc.ResolveInstrumentParams{
			ID: r.ID, Name: r.Name, Ticker: r.Ticker, ReferenceFigi: r.FIGI,
			EvidenceID: r.Evidence, ReferenceStatus: "conflict",
		}); err != nil {
			return err
		}
	}

	if plan.Target.ID == "" {
		return nil
	}

	target, base := plan.Target, plan.Selection
	for _, alias := range plan.Aliases {
		if err := mergeInstrument(ctx, q, alias, target.ID); err != nil {
			return err
		}
	}

	if err := q.SaveInstrumentSelection(ctx, sqlc.SaveInstrumentSelectionParams{
		InstrumentID: target.ID, Name: base.Name, Ticker: base.Ticker, Figi: base.FIGI,
		Evidence: base.Evidence, Status: string(base.Status),
		UnderlierID: sql.NullString{String: base.Underlier, Valid: base.Underlier != ""},
	}); err != nil {
		return err
	}

	if err := q.IdentityMetadata(ctx, sqlc.IdentityMetadataParams{
		ID:         target.ID,
		Allocation: target.Allocation,
		Reason:     string(target.Reason),
		Family:     string(target.Family),
	}); err != nil {
		return err
	}

	if err := q.AdvanceInstrumentRevision(ctx, target.ID); err != nil {
		return err
	}

	if err := projectInstrument(ctx, q, target.ID, application.RetainedClaimsRule); err != nil {
		return err
	}

	affected, err := q.IdentityDependents(ctx, target.ID)
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

	if err := q.DeleteSubjectInstrumentClaims(ctx, previous); err != nil {
		return err
	}

	if err := q.MoveInstrumentUnderlierLinks(ctx, sqlc.MoveInstrumentUnderlierLinksParams{
		Previous: previous, Target: target,
	}); err != nil {
		return err
	}

	if err := q.DeleteInstrumentUnderlierLinks(ctx, previous); err != nil {
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

		identities := make([]application.ListingIdentity, 0, len(candidates))
		for _, candidate := range candidates {
			identities = append(identities, application.ListingIdentity{ID: candidate.ID, FIGI: candidate.Figi})
		}

		plan := application.PlanListingMerge(l.Figi, identities)
		if plan.TargetID == "" {
			if e = q.MoveListing(ctx, sqlc.MoveListingParams{InstrumentID: target, ID: l.ID}); e != nil {
				return e
			}

			continue
		}

		if plan.FIGIToCopy != "" {
			if e = q.SetListingFIGI(
				ctx,
				sqlc.SetListingFIGIParams{ID: plan.TargetID, Figi: plan.FIGIToCopy},
			); e != nil {
				return e
			}
		}

		from, to := sql.NullString{String: l.ID, Valid: true}, sql.NullString{String: plan.TargetID, Valid: true}
		if e = q.MoveActivityListing(ctx, sqlc.MoveActivityListingParams{Target: to, Previous: from}); e != nil {
			return e
		}

		if e = q.MoveBaseListing(ctx, sqlc.MoveBaseListingParams{Target: to, Previous: from}); e != nil {
			return e
		}

		if e = aliasResource(ctx, q, "listings", l.ID, plan.TargetID); e != nil {
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
