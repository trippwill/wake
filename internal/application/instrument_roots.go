// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package application

import (
	"context"
	"encoding/json"
	"slices"

	"go.trippwill.dev/wake/source"
)

type RootClaim struct {
	ID                string
	Ordinal, Sequence int64
	Role              string
	Reported          source.Instrument
}

type RetainedRoot struct {
	Reported    source.Instrument
	Claims      []RootClaim
	Underlier   string
	References  map[source.Reference]struct{}
	Calculation RootCalculation
}

// RootStore reads a stable phase of bindings. After writes establish a new set
// of bindings the adapter must open a fresh phase before calculating projections.
type RootStore interface {
	LoadRoot(context.Context, string) (RetainedRoot, error)
	FindRoot(context.Context, source.Reference) (string, error)
	AutomaticUnderlier(context.Context, string) (string, error)
	QualifyRoot(context.Context, string, source.Reference) error
	CreateUnderlier(context.Context, string, source.Instrument) (string, error)
	BindUnderlierClaim(context.Context, string, string, RootClaim) error
	RetainRootContributions(context.Context, string) error
	SaveRoot(context.Context, string, RootCalculation, UnderlierLink) error
}

func rootReferenceKey(ref source.Reference) string {
	encoded, _ := json.Marshal([4]string{ref.Authority(), ref.Kind(), ref.Scope(), ref.Value()})
	return string(encoded)
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

// Reserve every qualified target from the complete prospective batch before
// binding anything. Contested inputs remain evidence; they cannot choose an ISIN
// or replace an established automatic link.
func underlierTargets(
	ctx context.Context,
	p RootStore,
	parents []string,
) (map[string]underlierTarget, error) {
	groups := map[string]*underlierGroup{}
	for _, parent := range parents {
		root, err := p.LoadRoot(ctx, parent)
		if err != nil {
			return nil, err
		}

		references := root.References

		var claims []source.Instrument
		for _, claim := range root.Claims {
			if claim.Role != "subject" {
				continue
			}

			u := claim.Reported.Option().Fields().Underlier
			if u.Kind() == "" {
				continue
			}

			claims = append(claims, u)
		}

		if root.Underlier != "" {
			child, e := p.LoadRoot(ctx, root.Underlier)
			if e != nil {
				return nil, e
			}

			for _, claim := range child.Claims {
				claims = append(claims, claim.Reported)
			}
		}

		for reference := range references {
			key := rootReferenceKey(reference)
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

		candidate, err := p.FindRoot(ctx, group.reference)
		if err != nil {
			return nil, err
		}

		if candidate != "" {
			root, e := p.LoadRoot(ctx, candidate)
			if e != nil {
				return nil, e
			}

			target = underlierTarget{
				id:       candidate,
				reported: root.Calculation.Reported,
				conflict: root.Calculation.Conflict,
			}
		}

		reported, conflict, err := ReconcileInstrument(target.reported, group.claims)
		if err != nil {
			return nil, err
		}

		target.reported, target.conflict = reported, target.conflict || conflict
		if target.id == "" {
			anonymous := map[string]bool{}
			if !target.conflict {
				for _, parent := range group.parents {
					root, e := p.LoadRoot(ctx, parent)
					if e != nil {
						return nil, e
					}

					if root.Calculation.Underlier.Reference() != group.reference {
						continue
					}

					selected, e := p.AutomaticUnderlier(ctx, parent)
					if e != nil {
						return nil, e
					}

					if selected == "" {
						continue
					}

					previous, e := p.LoadRoot(ctx, selected)
					if e != nil {
						return nil, e
					}

					if previous.Reported.Reference().IsZero() &&
						CompatibleInstrument(previous.Reported, reported) {
						anonymous[selected] = true
					}
				}
			}
			// Only one existing anonymous identity can be enriched unambiguously.
			// Multiple existing roots stay distinct instead of being merged by order.
			if len(anonymous) == 1 {
				for id := range anonymous {
					target.id = id
				}

				if err = p.QualifyRoot(ctx, target.id, group.reference); err != nil {
					return nil, err
				}
			} else {
				identity, e := source.NewInstrument(
					source.InstrumentInput{Kind: source.UnknownInstrument, Reference: group.reference},
				)
				if e != nil {
					return nil, e
				}

				target.id, err = p.CreateUnderlier(ctx, "", identity)
				if err != nil {
					return nil, err
				}
			}
		}

		targets[key] = target
	}

	return targets, nil
}

type UnderlierLink struct {
	ID string
	// Invalid targets clear a previous automatic choice even during conflict.
	Invalid bool
}

func resolveUnderlier(
	ctx context.Context, p RootStore,
	parent string,
	u source.Instrument,
	targets map[string]underlierTarget,
) (UnderlierLink, error) {
	if u.Kind() == "" {
		return UnderlierLink{}, nil
	}

	selected, err := p.AutomaticUnderlier(ctx, parent)
	if err != nil {
		return UnderlierLink{}, err
	}

	var target underlierTarget
	if !u.Reference().IsZero() {
		var ok bool

		target, ok = targets[rootReferenceKey(u.Reference())]
		if !ok {
			candidate, e := p.FindRoot(ctx, u.Reference())
			if e != nil {
				return UnderlierLink{}, e
			}

			if candidate == "" {
				return UnderlierLink{}, ErrNotFound
			}

			root, e := p.LoadRoot(ctx, candidate)
			if e != nil {
				return UnderlierLink{}, e
			}

			target = underlierTarget{
				id:       candidate,
				reported: root.Calculation.Reported,
				conflict: root.Calculation.Conflict,
			}
		}
	}

	if target.id == parent || selected == parent {
		return UnderlierLink{Invalid: true}, nil
	}

	if target.id != "" &&
		!CompatibleInstrumentKind(source.UnknownInstrument, target.reported.Kind()) {
		return UnderlierLink{Invalid: true}, nil
	}

	if selected != "" {
		previous, e := p.LoadRoot(ctx, selected)
		if e != nil {
			return UnderlierLink{}, e
		}

		if !CompatibleInstrumentKind(source.UnknownInstrument, previous.Calculation.Reported.Kind()) {
			return UnderlierLink{Invalid: true}, nil
		}

		if previous.Calculation.Conflict || target.conflict ||
			!CompatibleInstrument(previous.Reported, u) ||
			target.id != "" && !CompatibleInstrument(previous.Reported, target.reported) {
			return UnderlierLink{ID: selected}, nil
		}

		if target.id == "" {
			return UnderlierLink{ID: selected}, nil
		}
	}

	if target.id != "" {
		return UnderlierLink{ID: target.id}, nil
	}

	id, err := p.CreateUnderlier(ctx, parent, u)

	return UnderlierLink{ID: id}, err
}

// Bind source occurrences independently of operator choices. Qualified subject
// relationships survive even when an occurrence adds no supporting evidence.
func BindUnderliers(ctx context.Context, phase RootStore, parents []string) error {
	targets, err := underlierTargets(ctx, phase, parents)
	if err != nil {
		return err
	}
	// Decide all links before the first nested binding can change target history.
	links := map[string]string{}
	for _, parent := range parents {
		root, e := phase.LoadRoot(ctx, parent)
		if e != nil {
			return e
		}

		link, e := resolveUnderlier(ctx, phase, parent, root.Calculation.Underlier, targets)
		if e != nil {
			return e
		}

		links[parent] = link.ID
	}

	for _, parent := range parents {
		root, e := phase.LoadRoot(ctx, parent)
		if e != nil {
			return e
		}

		for _, claim := range root.Claims {
			if claim.Role != "subject" {
				continue
			}

			u := claim.Reported.Option().Fields().Underlier
			if u.Kind() == "" {
				continue
			}

			ids := map[string]bool{}
			if !u.Reference().IsZero() {
				ids[targets[rootReferenceKey(u.Reference())].id] = true
				if len(root.References) == 1 {
					ids[links[parent]] = true
				}
			} else {
				ids[links[parent]] = true
				if len(root.References) == 1 {
					for ref := range root.References {
						ids[targets[rootReferenceKey(ref)].id] = true
					}
				}
			}

			delete(ids, "")

			if len(ids) == 0 {
				id, e := phase.CreateUnderlier(ctx, parent, u)
				if e != nil {
					return e
				}

				ids[id] = true
			}

			for id := range ids {
				if err := phase.BindUnderlierClaim(ctx, parent, id, claim); err != nil {
					return err
				}
			}
		}
	}

	return nil
}

func ProjectRoots(ctx context.Context, phase RootStore, ids []string) error {
	for _, id := range ids {
		if err := phase.RetainRootContributions(ctx, id); err != nil {
			return err
		}
	}

	results := make(map[string]RootCalculation, len(ids))
	links := make(map[string]UnderlierLink, len(ids))
	for _, id := range ids {
		result, err := CalculateRetainedRoot(ctx, phase, id)
		if err != nil {
			return err
		}

		results[id] = result

		if result.Reported.Kind() == source.Option {
			links[id], err = resolveUnderlier(ctx, phase, id, result.Underlier, nil)
			if err != nil {
				return err
			}
		}
	}

	for _, id := range ids {
		if err := phase.SaveRoot(ctx, id, results[id], links[id]); err != nil {
			return err
		}
	}

	return nil
}

func CalculateRetainedRoot(ctx context.Context, p RootStore, id string) (RootCalculation, error) {
	root, err := p.LoadRoot(ctx, id)
	if err != nil {
		return RootCalculation{}, err
	}

	result := root.Calculation
	for _, reported := range []source.Instrument{result.Reported, result.Underlier} {
		if reported.Reference().IsZero() {
			continue
		}

		candidate, e := p.FindRoot(ctx, reported.Reference())
		if e != nil {
			return RootCalculation{}, e
		}

		if candidate == "" {
			continue
		}

		if candidate == id {
			result.Conflict = result.Conflict || reported.Reference() == result.Underlier.Reference()
			continue
		}

		value, e := p.LoadRoot(ctx, candidate)
		if e != nil {
			return RootCalculation{}, e
		}

		result.Conflict = result.Conflict || value.Calculation.Conflict ||
			!CompatibleInstrument(reported, value.Calculation.Reported)

		if reported.Reference() == result.Underlier.Reference() {
			result, e = EnrichRootUnderlier(result, value.Calculation)
			if e != nil {
				return RootCalculation{}, e
			}
		}
	}

	return result, nil
}

// Follow the automatic source relationship, independently of operator overrides
// and of whether the parent retained this child's latest observation.
func EnrichRootUnderlier(result, child RootCalculation) (RootCalculation, error) {
	if result.Reported.Kind() != source.Option {
		return result, nil
	}

	compatible := CompatibleInstrumentKind(source.UnknownInstrument, child.Reported.Kind()) &&
		CompatibleInstrument(result.Underlier, child.Reported)
	result.Conflict = result.Conflict || child.Conflict || !compatible
	if child.Conflict || !compatible {
		return result, nil
	}

	underlier, err := EnrichInstrument(result.Underlier, child.Reported)
	if err != nil {
		return RootCalculation{}, err
	}

	f := result.Reported.Fields()
	option := f.Option.Fields()
	option.Underlier = underlier

	f.Option, err = source.NewOptionTerms(option)
	if err != nil {
		return RootCalculation{}, err
	}

	result.Reported, err = source.NewInstrument(f)
	result.Underlier = underlier

	return result, err
}
