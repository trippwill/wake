// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package application

import (
	"slices"
	"sort"

	"go.trippwill.dev/wake/source"
)

type ActivityKind string

const (
	TradeActivity      ActivityKind = "trade"
	LifecycleActivity  ActivityKind = "lifecycle"
	ConversionActivity ActivityKind = "conversion"
)

// ActivityTerms is a sealed financial value. Source and accepted identities remain
// outside it; a calculation can change terms without changing evidence identity.
type ActivityTerms struct {
	kind       ActivityKind
	trade      source.Trade
	lifecycle  source.Lifecycle
	conversion source.Conversion
}

func TradeTerms(v source.Trade) (ActivityTerms, error) {
	if _, err := source.NewTrade(v.Fields()); err != nil {
		return ActivityTerms{}, err
	}

	return ActivityTerms{kind: TradeActivity, trade: v}, nil
}

func LifecycleTerms(v source.Lifecycle) (ActivityTerms, error) {
	if _, err := source.NewLifecycle(v.Fields()); err != nil {
		return ActivityTerms{}, err
	}

	return ActivityTerms{kind: LifecycleActivity, lifecycle: v}, nil
}

func ConversionTerms(v source.Conversion) (ActivityTerms, error) {
	if _, err := source.NewConversion(v.Fields()); err != nil {
		return ActivityTerms{}, err
	}

	return ActivityTerms{kind: ConversionActivity, conversion: v}, nil
}
func (v ActivityTerms) Kind() ActivityKind            { return v.kind }
func (v ActivityTerms) Trade() source.Trade           { return v.trade }
func (v ActivityTerms) Lifecycle() source.Lifecycle   { return v.lifecycle }
func (v ActivityTerms) Conversion() source.Conversion { return v.conversion }
func (v ActivityTerms) Instrument() source.Instrument {
	switch v.kind {
	case TradeActivity:
		return v.trade.Fields().Instrument
	case LifecycleActivity:
		return v.lifecycle.Fields().Instrument
	}

	return source.Instrument{}
}

// FinancialRule is an explicit implemented rule choice, not an update runner.
type FinancialRule string

const RetainedClaimsRule FinancialRule = "retained-claims-and-decisions/1"

type (
	ActivityClaim struct {
		Key   string
		Terms ActivityTerms
	}
	FieldSelection   struct{ Scope, Text, Currency string }
	ActivityBinding  struct{ Instrument, Listing string }
	CalculationLinks struct {
		Instruments    map[string]source.InstrumentKind
		Listings       map[string]string
		OriginalFamily source.InstrumentKind
	}
)

type ActivityCalculationInput struct {
	Rule               FinancialRule
	Baseline           ActivityTerms
	Claims             []ActivityClaim
	ObservationChoices map[string]ActivityTerms
	Binding            ActivityBinding
	Links              CalculationLinks
	Decisions          []FieldSelection
	SourceState        ActivityState
}
type CalculationExplanation struct {
	Rule              FinancialRule
	ConflictingClaims bool
	Observation       string
	AppliedScopes     []string
}
type ActivityCalculation struct {
	Terms, Automatic ActivityTerms
	Binding          ActivityBinding
	State            ActivityState
	Explanation      CalculationExplanation
	AutomaticListing bool
}

