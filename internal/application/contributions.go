// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package application

import (
	"reflect"
	"slices"

	"go.trippwill.dev/wake/financial"
	"go.trippwill.dev/wake/source"
)

// ObservationContribution considers retained source assertions, never the
// accepted projection. An empty outcome means the subject gains no information.
func ObservationContribution(
	prior []source.ActivityObservation,
	incoming source.ActivityObservation,
) ObservationOutcome {
	if len(prior) == 0 {
		return ObservationAccepted
	}

	covered := observationCovered(prior, incoming)
	conflict := slices.ContainsFunc(prior, func(old source.ActivityObservation) bool {
		return !CompatibleObservations(old, incoming)
	})
	if covered && conflict {
		// A source can contradict itself; an equivalent repeat still adds nothing.
		covered = false
		for _, old := range prior {
			if !CompatibleObservations(old, incoming) &&
				!observationCovered([]source.ActivityObservation{incoming}, old) {
				continue
			}

			covered = observationCovered([]source.ActivityObservation{old}, incoming)
			if covered {
				break
			}
		}
	}

	return contributionOutcome(!covered, conflict)
}

func observationCovered(prior []source.ActivityObservation, incoming source.ActivityObservation) bool {
	f := incoming.Fields()
	for _, ref := range append([]source.Reference{f.Event}, f.EventAliases...) {
		if !ref.IsZero() && !slices.ContainsFunc(prior, func(old source.ActivityObservation) bool {
			g := old.Fields()
			return ref == g.Event || slices.Contains(g.EventAliases, ref)
		}) {
			return false
		}
	}

	if f.EventDistinct && !slices.ContainsFunc(prior, func(old source.ActivityObservation) bool {
		return old.Fields().EventDistinct && old.Fields().Event == f.Event
	}) {
		return false
	}

	for _, field := range f.Fields {
		var previous []source.ActivityField
		for _, old := range prior {
			for _, value := range old.Fields().Fields {
				if value.Key() != field.Key() ||
					!field.Fields().ReportedOn.IsZero() && value.Fields().ReportedOn != field.Fields().ReportedOn {
					continue
				}

				previous = append(previous, value)
			}
		}

		if !activityFieldsCover(previous, field) {
			return false
		}
	}

	return true
}

func activityFieldsCover(prior []source.ActivityField, incoming source.ActivityField) bool {
	if !incoming.Known() {
		return slices.ContainsFunc(prior, func(old source.ActivityField) bool { return !old.Known() })
	}

	var histories []source.ActivityField
	for _, old := range prior {
		if !old.Known() || !CompatibleActivityFields(old, incoming) {
			continue
		}
		// Normalize comparison values without changing retained source order.
		value, err := enrichActivityField(old, old)
		if err != nil {
			continue
		}

		for index, previous := range histories {
			if CompatibleActivityFields(previous, value) {
				if merged, e := enrichActivityField(previous, value); e == nil {
					histories[index] = merged
				}
			}
		}

		histories = append(histories, value)
		for _, previous := range histories {
			value, err = enrichActivityField(previous, incoming)
			if err != nil {
				continue
			}

			x, y := previous.Fields(), value.Fields()
			x.ReportedOn, y.ReportedOn = financial.Date{}, financial.Date{}
			if reflect.DeepEqual(x, y) {
				return true
			}
		}
	}

	return false
}

// InstrumentContribution excludes activity terms and the descriptive details of
// underliers; each underlier owns its own supporting observations.
func InstrumentContribution(
	prior []source.Instrument,
	incoming source.Instrument,
	bindings map[source.Reference]RootBinding,
) (ObservationOutcome, error) {
	value, err := instrumentContributionValue(incoming)
	if err != nil {
		return "", err
	}

	if len(prior) == 0 {
		return ObservationAccepted, nil
	}

	// Verified bindings affect compatibility, never source novelty.
	bound, conflict, err := bindInstrumentContribution(value, bindings)
	if err != nil {
		return "", err
	}

	values := make([]instrumentContribution, 0, len(prior))
	for _, old := range prior {
		previous, e := instrumentContributionValue(old)
		if e != nil {
			return "", e
		}

		values = append(values, previous)

		identity, disputed, e := bindInstrumentContribution(previous, bindings)
		if e != nil {
			return "", e
		}

		conflict = conflict || disputed || !compatibleInstrumentContributions(identity, bound)
	}

	covered := instrumentCovered(values, value)
	if covered && conflict {
		covered = slices.ContainsFunc(values, func(previous instrumentContribution) bool {
			return compatibleInstrumentContributions(previous, value) &&
				instrumentCovered([]instrumentContribution{previous}, value)
		})
	}

	return contributionOutcome(!covered, conflict), nil
}

