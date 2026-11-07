// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package application

import (
	"net/url"
	"reflect"
	"slices"
	"sort"

	"go.trippwill.dev/wake/financial"
	"go.trippwill.dev/wake/source"
)

const ErrIncomplete Error = "activity terms are incomplete"

type ActivitySelection struct {
	Baseline, Fields []source.ActivityField
	Sources          map[string][]string
	Conflicts        []string
	OperatorFields   []string
}

func (v ActivityTerms) Observation() source.ActivityObservation {
	switch v.kind {
	case TradeActivity:
		return v.trade.Observation()
	case LifecycleActivity:
		return v.lifecycle.Observation()
	case CashActivity:
		return v.cash.Observation()
	case SecurityTransferActivity:
		return v.transfer.Observation()
	case ConversionActivity:
		return v.conversion.Observation()
	}

	return source.ActivityObservation{}
}

// CompatibleActivityFields compares one financial role, independently of its
// reporting date. Dates remain attached to the selected source assertion.
func CompatibleActivityFields(a, b source.ActivityField) bool {
	if a.Key() != b.Key() {
		return false
	}

	if !a.Known() || !b.Known() {
		return true
	}

	x, y := a.Fields(), b.Fields()
	switch a.Name() {
	case source.FieldCashAmount,
		source.FieldPositionValue,
		source.FieldTransferCost,
		source.FieldTransferPnL,
		source.FieldGross,
		source.FieldProceeds,
		source.FieldNet,
		source.FieldNetBillable,
		source.FieldAccruedInterest,
		source.FieldRealizedPnL,
		source.FieldReportedCostBasis:
		return x.Money.Equal(y.Money)
	case source.FieldPrice, source.FieldRate, source.FieldTransferPrice:
		return x.Price.Equal(y.Price) &&
			(x.PriceBasis == "" || y.PriceBasis == "" || x.PriceBasis == y.PriceBasis)
	case source.FieldQuantity:
		return x.Quantity.Equal(y.Quantity) && x.QuantityUnit == y.QuantityUnit
	case source.FieldInstrument:
		if !x.Instrument.Reference().IsZero() && !y.Instrument.Reference().IsZero() &&
			x.Instrument.Reference() != y.Instrument.Reference() {
			return false
		}

		left, right := x.Instrument, y.Instrument
		if left.Reference() == right.Reference() && !left.Reference().IsZero() {
			if left.Kind() == source.UnknownInstrument {
				f := left.Fields()
				f.Kind = right.Kind()
				left, _ = source.NewInstrument(f)
			}

			if right.Kind() == source.UnknownInstrument {
				f := right.Fields()
				f.Kind = left.Kind()
				right, _ = source.NewInstrument(f)
			}
		}

		return CompatibleInstrument(left, right)
	case source.FieldExecutedAt:
		return x.Timestamp.Contains(y.Timestamp) || y.Timestamp.Contains(x.Timestamp)
	case source.FieldCharge:
		return slices.Equal(x.ChargeParents, y.ChargeParents) && equalCharge(x.Charge, y.Charge)
	case source.FieldSent, source.FieldReceived:
		return equalAmount(x.CurrencyAmount, y.CurrencyAmount) ||
			x.CurrencyAmount.Currency() == y.CurrencyAmount.Currency() &&
				(x.CurrencyAmount.Amount() == "" || y.CurrencyAmount.Amount() == "")
	}
	// Report dates describe assertions, never automatic source revision order.
	x.ReportedOn = y.ReportedOn

	return reflect.DeepEqual(x, y)
}

