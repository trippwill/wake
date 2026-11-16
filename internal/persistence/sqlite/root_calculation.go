// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0
package persistence

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/application/calculation"
	"go.trippwill.dev/wake/internal/persistence/sqlite/sqlc"
	"go.trippwill.dev/wake/source"
)

// A phase reads one stable set of bindings. Discard it after binding writes;
// shared targets are read and decoded once regardless of their parent count.
type rootPhase struct {
	q        *sqlc.Queries
	roots    map[string]application.RetainedRoot
	bindings map[string][]sqlc.Instrument
}

func newRootPhase(q *sqlc.Queries) *rootPhase {
	return &rootPhase{
		q:        q,
		roots:    map[string]application.RetainedRoot{},
		bindings: map[string][]sqlc.Instrument{},
	}
}

func (p *rootPhase) bindingRows(ctx context.Context, id string) ([]sqlc.Instrument, error) {
	if rows, ok := p.bindings[id]; ok {
		return rows, nil
	}

	rows, err := p.q.InstrumentBindings(ctx, id)
	if err == nil {
		p.bindings[id] = rows
	}

	return rows, err
}

func (p *rootPhase) LoadRoot(ctx context.Context, id string) (application.RetainedRoot, error) {
	if root, ok := p.roots[id]; ok {
		return root, nil
	}

	row, err := p.q.Instrument(ctx, id)
	if err != nil {
		return application.RetainedRoot{}, err
	}

	value, err := instrumentValue(row)
	if err != nil {
		return application.RetainedRoot{}, err
	}

	inputs, err := p.q.RootCalculationInputs(ctx, id)
	if err != nil {
		return application.RetainedRoot{}, err
	}

	root := application.RetainedRoot{Reported: value.Reported(), References: map[source.Reference]struct{}{}}
	for _, input := range inputs {
		reported, e := reportedRootClaim(input.Kind, input.Data, input.Location, input.Role)
		if e != nil {
			return application.RetainedRoot{}, e
		}

		root.Claims = append(
			root.Claims,
			calculation.RootClaim{
				ID:       input.ClaimID,
				Ordinal:  input.Ordinal,
				Role:     input.Role,
				Sequence: input.Sequence,
				Reported: reported,
			},
		)
		if input.Role == "subject" {
			reference := reported.Option().Fields().Underlier.Reference()
			if !reference.IsZero() {
				root.References[reference] = struct{}{}
			}
		}
	}

	children, err := p.positionUnderlierClaims(ctx, id)
	if err != nil {
		return application.RetainedRoot{}, err
	}

	root.Claims = append(root.Claims, children...)
	slices.SortStableFunc(
		root.Claims,
		func(a, b calculation.RootClaim) int { return cmp.Compare(a.Sequence, b.Sequence) },
	)

	bindings, err := p.sourceBindings(ctx, row, root.Reported, root.References)
	if err != nil {
		return application.RetainedRoot{}, err
	}

	root.Calculation, err = calculation.CalculateRootClaims(
		root.Reported,
		row.ReferenceFigi != "",
		root.Claims,
		bindings,
	)
	if err != nil {
		return application.RetainedRoot{}, err
	}

	p.roots[id] = root
	if root.Calculation.Underlier.Kind() != "" {
		selection, e := p.q.InstrumentSelection(ctx, id)
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			return application.RetainedRoot{}, e
		}

		if selection.UnderlierID.Valid && selection.UnderlierID.String != id {
			root.Underlier = selection.UnderlierID.String

			child, e := p.LoadRoot(ctx, root.Underlier)
			if e != nil {
				return application.RetainedRoot{}, e
			}

			root.Calculation, e = application.EnrichRootUnderlier(root.Calculation, child.Calculation)
			if e != nil {
				return application.RetainedRoot{}, e
			}
		}
	}

	p.roots[id] = root

	return root, nil
}

func (p *rootPhase) sourceBindings(
	ctx context.Context,
	row sqlc.Instrument,
	reported source.Instrument,
	references map[source.Reference]struct{},
) (map[source.Reference]calculation.RootBinding, error) {
	bindings := map[source.Reference]calculation.RootBinding{}
	if row.ReferenceFigi != "" {
		rows, e := p.bindingRows(ctx, row.ID)
		if e != nil {
			return nil, e
		}

		for _, binding := range rows {
			value, e := instrumentValue(binding)
			if e != nil {
				return nil, e
			}

			bindings[value.Reported().Reference()] = calculation.RootBinding{
				Reported:  value.Reported(),
				Canonical: reported,
			}
		}
	}
	// Verified aliases of nested underliers use the same identity comparison.
	// Raw claims remain unchanged, including their original qualified references.
	for ref := range references {
		underlier, e := p.q.InstrumentBySource(ctx, referenceKey(ref))
		if errors.Is(e, sql.ErrNoRows) {
			continue
		}

		if e != nil {
			return nil, e
		}

		if underlier.ReferenceFigi == "" {
			continue
		}

		value, e := instrumentValue(underlier)
		if e != nil {
			return nil, e
		}

		rows, e := p.bindingRows(ctx, underlier.ID)
		if e != nil {
			return nil, e
		}

		for _, binding := range rows {
			original, e := instrumentValue(binding)
			if e != nil {
				return nil, e
			}

			bindings[original.Reported().Reference()] = calculation.RootBinding{
				Reported:  original.Reported(),
				Canonical: value.Reported(),
			}
		}
	}

	return bindings, nil
}

