// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package application

import (
	"context"

	"go.trippwill.dev/wake/internal/application/calculation"
	"go.trippwill.dev/wake/internal/reference/figi"
	"go.trippwill.dev/wake/source"
)

type IdentityRoot struct {
	ID, Name, Ticker, FIGI, Evidence, Allocation string
	Family                                       source.InstrumentKind
	Reason                                       figi.Reason
}

type IdentityReader interface {
	IdentityRoot(context.Context, string) (IdentityRoot, error)
	IdentityRoots(context.Context, string) ([]IdentityRoot, error)
	AutomaticInstrument(context.Context, string) (calculation.InstrumentSelection, error)
	InstrumentDecisions(context.Context, string) ([]DecisionRecord, error)
	InstrumentReference(context.Context, string, string) (figi.Record, string, error)
	CanonicalInstrument(context.Context, string) (string, error)
}

type IdentityConflict struct {
	Root   IdentityRoot
	Reason figi.Reason
}

type IdentityConvergence struct {
	Target    IdentityRoot
	Selection calculation.InstrumentSelection
	Aliases   []string
	Conflicts []IdentityConflict
}

func PlanIdentityConvergence(
	ctx context.Context,
	reader IdentityReader,
	id string,
) (IdentityConvergence, error) {
	root, err := reader.IdentityRoot(ctx, id)
	if err != nil {
		return IdentityConvergence{}, err
	}

	base, err := reader.AutomaticInstrument(ctx, id)
	if err != nil {
		return IdentityConvergence{}, err
	}

	rootFIGI := root.FIGI
	if rootFIGI == "" {
		rootFIGI = base.FIGI
	}

	if rootFIGI == "" {
		return IdentityConvergence{}, nil
	}

	roots, err := reader.IdentityRoots(ctx, rootFIGI)
	if err != nil || len(roots) < 2 {
		return IdentityConvergence{}, err
	}

	family := root.Family

	choices := map[string]string{}
	blocked := false
	blockReason := figi.OperatorConflict
	for _, candidate := range roots {
		if !calculation.CompatibleInstrumentKind(family, candidate.Family) {
			blocked = true
			blockReason = figi.IncompatibleSubject
		}

		if family == source.UnknownInstrument {
			family = candidate.Family
		}

		automatic, err := reader.AutomaticInstrument(ctx, candidate.ID)
		if err != nil {
			return IdentityConvergence{}, err
		}

		if automatic.FIGI != "" && automatic.FIGI != rootFIGI {
			blocked = true
			blockReason = figi.CompetingIdentity
		}

		if base.FIGI == "" && automatic.FIGI == rootFIGI {
			base = automatic
		}

		decisions, err := reader.InstrumentDecisions(ctx, candidate.ID)
		if err != nil {
			return IdentityConvergence{}, err
		}

		active := map[string]DecisionRecord{}
		for _, decision := range decisions {
			if decision.Active {
				active[decision.Scope] = decision
			}
		}

		if retainedIdentityConflict(candidate.Reason) {
			if _, chosen := active["reference"]; !chosen {
				blocked = true
				blockReason = candidate.Reason
			}
		}

		for scope, decision := range active {
			value, err := identityChoice(ctx, reader, candidate, decision)
			if err != nil {
				return IdentityConvergence{}, err
			}

			if scope == "reference" && value != rootFIGI {
				blocked = true
			}

			if previous, ok := choices[scope]; ok && previous != value {
				blocked = true
			}

			choices[scope] = value
		}
	}

	if blocked {
		return IdentityConvergence{Conflicts: identityConflicts(roots, blockReason)}, nil
	}

	target := roots[0]

	owner, err := reader.AutomaticInstrument(ctx, target.ID)
	if err != nil {
		return IdentityConvergence{}, err
	}

	if owner.Name != "" {
		base.Name = owner.Name
	}

	if owner.Ticker != "" {
		base.Ticker = owner.Ticker
	}

	if owner.Underlier != "" {
		base.Underlier = owner.Underlier
	}

	if base.Underlier != "" {
		base.Underlier, err = reader.CanonicalInstrument(ctx, base.Underlier)
		if err != nil {
			return IdentityConvergence{}, err
		}
	}

	target.Allocation, target.Family, target.Reason = root.Allocation, family, root.Reason
	if target.Reason == figi.OperatorConflict || target.Reason == figi.ConvergenceBlocked {
		target.Reason = ""
	}

	aliases := make([]string, 0, len(roots)-1)
	for _, candidate := range roots[1:] {
		aliases = append(aliases, candidate.ID)
	}

	return IdentityConvergence{Target: target, Selection: base, Aliases: aliases}, nil
}

func retainedIdentityConflict(reason figi.Reason) bool {
	switch reason {
	case figi.SourceConflict,
		figi.CompetingIdentity,
		figi.ConflictingParents,
		figi.IncompatibleSubject:
		return true
	default:
		return false
	}
}

func identityConflicts(roots []IdentityRoot, reason figi.Reason) []IdentityConflict {
	if reason != figi.OperatorConflict {
		reason = figi.ConvergenceBlocked
	}

	conflicts := make([]IdentityConflict, 0, len(roots))
	for _, root := range roots {
		current := reason
		if retainedIdentityConflict(root.Reason) {
			current = root.Reason
		}

		conflicts = append(conflicts, IdentityConflict{Root: root, Reason: current})
	}

	return conflicts
}

func identityChoice(
	ctx context.Context,
	reader IdentityReader,
	root IdentityRoot,
	d DecisionRecord,
) (string, error) {
	switch d.Scope {
	case "reference":
		record, _, err := reader.InstrumentReference(ctx, root.ID, d.Text)
		if err != nil {
			return "", err
		}

		return figi.SelectIdentity(root.Family, []figi.Record{record}).FIGI, nil
	case "underlier":
		return reader.CanonicalInstrument(ctx, d.Text)
	default:
		return d.Text + "\x00" + d.Currency, nil
	}
}