func CompatibleObservations(a, b source.ActivityObservation) bool {
	if a.Fields().Account != b.Fields().Account ||
		!compatibleChargeAssertions(a.Fields().Fields, b.Fields().Fields) {
		return false
	}

	for _, pair := range [][2]source.ActivityObservation{{a, b}, {b, a}} {
		if family, ok := pair[0].Field(source.FieldFamily); ok && family.Known() {
			for _, field := range pair[1].Fields().Fields {
				if field.Known() && !field.AppliesTo(family.Fields().Text) {
					return false
				}
			}
		}
	}

	values := map[string]source.ActivityField{}
	for _, f := range a.Fields().Fields {
		values[f.Key()] = f
	}

	for _, f := range b.Fields().Fields {
		if old, ok := values[f.Key()]; ok && !CompatibleActivityFields(old, f) {
			return false
		}
	}

	return true
}

func compatibleChargeAssertions(a, b []source.ActivityField) bool {
	for _, left := range a {
		if left.Name() != source.FieldCharge || !left.Known() {
			continue
		}

		for _, right := range b {
			if right.Name() != source.FieldCharge || !right.Known() {
				continue
			}

			x, y := left.Fields(), right.Fields()

			leftPath := append(slices.Clone(x.ChargeParents), x.Charge.Role())
			rightPath := append(slices.Clone(y.ChargeParents), y.Charge.Role())
			for i := 0; i < min(len(leftPath), len(rightPath)); i++ {
				if leftPath[i].Field() != rightPath[i].Field() {
					break
				}

				if leftPath[i].Category() != rightPath[i].Category() {
					return false
				}
			}

			if left.Key() == right.Key() && !CompatibleActivityFields(left, right) {
				return false
			}
		}
	}

	return true
}

type activityFieldClaim struct {
	value source.ActivityField
	key   string
}

// SelectActivityFields compares only retained source assertions. A field absent
// from the durable baseline stays unknown when incompatible enrichments compete.
func SelectActivityFields(
	base []source.ActivityField,
	claims []ActivityClaim,
	choices map[string]string,
) (ActivitySelection, error) {
	out := ActivitySelection{Baseline: slices.Clone(base), Sources: map[string][]string{}}
	baseline := map[string]source.ActivityField{}
	all := map[string][]activityFieldClaim{}
	conflicts := map[string]bool{}

	for _, f := range base {
		baseline[f.Key()] = f
	}

	family, hasFamily := baseline["family"]
	for _, claim := range claims {
		if claim.Key == "" {
			return out, ErrInvalid
		}

		if _, err := source.NewActivityObservation(claim.Value.Fields()); err != nil {
			return out, ErrInvalid
		}

		if claim.LinkConflict {
			conflicts["identity"] = true
		}

		if hasFamily && family.Known() {
			if f, ok := claim.Value.Field(
				source.FieldFamily,
			); ok && f.Known() &&
				!CompatibleActivityFields(family, f) {
				conflicts["family"] = true
				continue
			}
		}

		if instrument, ok := baseline["instrument"]; ok && instrument.Known() {
			if f, present := claim.Value.Field(source.FieldInstrument); present && f.Known() {
				a, b := instrument.Fields().Instrument.Reference(), f.Fields().Instrument.Reference()
				if !a.IsZero() && !b.IsZero() && a != b {
					conflicts["identity"] = true
					continue
				}
			}
		}

		for _, f := range claim.Value.Fields().Fields {
			if hasFamily && family.Known() && f.Known() && !f.AppliesTo(family.Fields().Text) {
				conflicts[f.Key()] = true
				continue
			}

			all[f.Key()] = append(all[f.Key()], activityFieldClaim{f, claim.Key})
		}
	}

	keys := map[string]bool{}
	for k := range baseline {
		keys[k] = true
	}

	for k := range all {
		keys[k] = true
	}

	ordered := make([]string, 0, len(keys))
	for k := range keys {
		ordered = append(ordered, k)
	}

	sort.Strings(ordered)

	for _, key := range ordered {
		value, support, disputed, err := selectActivityField(baseline[key], all[key], choices[key])
		if err != nil {
			return out, err
		}

		if disputed {
			conflicts[key] = true
		}

		if _, ok := choices[key]; ok {
			out.OperatorFields = append(out.OperatorFields, key)
		}

		if value.Name() != "" {
			out.Fields = append(out.Fields, value)
			out.Sources[key] = support
		}
	}

	for key := range choices {
		if !keys[key] {
			return out, ErrInvalid
		}
	}

	if err := selectChargeAncestors(&out, baseline, all, choices, conflicts); err != nil {
		return out, err
	}

	for key := range conflicts {
		out.Conflicts = append(out.Conflicts, key)
	}

	sort.Strings(out.Conflicts)
	sort.Slice(out.Baseline, func(i, j int) bool { return out.Baseline[i].Key() < out.Baseline[j].Key() })

	return out, nil
}