func contributionOutcome(added, conflict bool) ObservationOutcome {
	if !added {
		return ""
	}

	if conflict {
		return ObservationTermsConflict
	}

	return ObservationEnriched
}

type instrumentContribution struct {
	source.Instrument
	underlierKind      source.InstrumentKind
	underlierReference source.Reference
}

func instrumentContributionValue(value source.Instrument) (instrumentContribution, error) {
	value, _, err := rootTerms(value, nil)
	if err != nil || value.Kind() == "" {
		return instrumentContribution{}, err
	}

	f := value.Fields()
	u := f.Option.Fields().Underlier
	f.Option = source.OptionTerms{}
	value, err = source.NewInstrument(f)

	return instrumentContribution{value, u.Kind(), u.Reference()}, err
}

func bindInstrumentContribution(
	value instrumentContribution,
	bindings map[source.Reference]RootBinding,
) (instrumentContribution, bool, error) {
	own := map[source.Reference]RootBinding{}
	if binding, ok := bindings[value.Reference()]; ok {
		previous, err := instrumentContributionValue(binding.Reported)
		if err != nil {
			return instrumentContribution{}, false, err
		}

		canonical, err := instrumentContributionValue(binding.Canonical)
		if err != nil {
			return instrumentContribution{}, false, err
		}

		own[value.Reference()] = RootBinding{Reported: previous.Instrument, Canonical: canonical.Instrument}
	}

	identity, conflict, err := rootIdentity(value.Instrument, own)
	value.Instrument = identity
	if binding, ok := bindings[value.underlierReference]; ok &&
		binding.Canonical.Reference() != value.underlierReference {
		conflict = conflict || !CompatibleInstrumentKind(value.underlierKind, binding.Reported.Kind()) ||
			!CompatibleInstrumentKind(value.underlierKind, binding.Canonical.Kind())
		value.underlierReference = binding.Canonical.Reference()
	}

	return value, conflict, err
}

func compatibleInstrumentContributions(a, b instrumentContribution) bool {
	return CompatibleInstrument(a.Instrument, b.Instrument) &&
		(a.underlierKind == "" || b.underlierKind == "" || CompatibleInstrumentKind(a.underlierKind, b.underlierKind)) &&
		(a.underlierReference.IsZero() || b.underlierReference.IsZero() || a.underlierReference == b.underlierReference)
}

func instrumentCovered(prior []instrumentContribution, value instrumentContribution) bool {
	if value.Kind() == "" {
		return true
	}

	f := value.Fields()
	kind, ref := f.Kind == source.UnknownInstrument, f.Reference.IsZero()
	underlierKind := value.underlierKind == "" || value.underlierKind == source.UnknownInstrument

	underlierReference := value.underlierReference.IsZero()
	name, ticker, venue := f.Name == "", f.Ticker == "", f.Venue == ""
	identifiers := []source.Identifier{}
	for _, old := range prior {
		g := old.Fields()
		kind, ref = kind || g.Kind == f.Kind, ref || g.Reference == f.Reference
		name, ticker, venue = name ||
			g.Name == f.Name, ticker ||
			g.Ticker == f.Ticker, venue ||
			g.Venue == f.Venue
		identifiers = append(identifiers, g.Identifiers...)
		underlierKind = underlierKind || old.underlierKind == value.underlierKind
		underlierReference = underlierReference || old.underlierReference == value.underlierReference
	}

	return kind && ref && name && ticker && venue && underlierKind && underlierReference &&
		!slices.ContainsFunc(
			f.Identifiers,
			func(id source.Identifier) bool { return !slices.Contains(identifiers, id) },
		)
}
