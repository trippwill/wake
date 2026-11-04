// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package application

import (
	"slices"
	"sort"
	"strings"

	"go.trippwill.dev/wake/source"
)

type ActivityKind string

const (
	TradeActivity            ActivityKind = "trade"
	LifecycleActivity        ActivityKind = "lifecycle"
	ConversionActivity       ActivityKind = "conversion"
	SecurityTransferActivity ActivityKind = "security-transfer"
	CashActivity             ActivityKind = "cash"
)

// ActivityTerms is a sealed financial value. Source and accepted identities remain
// outside it; a calculation can change terms without changing evidence identity.
type ActivityTerms struct {
	kind       ActivityKind
	trade      source.Trade
	lifecycle  source.Lifecycle
	conversion source.Conversion
	transfer   source.SecurityTransfer
	cash       source.CashActivity
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

func SecurityTransferTerms(v source.SecurityTransfer) (ActivityTerms, error) {
	if _, err := source.NewSecurityTransfer(v.Fields()); err != nil {
		return ActivityTerms{}, err
	}

	return ActivityTerms{kind: SecurityTransferActivity, transfer: v}, nil
}

func CashTerms(v source.CashActivity) (ActivityTerms, error) {
	if _, err := source.NewCashActivity(v.Fields()); err != nil {
		return ActivityTerms{}, err
	}

	return ActivityTerms{kind: CashActivity, cash: v}, nil
}
func (v ActivityTerms) Cash() source.CashActivity                 { return v.cash }
func (v ActivityTerms) SecurityTransfer() source.SecurityTransfer { return v.transfer }
func (v ActivityTerms) Kind() ActivityKind                        { return v.kind }
func (v ActivityTerms) Trade() source.Trade                       { return v.trade }
func (v ActivityTerms) Lifecycle() source.Lifecycle               { return v.lifecycle }
func (v ActivityTerms) Conversion() source.Conversion             { return v.conversion }
func (v ActivityTerms) Instrument() source.Instrument {
	switch v.kind {
	case TradeActivity:
		return v.trade.Fields().Instrument
	case CashActivity:
		return v.cash.Fields().Instrument
	case SecurityTransferActivity:
		return v.transfer.Fields().Instrument
	case LifecycleActivity:
		return v.lifecycle.Fields().Instrument
	}

	return source.Instrument{}
}

// FinancialRule is an explicit implemented rule choice, not an update runner.
type FinancialRule string

const RetainedClaimsRule FinancialRule = "retained-claims-and-decisions/2"

type (
	ActivityClaim struct {
		Key          string
		Value        source.ActivityObservation
		LinkConflict bool
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
	BaselineFields     []source.ActivityField
	Rule               FinancialRule
	Baseline           ActivityTerms
	Claims             []ActivityClaim
	ObservationChoices map[string]source.ActivityObservation
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
	Fields           ActivitySelection
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

	claims := slices.Clone(in.Claims)
	extraKeys := make([]string, 0, len(in.ObservationChoices))
	for key := range in.ObservationChoices {
		extraKeys = append(extraKeys, key)
	}

	sort.Strings(extraKeys)

	for _, key := range extraKeys {
		found := false
		for _, claim := range claims {
			found = found || claim.Key == key
		}

		if !found {
			claims = append(claims, ActivityClaim{Key: key, Value: in.ObservationChoices[key]})
		}
	}

	choices := map[string]string{}
	for _, d := range in.Decisions {
		if after, ok := strings.CutPrefix(d.Scope, "evidence/"); ok {
			choices[after] = d.Text
		}

		if d.Scope == "observation" {
			var value source.ActivityObservation
			for _, claim := range in.Claims {
				if claim.Key == d.Text {
					value = claim.Value
				}
			}

			if value.IsZero() {
				value = in.ObservationChoices[d.Text]
			}

			if value.IsZero() {
				return ActivityCalculation{}, ErrInvalid
			}

			for _, field := range value.Fields().Fields {
				choices[field.Key()] = d.Text
			}
		}
	}

	baseFields := in.BaselineFields
	if len(baseFields) == 0 {
		baseFields = in.Baseline.Observation().Fields().Fields
	}

	fields, err := SelectActivityFields(baseFields, claims, nil)
	if err != nil {
		return ActivityCalculation{}, err
	}

	selected, err := completeActivitySelection(in.Baseline.Observation().Fields(), baseFields, &fields, nil)
	if err != nil {
		return ActivityCalculation{}, err
	}

	conflict := len(fields.Conflicts) > 0

	chosen, err := SelectActivityFields(baseFields, claims, choices)
	if err != nil {
		return ActivityCalculation{}, err
	}

	terms, err := completeActivitySelection(in.Baseline.Observation().Fields(), baseFields, &chosen, choices)
	if err != nil {
		return ActivityCalculation{}, err
	}

	if slices.Contains(chosen.Conflicts, "invariants") {
		conflict = true
	}

	out := ActivityCalculation{
		Terms:            terms,
		Automatic:        selected,
		Fields:           chosen,
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
			if strings.HasPrefix(d.Scope, "evidence/") {
				continue
			}

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

func applyActivityField(value ActivityTerms, d FieldSelection) (ActivityTerms, error) {
	if _, ok := ActivityFieldPolicy(value.Kind(), d.Scope); !ok {
		return ActivityTerms{}, ErrInvalid
	}

	out := ActivityTerms{kind: value.kind}

	var err error
	switch value.kind {
	case TradeActivity:
		out.trade, err = ApplyTradeField(value.trade, d.Scope, d.Text, d.Currency)
	case LifecycleActivity:
		out.lifecycle, err = ApplyLifecycleField(value.lifecycle, d.Scope, d.Text, d.Currency)
	case CashActivity:
		var observation source.ActivityObservation

		observation, err = applyMovementField(value.Observation(), d)
		if err == nil {
			out.cash, err = observation.CashActivity()
		}
	case SecurityTransferActivity:
		var observation source.ActivityObservation

		observation, err = applyMovementField(value.Observation(), d)
		if err == nil {
			out.transfer, err = observation.SecurityTransfer()
		}
	case ConversionActivity:
		out.conversion, err = ApplyConversionField(value.conversion, d.Scope, d.Text, d.Currency)
	default:
		return ActivityTerms{}, ErrInvalid
	}
	// Field edits perform no I/O. Parsing and whole-value constructor failures
	// are application validation outcomes, including dependent term invariants.
	if err != nil {
		return ActivityTerms{}, ErrInvalid
	}

	return out, nil
}

// SourceClaimOutcome compares source assertions before any operator selection.
// Every prior claim participates even when their disagreement leaves the automatic
// projection at an unknown baseline. Such a projection cannot corroborate a new
// contradictory assertion merely by omitting the disputed value.
func SourceClaimOutcome(base, incoming ActivityTerms, prior []ActivityClaim) ObservationOutcome {
	if !compatibleActivityTerms(base, incoming) {
		return ObservationTermsConflict
	}

	for _, claim := range prior {
		if !CompatibleObservations(claim.Value, incoming.Observation()) {
			return ObservationTermsConflict
		}
	}

	return ObservationCorroborated
}

func compatibleActivityTerms(a, b ActivityTerms) bool {
	if a.kind != b.kind {
		return false
	}

	switch a.kind {
	case TradeActivity:
		return Compatible(a.trade, b.trade)
	case LifecycleActivity:
		return CompatibleLifecycle(a.lifecycle, b.lifecycle)
	case SecurityTransferActivity, CashActivity:
		return CompatibleObservations(a.Observation(), b.Observation())
	case ConversionActivity:
		return CompatibleConversion(a.conversion, b.conversion)
	default:
		return false
	}
}