func chargeCategories(field source.ActivityField) map[string]source.ChargeCategory {
	roles := map[string]source.ChargeCategory{}
	if field.Name() != source.FieldCharge || !field.Known() {
		return roles
	}

	f := field.Fields()
	path := "charge"
	for _, role := range append(slices.Clone(f.ChargeParents), f.Charge.Role()) {
		path += "/" + url.PathEscape(role.Field())
		roles[path] = role.Category()
	}

	return roles
}

// Shared ancestors couple otherwise independent monetary assertions. A missing
// baseline category stays unknown when enrichments disagree; explicit choices
// replace incompatible baseline descendants as one coherent subtree.
func selectChargeAncestors(
	out *ActivitySelection,
	baseline map[string]source.ActivityField,
	all map[string][]activityFieldClaim,
	choices map[string]string,
	conflicts map[string]bool,
) error {
	categories := map[string]map[source.ChargeCategory]bool{}

	preferred := map[string]source.ChargeCategory{}
	add := func(field source.ActivityField) {
		for path, category := range chargeCategories(field) {
			if categories[path] == nil {
				categories[path] = map[source.ChargeCategory]bool{}
			}

			categories[path][category] = true
		}
	}
	for _, field := range baseline {
		add(field)

		for path, category := range chargeCategories(field) {
			preferred[path] = category
		}
	}

	for _, claims := range all {
		for _, claim := range claims {
			add(claim.value)
		}
	}

	chosen := map[string]source.ChargeCategory{}
	for _, field := range out.Fields {
		if _, ok := choices[field.Key()]; !ok {
			continue
		}

		for path, category := range chargeCategories(field) {
			if old, ok := chosen[path]; ok && old != category {
				return ErrInvalid
			}

			chosen[path], preferred[path] = category, category
		}
	}

	accept := func(field source.ActivityField) bool {
		keep := true
		for path, category := range chargeCategories(field) {
			if len(categories[path]) > 1 {
				conflicts[field.Key()] = true

				if preferred[path] != category {
					keep = false
				}
			}
		}

		return keep
	}
	for _, claims := range all {
		for _, claim := range claims {
			accept(claim.value)
		}
	}

	out.Fields = slices.DeleteFunc(out.Fields, func(field source.ActivityField) bool {
		if accept(field) {
			return false
		}

		delete(out.Sources, field.Key())

		return true
	})

	return nil
}

func sameActivityAssertion(a, b source.ActivityField) bool {
	left, right := a.Fields(), b.Fields()
	left.ReportedOn, right.ReportedOn = financial.Date{}, financial.Date{}

	return reflect.DeepEqual(left, right)
}