func bindArtifactInstrumentClaims(ctx context.Context, q *sqlc.Queries, artifact string) error {
	rows, err := q.ArtifactInstrumentClaims(ctx, artifact)
	if err != nil {
		return err
	}

	parents := []string{}
	seen := map[string]bool{}
	for _, row := range rows {
		if !row.InstrumentID.Valid {
			continue
		}

		data, e := claimData(row.Kind, row.Data, row.Location)
		if e != nil {
			return e
		}

		reported, e := reportedActivityInstrument(row.Kind, data)
		if e != nil {
			return e
		}

		if reported.Kind() == "" {
			continue
		}

		root, e := q.Instrument(ctx, row.InstrumentID.String)
		if e != nil {
			return e
		}

		baseline, e := instrumentValue(root)
		if e != nil {
			return e
		}

		if root.ReferenceFigi == "" && !baseline.Reported().Reference().IsZero() &&
			!reported.Reference().IsZero() &&
			baseline.Reported().Reference() != reported.Reference() {
			continue
		}

		if e = q.PutInstrumentClaim(
			ctx,
			sqlc.PutInstrumentClaimParams{
				InstrumentID: root.ID,
				ClaimID:      row.ClaimID,
				Ordinal:      row.Ordinal,
				Role:         "subject",
			},
		); e != nil {
			return e
		}

		if reported.Kind() == source.Option && !seen[root.ID] {
			parents = append(parents, root.ID)
			seen[root.ID] = true
		}
	}

	return bindUnderlierInstrumentClaims(ctx, q, parents)
}

func bindUnderlierInstrumentClaims(ctx context.Context, q *sqlc.Queries, parents []string) error {
	return application.BindUnderliers(ctx, newRootPhase(q), parents)
}

func projectArtifactRoots(ctx context.Context, q *sqlc.Queries, artifact string) error {
	if err := bindArtifactInstrumentClaims(ctx, q, artifact); err != nil {
		return err
	}

	ids, err := q.ArtifactRootIDs(ctx, artifact)
	if err != nil {
		return err
	}

	return projectRoots(ctx, q, ids)
}

func projectRoots(ctx context.Context, q *sqlc.Queries, ids []string) error {
	return application.ProjectRoots(ctx, newRootPhase(q), ids)
}

func calculateRoot(ctx context.Context, q *sqlc.Queries, id string) (calculation.RootCalculation, error) {
	return application.CalculateRetainedRoot(ctx, newRootPhase(q), id)
}

func (p *rootPhase) FindRoot(ctx context.Context, ref source.Reference) (string, error) {
	row, err := p.q.InstrumentBySource(ctx, referenceKey(ref))
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}

	return row.ID, err
}

func (p *rootPhase) AutomaticUnderlier(ctx context.Context, parent string) (string, error) {
	if err := p.q.EnsureInstrumentSelection(ctx, parent); err != nil {
		return "", err
	}

	row, err := p.q.InstrumentSelection(ctx, parent)

	return row.UnderlierID.String, err
}

func (p *rootPhase) QualifyRoot(ctx context.Context, id string, ref source.Reference) error {
	return p.q.QualifyInstrument(ctx, sqlc.QualifyInstrumentParams{ID: id, SourceKey: referenceKey(ref)})
}

func (p *rootPhase) CreateUnderlier(ctx context.Context, parent string, i source.Instrument) (string, error) {
	location := "underlier"
	if parent != "" {
		location = parent + "/underlier"
	}

	id, _, err := acceptInstrument(ctx, p.q, "retained", location, i, "")

	return id, err
}

func (p *rootPhase) BindUnderlierClaim(
	ctx context.Context,
	parent, id string,
	claim calculation.RootClaim,
) error {
	if id != parent {
		if err := p.q.PutInstrumentUnderlier(
			ctx,
			sqlc.PutInstrumentUnderlierParams{ParentID: parent, UnderlierID: id},
		); err != nil {
			return err
		}
	}

	return p.q.PutInstrumentClaim(ctx, sqlc.PutInstrumentClaimParams{
		InstrumentID: id, ClaimID: claim.ID, Ordinal: claim.Ordinal, Role: "underlier",
	})
}

func (p *rootPhase) RetainRootContributions(ctx context.Context, id string) error {
	return retainInstrumentContributions(ctx, p.q, id)
}

func (p *rootPhase) SaveRoot(
	ctx context.Context,
	id string,
	result calculation.RootCalculation,
	link calculation.UnderlierLink,
) error {
	return persistRootEnrichment(ctx, p.q, id, result, link)
}

func reportedRootClaim(kind string, raw []byte, location, role string) (source.Instrument, error) {
	data, err := claimData(kind, raw, location)
	if err != nil {
		return source.Instrument{}, err
	}

	claim, err := reportedActivityInstrument(kind, data)
	if err != nil {
		return source.Instrument{}, err
	}

	if role == "underlier" {
		claim = claim.Option().Fields().Underlier
	}

	return claim, nil
}

func reportedActivityInstrument(kind string, data []byte) (source.Instrument, error) {
	if kind == "position-instrument" {
		var d positionInstrumentData
		if err := json.Unmarshal(data, &d); err != nil {
			return source.Instrument{}, err
		}

		return d.Instrument.instrument()
	}

	if kind == "activity" {
		o, err := decodeObservation(data)
		if err != nil {
			return source.Instrument{}, err
		}

		f, ok := o.Field(source.FieldInstrument)
		if !ok || !f.Known() {
			return source.Instrument{}, nil
		}

		return f.Fields().Instrument, nil
	}

	terms, err := decodeActivityTerms(kind, data)
	if err != nil {
		return source.Instrument{}, err
	}

	return terms.Instrument(), nil
}
