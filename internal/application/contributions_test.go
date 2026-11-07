// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package application

import (
	"testing"

	"go.trippwill.dev/wake/financial"
	"go.trippwill.dev/wake/source"
)

func TestObservationContributionsRetainNewAssertionsAndPrecision(t *testing.T) {
	base := selectionTrade(t, source.Equity, nil, source.DebtTerms{})
	usd, _ := financial.ParseCurrency("USD")
	prior := []source.ActivityObservation{}
	for _, step := range []struct {
		net, at string
		want    ObservationOutcome
	}{
		{"", "2026-10-20T12:00:00Z", ObservationAccepted},
		{"", "2026-10-20T12:00:00Z", ""},
		{"10", "2026-10-20T12:00:00Z", ObservationEnriched},
		{"10", "2026-10-20T12:00:00.000Z", ObservationEnriched},
		{"20", "2026-10-20T12:00:00.000Z", ObservationTermsConflict},
		{"20", "2026-10-20T12:00:00.000Z", ""},
		{"30", "2026-10-20T12:00:00.000Z", ObservationTermsConflict},
		{"10", "2026-10-20T12:00:00Z", ""},
	} {
		f := base.Fields()
		if step.net != "" {
			f.Net, _ = financial.NewMoney(step.net, usd)
		}

		f.ExecutedAt, _ = financial.ParseTimestamp(step.at)

		value, err := source.NewTrade(f)
		if err != nil {
			t.Fatal(err)
		}

		got := ObservationContribution(prior, value.Observation())
		if got != step.want {
			t.Fatalf("net=%s at=%s: got %q, want %q: %v", step.net, step.at, got, step.want, err)
		}

		if got != "" {
			prior = append(prior, value.Observation())
		}
	}
}

func TestObservationContributionsRetainAliasesAndDistinctEventAssertions(t *testing.T) {
	base := selectionTrade(t, source.Equity, nil, source.DebtTerms{}).Observation().Fields()

	base.Event, _ = source.NewReference("synthetic", "execution", "U1", "fill")
	alias, _ := source.NewReference("synthetic", "order", "U1", "shared-order")
	prior := []source.ActivityObservation{}
	for _, step := range []struct {
		alias, distinct bool
		want            ObservationOutcome
	}{
		{false, false, ObservationAccepted},
		{true, false, ObservationEnriched},
		{true, false, ""},
		{true, true, ObservationEnriched},
		{true, true, ""},
		{false, true, ""},
	} {
		input := base
		input.EventAliases = nil
		input.EventDistinct = step.distinct
		if step.alias {
			input.EventAliases = []source.Reference{alias}
		}

		value, err := source.NewActivityObservation(input)
		if err != nil {
			t.Fatal(err)
		}

		outcome := ObservationContribution(prior, value)
		if outcome != step.want {
			t.Fatalf("alias=%t distinct=%t: got %q, want %q", step.alias, step.distinct, outcome, step.want)
		}

		if outcome != "" {
			prior = append(prior, value)
		}
	}

	if len(prior) != 3 {
		t.Fatal("identity assertion history lost or repeated", prior)
	}
}

func TestInstrumentContributionsOwnIdentityAndUnderlierRelationship(t *testing.T) {
	ref, _ := source.NewReference("synthetic", "instrument", "", "option")
	underlier, _ := source.NewReference("synthetic", "instrument", "", "underlier")
	prior := []source.Instrument{}
	for _, step := range []struct {
		name, ticker, multiplier, identifier string
		want                                 ObservationOutcome
	}{
		{"", "", "10", "", ObservationAccepted},
		{"", "", "100", "", ""},
		{"", "NEW", "100", "", ""},
		{"Name", "NEW", "100", "", ObservationEnriched},
		{"Name", "NEW", "100", "US0000000001", ObservationEnriched},
		{"Name", "NEW", "100", "US0000000002", ObservationTermsConflict},
		{"Name", "NEW", "100", "US0000000002", ""},
	} {
		u, err := source.NewInstrument(
			source.InstrumentInput{Kind: source.Equity, Reference: underlier, Ticker: step.ticker},
		)
		if err != nil {
			t.Fatal(err)
		}

		multiplier, _ := financial.NewQuantity(step.multiplier)
		option, _ := source.NewOptionTerms(source.OptionInput{Underlier: u, Multiplier: multiplier})

		f := source.InstrumentInput{Kind: source.Option, Reference: ref, Name: step.name, Option: option}
		if step.identifier != "" {
			id, _ := source.NewIdentifier(source.ISIN, step.identifier, "isin")
			f.Identifiers = []source.Identifier{id}
		}

		value, err := source.NewInstrument(f)
		if err != nil {
			t.Fatal(err)
		}

		got, err := InstrumentContribution(prior, value, nil)
		if err != nil || got != step.want {
			t.Fatalf("%+v: got %q, want %q: %v", step, got, step.want, err)
		}

		if got != "" {
			prior = append(prior, value)
		}
	}
}

func TestInstrumentContributionDoesNotInheritBoundChildDispute(t *testing.T) {
	parent, _ := source.NewReference("fixture", "instrument", "", "parent")
	previous, _ := source.NewReference("fixture", "instrument", "", "previous-child")
	canonical, _ := source.NewReference("fixture", "instrument", "", "canonical-child")
	firstID, _ := source.NewIdentifier(source.ISIN, "US0000000001", "isin")
	otherID, _ := source.NewIdentifier(source.ISIN, "US0000000002", "isin")
	child, _ := source.NewInstrument(
		source.InstrumentInput{
			Kind:        source.Equity,
			Reference:   previous,
			Identifiers: []source.Identifier{firstID},
		},
	)
	bound, _ := source.NewInstrument(
		source.InstrumentInput{
			Kind:        source.Equity,
			Reference:   canonical,
			Identifiers: []source.Identifier{firstID},
		},
	)
	option, _ := source.NewOptionTerms(source.OptionInput{Underlier: child})
	first, _ := source.NewInstrument(
		source.InstrumentInput{Kind: source.Option, Reference: parent, Option: option},
	)
	f := child.Fields()
	f.Identifiers = []source.Identifier{otherID}
	changed, _ := source.NewInstrument(f)
	option, _ = source.NewOptionTerms(source.OptionInput{Underlier: changed})
	next, _ := source.NewInstrument(
		source.InstrumentInput{Kind: source.Option, Reference: parent, Name: "New parent label", Option: option},
	)

	outcome, err := InstrumentContribution([]source.Instrument{first}, next, map[source.Reference]RootBinding{
		previous: {Reported: child, Canonical: bound},
	})
	if err != nil || outcome != ObservationEnriched {
		t.Fatal("child dispute changed the parent's contribution outcome", outcome, err)
	}
}