func selectActivityField(
	base source.ActivityField,
	claims []activityFieldClaim,
	choice string,
) (source.ActivityField, []string, bool, error) {
	known := []activityFieldClaim{}
	disputed := false
	for _, claim := range claims {
		if !claim.value.Known() || disputed {
			continue
		}

		if base.Known() && !CompatibleActivityFields(base, claim.value) {
			disputed = true
			continue
		}

		duplicate := false
		for _, prior := range known {
			if sameActivityAssertion(prior.value, claim.value) {
				duplicate = true
				break
			}

			if !disputed && !CompatibleActivityFields(prior.value, claim.value) {
				disputed = true
				break
			}
		}

		if !duplicate && !disputed {
			known = append(known, claim)
		}
	}

	selected := base
	if !disputed {
		for _, claim := range known {
			value, err := enrichActivityField(selected, claim.value)
			if err != nil {
				selected = base
				disputed = true

				break
			}

			selected = value
		}
	}

	if selected.Name() == "" && len(claims) > 0 {
		selected = claims[0].value
		if disputed {
			name := selected.Name()
			selected = source.ActivityField{}
			if name != source.FieldCharge && name != source.FieldRelationship {
				var err error

				selected, err = source.NewActivityField(source.ActivityFieldInput{Name: name, Unknown: true})
				if err != nil {
					return selected, nil, disputed, err
				}
			}
		}
	}

	support := []string{}
	// Exact attribution includes report date and decimal text. Compatible repeats
	// cannot silently replace the source used for the selected value.
	for _, claim := range claims {
		if reflect.DeepEqual(selected.Fields(), claim.value.Fields()) {
			support = append(support, claim.key)
			break
		}
	}

	if selected.Name() == source.FieldInstrument {
		for _, claim := range claims {
			if claim.value.Known() && CompatibleActivityFields(selected, claim.value) &&
				!slices.Contains(support, claim.key) {
				support = append(support, claim.key)
			}
		}
	}

	if choice != "" {
		found := false
		for _, claim := range claims {
			if claim.key != choice {
				continue
			}

			found = true

			candidate := claim.value
			if candidate.Name() == source.FieldInstrument && candidate.Known() && selected.Known() &&
				CompatibleActivityFields(candidate, selected) {
				f := candidate.Fields()

				value, err := EnrichInstrument(f.Instrument, selected.Fields().Instrument)
				if err != nil {
					return selected, nil, disputed, ErrInvalid
				}

				f.Instrument = value

				candidate, err = source.NewActivityField(f)
				if err != nil {
					return selected, nil, disputed, err
				}
			}

			selected = candidate
			support = []string{choice}
			if candidate.Name() == source.FieldInstrument && candidate.Known() {
				for _, prior := range claims {
					if prior.key != choice && prior.value.Known() && CompatibleActivityFields(candidate, prior.value) &&
						!slices.Contains(support, prior.key) {
						support = append(support, prior.key)
					}
				}
			}

			break
		}

		if !found {
			return selected, nil, disputed, ErrInvalid
		}
	}

	return selected, support, disputed, nil
}

func enrichActivityField(base, incoming source.ActivityField) (source.ActivityField, error) {
	if !base.Known() {
		return incoming, nil
	}

	if !incoming.Known() {
		return base, nil
	}

	f, g := base.Fields(), incoming.Fields()
	switch base.Name() {
	case source.FieldInstrument:
		value, err := EnrichInstrument(f.Instrument, g.Instrument)
		if err != nil {
			return base, err
		}

		f.Instrument = value

		return source.NewActivityField(f)
	case source.FieldExecutedAt:
		if f.Timestamp.Contains(g.Timestamp) {
			return incoming, nil
		}
	case source.FieldSent, source.FieldReceived:
		if f.CurrencyAmount.Amount() == "" && g.CurrencyAmount.Amount() != "" {
			return incoming, nil
		}
	case source.FieldPrice:
		if f.PriceBasis == "" && g.PriceBasis != "" {
			f.PriceBasis = g.PriceBasis
			return source.NewActivityField(f)
		}
	}

	return base, nil
}

