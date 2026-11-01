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
	"go.trippwill.dev/wake/source"
)

type rootClaim struct {
	id       string
	ordinal  int64
	role     string
	reported source.Instrument
}

type retainedRoot struct {
	row         sqlc.Instrument
	reported    source.Instrument
	claims      []rootClaim
	references  map[string]source.Reference
	calculation application.RootCalculation
}

// A phase reads one stable set of bindings. Discard it after binding writes;
// shared targets are read and decoded once regardless of their parent count.
type rootPhase struct {
	q     *sqlc.Queries
	roots map[string]retainedRoot
}

func newRootPhase(q *sqlc.Queries) *rootPhase {
	return &rootPhase{q: q, roots: map[string]retainedRoot{}}
}

func (p *rootPhase) load(ctx context.Context, id string) (retainedRoot, error) {
	if root, ok := p.roots[id]; ok {
		return root, nil
	}

	row, err := p.q.Instrument(ctx, id)
	if err != nil {
		return retainedRoot{}, err
	}

	value, err := instrumentValue(row)
	if err != nil {
		return retainedRoot{}, err
	}

	inputs, err := p.q.RootCalculationInputs(ctx, id)
	if err != nil {
		return retainedRoot{}, err
	}

	root := retainedRoot{row: row, reported: value.Reported(), references: map[string]source.Reference{}}
	claims := make([]source.Instrument, 0, len(inputs))
	for _, input := range inputs {
		reported, e := reportedRootClaim(input)
		if e != nil {
			return retainedRoot{}, e
		}

		root.claims = append(root.claims, rootClaim{input.ClaimID, input.Ordinal, input.Role, reported})
		if input.Role == "activity" {
			reference := reported.Option().Fields().Underlier.Reference()
			if !reference.IsZero() {
				root.references[referenceKey(reference)] = reference
			}
		}

		claims = append(claims, reported)
	}

	bindings := map[source.Reference]source.Instrument{}
	if row.ReferenceFigi != "" {
		rows, e := p.q.InstrumentBindings(ctx, id)
		if e != nil {
			return retainedRoot{}, e
		}

		for _, binding := range rows {
			value, e := instrumentValue(binding)
			if e != nil {
				return retainedRoot{}, e
			}

			bindings[value.Reported().Reference()] = root.reported
		}
	}
	// Verified aliases of nested underliers use the same identity comparison.
	// Raw claims remain unchanged, including their original qualified references.
	for _, ref := range root.references {
		underlier, e := p.q.InstrumentBySource(ctx, referenceKey(ref))
		if errors.Is(e, sql.ErrNoRows) {
			continue
		}

		if e != nil {
			return retainedRoot{}, e
		}

		if underlier.ReferenceFigi == "" {
			continue
		}

		value, e := instrumentValue(underlier)
		if e != nil {
			return retainedRoot{}, e
		}

		bindings[ref] = value.Reported()
	}

	root.calculation, err = application.CalculateBoundRoot(root.reported, claims, bindings)
	if err != nil {
		return retainedRoot{}, err
	}

	p.roots[id] = root

	return root, nil
}

type underlierTarget struct {
	id       string
	reported source.Instrument
	conflict bool
}

type underlierGroup struct {
	reference source.Reference
	claims    []source.Instrument
	parents   []string
}

func (p *rootPhase) automatic(ctx context.Context, parent string) (sql.NullString, error) {
	if err := p.q.EnsureInstrumentSelection(ctx, parent); err != nil {
		return sql.NullString{}, err
	}

	selection, err := p.q.InstrumentSelection(ctx, parent)

	return selection.UnderlierID, err
}