// CalculateActivity is the common financial path for processing, prospective
// correction checks, saved decisions and releases. All references and decisions
// are retained inputs. This function performs no I/O and mutates none of them.
func CalculateActivity(in ActivityCalculationInput) (ActivityCalculation, error) {
	if in.Rule != RetainedClaimsRule {
		return ActivityCalculation{}, ErrInvalid
	}

	selected, conflict, err := selectActivity(in.Baseline, in.Claims)
	if err != nil {
		return ActivityCalculation{}, err
	}

	out := ActivityCalculation{
		Terms:            selected,
		Automatic:        selected,
		Binding:          in.Binding,
		Explanation:      CalculationExplanation{Rule: in.Rule, ConflictingClaims: conflict},
		AutomaticListing: true,
	}

	decisions := slices.Clone(in.Decisions)
	sort.Slice(decisions, func(i, j int) bool { return decisions[i].Scope < decisions[j].Scope })

	for i, d := range decisions {
		if _, ok := ActivityFieldPolicy(in.Baseline.Kind(), d.Scope); !ok {
			return ActivityCalculation{}, ErrInvalid
		}

		if i > 0 && d.Scope == decisions[i-1].Scope {
			return ActivityCalculation{}, ErrInvalid
		}

		if d.Scope != "observation" {
			continue
		}

		found := false
		for _, claim := range in.Claims {
			if claim.Key == d.Text && claim.Terms.Kind() == in.Baseline.Kind() {
				out.Terms = claim.Terms
				found = true

				break
			}
		}

		if !found {
			if choice, ok := in.ObservationChoices[d.Text]; ok && choice.Kind() == in.Baseline.Kind() {
				out.Terms = choice
				found = true
			}
		}

		if !found {
			return ActivityCalculation{}, ErrInvalid
		}

		out.Explanation.Observation = d.Text
	}

	for _, d := range decisions {
		switch d.Scope {
		case "observation":
		case "instrument":
			family, ok := in.Links.Instruments[d.Text]
			if !ok || in.Binding.Instrument == "" || family != in.Links.OriginalFamily {
				return ActivityCalculation{}, ErrInvalid
			}

			out.Binding.Instrument = d.Text
			out.AutomaticListing = d.Text == in.Binding.Instrument
			if out.Binding.Listing != "" && in.Links.Listings[out.Binding.Listing] != d.Text {
				out.Binding.Listing = ""
			}
		case "listing":
			if d.Text != "" && in.Links.Listings[d.Text] != out.Binding.Instrument {
				return ActivityCalculation{}, ErrInvalid
			}

			out.Binding.Listing = d.Text
			out.AutomaticListing = false
		default:
			out.Terms, err = applyActivityField(out.Terms, d)
			if err != nil {
				return ActivityCalculation{}, err
			}
		}

		out.Explanation.AppliedScopes = append(out.Explanation.AppliedScopes, d.Scope)
	}

	lifecycle := in.SourceState.Lifecycle()
	if lifecycle == "" {
		lifecycle = Accepted
	}

	review := in.SourceState.ReviewReasons()
	if conflict {
		review |= ConflictingClaims
	}

	authority := AutomaticSelection
	if len(decisions) > 0 {
		authority = OperatorSelection
	}

	out.State, err = NewActivityState(lifecycle, review, authority)

	return out, err
}

func selectActivity(base ActivityTerms, claims []ActivityClaim) (ActivityTerms, bool, error) {
	conflict := false
	switch base.kind {
	case TradeActivity:
		values := []source.Trade{}
		for _, claim := range claims {
			if claim.Terms.kind != base.kind {
				conflict = true
				continue
			}

			values = append(values, claim.Terms.trade)
		}

		selected, disagreement, err := SelectTrades(base.trade, values)

		return ActivityTerms{kind: base.kind, trade: selected}, conflict || disagreement, err
	case LifecycleActivity:
		values := []source.Lifecycle{}
		for _, claim := range claims {
			if claim.Terms.kind != base.kind {
				conflict = true
				continue
			}

			values = append(values, claim.Terms.lifecycle)
		}

		selected, disagreement, err := SelectLifecycles(base.lifecycle, values)

		return ActivityTerms{kind: base.kind, lifecycle: selected}, conflict || disagreement, err
	case ConversionActivity:
		values := []source.Conversion{}
		for _, claim := range claims {
			if claim.Terms.kind != base.kind {
				conflict = true
				continue
			}

			values = append(values, claim.Terms.conversion)
		}

		selected, disagreement, err := SelectConversions(base.conversion, values)

		return ActivityTerms{kind: base.kind, conversion: selected}, conflict || disagreement, err
	}

	return ActivityTerms{}, false, ErrInvalid
}

func applyActivityField(value ActivityTerms, d FieldSelection) (ActivityTerms, error) {
	if _, ok := ActivityFieldPolicy(value.Kind(), d.Scope); !ok {
		return ActivityTerms{}, ErrInvalid
	}

	switch value.kind {
	case TradeActivity:
		v, err := ApplyTradeField(value.trade, d.Scope, d.Text, d.Currency)
		return ActivityTerms{kind: value.kind, trade: v}, err
	case LifecycleActivity:
		v, err := ApplyLifecycleField(value.lifecycle, d.Scope, d.Text, d.Currency)
		return ActivityTerms{kind: value.kind, lifecycle: v}, err
	case ConversionActivity:
		v, err := ApplyConversionField(value.conversion, d.Scope, d.Text, d.Currency)
		return ActivityTerms{kind: value.kind, conversion: v}, err
	}

	return ActivityTerms{}, ErrInvalid
}