// CompleteActivityTerms enforces whole-value invariants only after identity and
// compatible source assertions have supplied the required terms.
func CompleteActivityTerms(
	subject source.ActivityObservationInput,
	fields []source.ActivityField,
) (ActivityTerms, error) {
	subject.Fields = fields

	o, err := source.NewActivityObservation(subject)
	if err != nil {
		return ActivityTerms{}, ErrIncomplete
	}

	family, ok := o.Field(source.FieldFamily)
	if !ok || !family.Known() {
		return ActivityTerms{}, ErrIncomplete
	}

	switch family.Fields().Text {
	case "trade":
		v, e := o.Trade()
		if e != nil {
			return ActivityTerms{}, ErrIncomplete
		}

		return TradeTerms(v)
	case "lifecycle":
		v, e := o.Lifecycle()
		if e != nil {
			return ActivityTerms{}, ErrIncomplete
		}

		return LifecycleTerms(v)
	case "cash":
		v, e := o.CashActivity()
		if e != nil {
			return ActivityTerms{}, ErrIncomplete
		}

		return CashTerms(v)
	case "security-transfer":
		v, e := o.SecurityTransfer()
		if e != nil {
			return ActivityTerms{}, ErrIncomplete
		}

		return SecurityTransferTerms(v)
	case "conversion":
		v, e := o.Conversion()
		if e != nil {
			return ActivityTerms{}, ErrIncomplete
		}

		return ConversionTerms(v)
	}

	return ActivityTerms{}, ErrIncomplete
}

type activityFieldGroup struct {
	fields []source.ActivityField
	chosen bool
}

// A conversion rate and its base currency form one constructor invariant. Other
// assertions remain independent, including reporting amounts and charge roles.
func groupActivityFields(fields []source.ActivityField, choices map[string]string) []activityFieldGroup {
	groups := []activityFieldGroup{}
	positions := map[string]int{}
	for _, field := range fields {
		key := field.Key()
		if field.Name() == source.FieldRateBase {
			key = string(source.FieldRate)
		}

		position, ok := positions[key]
		if !ok {
			position = len(groups)
			positions[key] = position

			groups = append(groups, activityFieldGroup{})
		}

		groups[position].fields = append(groups[position].fields, field)
		if _, ok := choices[field.Key()]; ok {
			groups[position].chosen = true
		}
	}

	return groups
}

func overlayActivityFields(fields, values []source.ActivityField) []source.ActivityField {
	result := slices.Clone(fields)
	for _, value := range values {
		position := slices.IndexFunc(
			result,
			func(field source.ActivityField) bool { return field.Key() == value.Key() },
		)
		if position < 0 {
			result = append(result, value)
		} else {
			result[position] = value
		}
	}

	return result
}

// completeActivitySelection keeps independently valid enrichments when an
// automatic assertion cannot satisfy the complete-term constructors. Coupled
// fields are evaluated together, including automatic companions of an explicit
// choice. Unrelated incomplete evidence remains reviewable without invalidating
// valid choices or discarding a complete coupled enrichment.
func completeActivitySelection(
	subject source.ActivityObservationInput,
	baseline []source.ActivityField,
	selected *ActivitySelection,
	choices map[string]string,
) (ActivityTerms, error) {
	terms, err := CompleteActivityTerms(subject, selected.Fields)
	if err == nil {
		return terms, nil
	}

	groups := groupActivityFields(selected.Fields, choices)
	retained := slices.Clone(baseline)
	for _, group := range groups {
		if group.chosen {
			retained = overlayActivityFields(retained, group.fields)
		}
	}

	terms, err = CompleteActivityTerms(subject, retained)
	if err != nil {
		return ActivityTerms{}, ErrInvalid
	}

	for _, group := range groups {
		if group.chosen {
			continue
		}

		candidate := overlayActivityFields(retained, group.fields)

		complete, e := CompleteActivityTerms(subject, candidate)
		if e != nil {
			for _, field := range group.fields {
				delete(selected.Sources, field.Key())
			}

			continue
		}

		retained, terms = candidate, complete
	}

	sort.Slice(retained, func(i, j int) bool { return retained[i].Key() < retained[j].Key() })
	selected.Fields = retained
	selected.Conflicts = append(selected.Conflicts, "invariants")

	return terms, nil
}