// Reserve every qualified target from the complete prospective batch before
// binding anything. Contested inputs remain evidence; they cannot choose an ISIN
// or replace an established automatic link.
func (p *rootPhase) targets(ctx context.Context, parents []string) (map[string]underlierTarget, error) {
	groups := map[string]*underlierGroup{}
	for _, parent := range parents {
		root, err := p.load(ctx, parent)
		if err != nil {
			return nil, err
		}

		references := root.references

		var claims []source.Instrument
		for _, claim := range root.claims {
			if claim.role != "activity" {
				continue
			}

			u := claim.reported.Option().Fields().Underlier
			if u.Kind() == "" {
				continue
			}

			claims = append(claims, u)
		}

		for key, reference := range references {
			if groups[key] == nil {
				groups[key] = &underlierGroup{reference: reference}
			}

			group := groups[key]
			group.parents = append(group.parents, parent)
			for _, claim := range claims {
				if claim.Reference() == reference || claim.Reference().IsZero() && len(references) == 1 {
					group.claims = append(group.claims, claim)
				}
			}
		}
	}

	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}

	slices.Sort(keys)

	targets := make(map[string]underlierTarget, len(groups))
	for _, key := range keys {
		group := groups[key]

		var target underlierTarget

		candidate, err := p.q.InstrumentBySource(ctx, key)
		if err == nil {
			root, e := p.load(ctx, candidate.ID)
			if e != nil {
				return nil, e
			}

			target = underlierTarget{
				id:       candidate.ID,
				reported: root.calculation.Reported,
				conflict: root.calculation.Conflict,
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}

		reported, conflict, err := application.ReconcileInstrument(target.reported, group.claims)
		if err != nil {
			return nil, err
		}

		target.reported, target.conflict = reported, target.conflict || conflict
		if target.id == "" {
			anonymous := map[string]bool{}
			if !target.conflict {
				for _, parent := range group.parents {
					root, e := p.load(ctx, parent)
					if e != nil {
						return nil, e
					}

					if root.calculation.Underlier.Reference() != group.reference {
						continue
					}

					selected, e := p.automatic(ctx, parent)
					if e != nil {
						return nil, e
					}

					if !selected.Valid {
						continue
					}

					previous, e := p.load(ctx, selected.String)
					if e != nil {
						return nil, e
					}

					if previous.reported.Reference().IsZero() &&
						application.CompatibleInstrument(previous.reported, reported) {
						anonymous[selected.String] = true
					}
				}
			}
			// Only one existing anonymous identity can be enriched unambiguously.
			// Multiple existing roots stay distinct instead of being merged by order.
			if len(anonymous) == 1 {
				for id := range anonymous {
					target.id = id
				}

				if err = p.q.QualifyInstrument(
					ctx,
					sqlc.QualifyInstrumentParams{ID: target.id, SourceKey: key},
				); err != nil {
					return nil, err
				}
			} else {
				identity, e := source.NewInstrument(
					source.InstrumentInput{Kind: source.UnknownInstrument, Reference: group.reference},
				)
				if e != nil {
					return nil, e
				}

				target.id, _, err = acceptInstrument(ctx, p.q, "retained", "underlier", identity, "")
				if err != nil {
					return nil, err
				}
			}
		}

		targets[key] = target
	}

	return targets, nil
}

func (p *rootPhase) link(
	ctx context.Context,
	parent string,
	u source.Instrument,
	targets map[string]underlierTarget,
) (string, error) {
	if u.Kind() == "" {
		return "", nil
	}

	selected, err := p.automatic(ctx, parent)
	if err != nil {
		return "", err
	}

	var target underlierTarget
	if !u.Reference().IsZero() {
		var ok bool

		target, ok = targets[referenceKey(u.Reference())]
		if !ok {
			candidate, e := p.q.InstrumentBySource(ctx, referenceKey(u.Reference()))
			if e != nil {
				return "", e
			}

			root, e := p.load(ctx, candidate.ID)
			if e != nil {
				return "", e
			}

			target = underlierTarget{
				id:       candidate.ID,
				reported: root.calculation.Reported,
				conflict: root.calculation.Conflict,
			}
		}
	}

	if selected.Valid {
		previous, e := p.load(ctx, selected.String)
		if e != nil {
			return "", e
		}

		if target.conflict || !application.CompatibleInstrument(previous.reported, u) ||
			target.id != "" && !application.CompatibleInstrument(previous.reported, target.reported) {
			return selected.String, nil
		}

		if target.id == "" {
			return selected.String, nil
		}
	}

	if target.id != "" {
		return target.id, nil
	}

	id, _, err := acceptInstrument(ctx, p.q, "retained", parent+"/underlier", u, "")

	return id, err
}

