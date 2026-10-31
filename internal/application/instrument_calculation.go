// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package application

import (
	"slices"
	"sort"
	"strings"

	"go.trippwill.dev/wake/source"
)

type (
	InstrumentSelection struct {
		Name, Ticker, FIGI, Evidence, Underlier string
		Status                                  ReferenceStatus
	}
	ReferenceSelection struct {
		InstrumentSelection
		Eligible bool
	}
	InstrumentCalculationInput struct {
		Rule           FinancialRule
		ID             string
		Family         source.InstrumentKind
		Baseline       InstrumentSelection
		Decisions      []FieldSelection
		References     map[string]ReferenceSelection
		CompetingFIGIs map[string]bool
		Underliers     map[string]source.InstrumentKind
	}
)

type InstrumentCalculation struct {
	Selection   InstrumentSelection
	Authority   SelectionAuthority
	Explanation CalculationExplanation
}

func CalculateInstrument(in InstrumentCalculationInput) (InstrumentCalculation, error) {
	if in.Rule != RetainedClaimsRule {
		return InstrumentCalculation{}, ErrInvalid
	}

	out := InstrumentCalculation{
		Selection:   in.Baseline,
		Authority:   AutomaticSelection,
		Explanation: CalculationExplanation{Rule: in.Rule},
	}
	decisions := slices.Clone(in.Decisions)
	sort.Slice(decisions, func(i, j int) bool { return decisions[i].Scope < decisions[j].Scope })
	// A selected reference supplies default labels before explicit field choices.
	for _, d := range decisions {
		if d.Scope == "reference" {
			selected, ok := in.References[d.Text]
			if !ok || !selected.Eligible || in.CompetingFIGIs[selected.FIGI] {
				return InstrumentCalculation{}, ErrInvalid
			}

			underlier := out.Selection.Underlier
			out.Selection = selected.InstrumentSelection
			out.Selection.Underlier = underlier
			out.Selection.Status = "resolved"
		}
	}

	for i, d := range decisions {
		if i > 0 && d.Scope == decisions[i-1].Scope {
			return InstrumentCalculation{}, ErrInvalid
		}

		policy, ok := CorrectionFieldPolicy(d.Scope)
		if !ok || policy.kinds&instrumentFields == 0 {
			return InstrumentCalculation{}, ErrInvalid
		}

		switch d.Scope {
		case "reference":
		case "name", "ticker":
			if (!policy.Optional() || d.Text != "") &&
				(d.Text == "" || len(d.Text) > policy.Limit() || strings.TrimSpace(d.Text) != d.Text) {
				return InstrumentCalculation{}, ErrInvalid
			}

			if d.Scope == "name" {
				out.Selection.Name = d.Text
			} else {
				out.Selection.Ticker = d.Text
			}
		case "underlier":
			kind, ok := in.Underliers[d.Text]
			if in.Family != source.Option ||
				d.Text != "" && (!ok || d.Text == in.ID || (kind != source.Equity && kind != source.Fund)) {
				return InstrumentCalculation{}, ErrInvalid
			}

			out.Selection.Underlier = d.Text
		}

		out.Explanation.AppliedScopes = append(out.Explanation.AppliedScopes, d.Scope)
	}

	if len(decisions) > 0 {
		out.Authority = OperatorSelection
	}

	return out, nil
}

// ReconcileInstrument compares reported roots before applying reference or operator
// selections. Conflicting enrichment keeps the retained baseline for review.
func ReconcileInstrument(
	base source.Instrument,
	observations []source.Instrument,
) (source.Instrument, bool, error) {
	known := base
	conflict := false
	for _, observation := range observations {
		if known.Kind() == "" {
			known = observation
			continue
		}

		if !CompatibleInstrument(known, observation) {
			conflict = true
			break
		}

		var err error

		known, err = EnrichInstrument(known, observation)
		if err != nil {
			conflict = true
			break
		}
	}

	if conflict {
		return base, true, nil
	}

	return known, false, nil
}

// RootCalculation reconciles shared reported terms independently of activity edits.
// Underlier identity survives disagreements in the parent's contract terms.
type RootCalculation struct {
	Reported, Underlier source.Instrument
	Conflict            bool
}

func CalculateRoot(base source.Instrument, claims []source.Instrument) (RootCalculation, error) {
	reported, conflict, err := ReconcileInstrument(base, claims)
	if err != nil {
		return RootCalculation{}, err
	}

	underliers := make([]source.Instrument, 0, len(claims))
	for _, claim := range claims {
		if u := claim.Option().Fields().Underlier; u.Kind() != "" {
			underliers = append(underliers, u)
		}
	}

	underlier, underlierConflict, err := ReconcileInstrument(base.Option().Fields().Underlier, underliers)

	return RootCalculation{
		Reported:  reported,
		Underlier: underlier,
		Conflict:  conflict || underlierConflict,
	}, err
}