// Bind source occurrences independently of operator choices. Existing bindings
// remain retained when qualified claims expose a compatible or disputed context.
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

		terms, e := decodeActivityTerms(row.Kind, data)
		if e != nil {
			return e
		}

		root, e := q.Instrument(ctx, row.InstrumentID.String)
		if e != nil {
			return e
		}

		baseline, e := instrumentValue(root)
		if e != nil {
			return e
		}

		reported := terms.Instrument()
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
				Role:         "activity",
			},
		); e != nil {
			return e
		}

		if reported.Kind() == source.Option && !seen[root.ID] {
			parents = append(parents, root.ID)
			seen[root.ID] = true
		}
	}

	phase := newRootPhase(q)

	targets, err := phase.targets(ctx, parents)
	if err != nil {
		return err
	}
	// Decide all links before the first nested binding can change target history.
	links := map[string]string{}
	for _, parent := range parents {
		root, e := phase.load(ctx, parent)
		if e != nil {
			return e
		}

		links[parent], e = phase.link(ctx, parent, root.calculation.Underlier, targets)
		if e != nil {
			return e
		}
	}

	for _, parent := range parents {
		root, e := phase.load(ctx, parent)
		if e != nil {
			return e
		}

		for _, claim := range root.claims {
			if claim.role != "activity" {
				continue
			}

			u := claim.reported.Option().Fields().Underlier
			if u.Kind() == "" {
				continue
			}

			ids := map[string]bool{}
			if !u.Reference().IsZero() {
				ids[targets[referenceKey(u.Reference())].id] = true
				if len(root.references) == 1 {
					ids[links[parent]] = true
				}
			} else {
				ids[links[parent]] = true
				if len(root.references) == 1 {
					for key := range root.references {
						ids[targets[key].id] = true
					}
				}
			}

			delete(ids, "")

			if len(ids) == 0 {
				id, _, e := acceptInstrument(ctx, q, "retained", parent+"/underlier", u, "")
				if e != nil {
					return e
				}

				ids[id] = true
			}

			for id := range ids {
				if e = q.PutInstrumentClaim(
					ctx,
					sqlc.PutInstrumentClaimParams{
						InstrumentID: id,
						ClaimID:      claim.id,
						Ordinal:      claim.ordinal,
						Role:         "underlier",
					},
				); e != nil {
					return e
				}
			}
		}
	}

	return nil
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
	phase := newRootPhase(q)
	results := make(map[string]application.RootCalculation, len(ids))
	links := make(map[string]string, len(ids))
	for _, id := range ids {
		result, err := phase.calculate(ctx, id)
		if err != nil {
			return err
		}

		results[id] = result

		links[id], err = phase.link(ctx, id, result.Underlier, nil)
		if err != nil {
			return err
		}
	}

	for _, id := range ids {
		if err := persistRootEnrichment(ctx, q, id, results[id], links[id]); err != nil {
			return err
		}
	}

	return nil
}

func calculateRoot(ctx context.Context, q *sqlc.Queries, id string) (application.RootCalculation, error) {
	return newRootPhase(q).calculate(ctx, id)
}

func (p *rootPhase) calculate(ctx context.Context, id string) (application.RootCalculation, error) {
	root, err := p.load(ctx, id)
	if err != nil {
		return application.RootCalculation{}, err
	}

	result := root.calculation
	for _, reported := range []source.Instrument{result.Reported, result.Underlier} {
		if reported.Reference().IsZero() {
			continue
		}

		candidate, e := p.q.InstrumentBySource(ctx, referenceKey(reported.Reference()))
		if errors.Is(e, sql.ErrNoRows) {
			continue
		}

		if e != nil {
			return application.RootCalculation{}, e
		}

		if candidate.ID == id {
			continue
		}

		value, e := p.load(ctx, candidate.ID)
		if e != nil {
			return application.RootCalculation{}, e
		}

		result.Conflict = result.Conflict || value.calculation.Conflict ||
			!application.CompatibleInstrument(reported, value.calculation.Reported)
	}

	return result, nil
}

func reportedRootClaim(row sqlc.RootCalculationInputsRow) (source.Instrument, error) {
	data, err := claimData(row.Kind, row.Data, row.Location)
	if err != nil {
		return source.Instrument{}, err
	}

	terms, err := decodeActivityTerms(row.Kind, data)
	if err != nil {
		return source.Instrument{}, err
	}

	claim := terms.Instrument()
	if row.Role == "underlier" {
		claim = claim.Option().Fields().Underlier
	}

	return claim, nil
}
