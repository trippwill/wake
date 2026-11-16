// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package application

import (
	"context"
	"slices"
	"sort"

	"go.trippwill.dev/wake/internal/application/calculation"
	"go.trippwill.dev/wake/source"
)

type ActivityToMatch struct {
	Artifact, Version, Location, Claim, Key string
	Ordinal                                 int64
	Value                                   source.ActivityObservation
}

type ActivityReferenceReader interface {
	EventActivities(context.Context, string, source.Reference) ([]string, error)
	DistinctActivityReferences(context.Context, string) ([]source.Reference, error)
}

type ActivityLinks interface {
	ActivityReferenceReader
	ActivitiesToMatch(context.Context) ([]ActivityToMatch, error)
	ActivityLink(context.Context, ActivityToMatch) (id, outcome string, err error)
	CreateActivity(
		context.Context,
		string,
		ActivityToMatch,
		calculation.ActivityTerms,
		calculation.ActivitySelection,
	) (string, error)
	ActivityBaseline(context.Context, string) (calculation.ActivityTerms, error)
	RetainedActivityClaims(context.Context, string) ([]calculation.ActivityClaim, error)
	SaveEventLink(context.Context, string, string, source.Reference, bool) error
	SaveActivityRelationship(context.Context, string, source.Relationship) error
	RelinkActivity(context.Context, ActivityToMatch, string, string) error
}

type pendingActivity struct {
	row       ActivityToMatch
	value     source.ActivityObservation
	complete  bool
	ambiguous bool
}

func indexPendingSubjects(pending []pendingActivity) calculation.ActivitySubjectIndex {
	var index calculation.ActivitySubjectIndex
	for i, item := range pending {
		if !item.ambiguous {
			index.Add(i, item.value)
		}
	}

	return index
}

func compatibleSubjects(a, b source.ActivityObservation) bool {
	x, y := a.Fields(), b.Fields()
	if x.Account != y.Account {
		return false
	}

	if x.EventDistinct && y.EventDistinct && x.Event.Authority() == y.Event.Authority() &&
		x.Event.Kind() == y.Event.Kind() &&
		x.Event.Scope() == y.Event.Scope() &&
		x.Event != y.Event {
		return false
	}

	for _, r := range calculation.ActivityReferences(a) {
		if slices.Contains(calculation.ActivityReferences(b), r) {
			return true
		}
	}

	return false
}

// A broad alias cannot supply terms to one of several explicitly distinct
// events. Summaries are indexed by reference so unrelated history is not scanned.
func markAmbiguousSubjects(pending []pendingActivity) {
	type namespace [3]string

	type identity struct {
		first     source.Reference
		competing bool
	}

	byReference := map[source.Reference]map[namespace]identity{}
	for _, item := range pending {
		f := item.value.Fields()
		if !f.EventDistinct {
			continue
		}

		space := namespace{f.Event.Authority(), f.Event.Kind(), f.Event.Scope()}
		for _, ref := range calculation.ActivityReferences(item.value) {
			if byReference[ref] == nil {
				byReference[ref] = map[namespace]identity{}
			}

			known := byReference[ref][space]
			if known.first.IsZero() {
				known.first = f.Event
			} else if known.first != f.Event {
				known.competing = true
			}

			byReference[ref][space] = known
		}
	}

	for i := range pending {
		if pending[i].value.Fields().EventDistinct {
			continue
		}

		seen := map[namespace]source.Reference{}
		for _, ref := range calculation.ActivityReferences(pending[i].value) {
			for space, known := range byReference[ref] {
				if known.competing || !seen[space].IsZero() && seen[space] != known.first {
					pending[i].ambiguous = true
				}

				seen[space] = known.first
			}
		}
	}
}

// Each affected source occurrence uses the same identity and completion path.
// Pending observations contain financial assertions, never synthesized trades.
func LinkActivityObservations(
	ctx context.Context,
	links ActivityLinks,
	accountID string,
) ([]string, error) {
	rows, err := links.ActivitiesToMatch(ctx)
	if err != nil || len(rows) == 0 {
		return nil, err
	}

	pending := make([]pendingActivity, 0, len(rows))
	for _, row := range rows {
		value := row.Value

		_, completeErr := calculation.CompleteActivityTerms(value.Fields(), value.Fields().Fields)
		pending = append(pending, pendingActivity{row: row, value: value, complete: completeErr == nil})
	}

	markAmbiguousSubjects(pending)

	// Complete observations establish the existing durable baseline before sparse
	// observations are considered. This does not require a complete row to exist.
	sort.SliceStable(pending, func(i, j int) bool { return pending[i].complete && !pending[j].complete })

	subjects := indexPendingSubjects(pending)

	affected := map[string]bool{}
	for _, item := range pending {
		f := item.value.Fields()

		prior, priorOutcome, e := links.ActivityLink(ctx, item.row)
		if e != nil {
			return nil, e
		}

		if priorOutcome == "unselected" {
			continue
		}

		created := false

		target, ambiguous, e := MatchActivityReferences(
			ctx,
			links,
			accountID,
			f.Event,
			f.EventAliases,
			f.EventDistinct,
		)
		if e != nil {
			return nil, e
		}

		if item.ambiguous {
			target = ""
			ambiguous = true
		}

		outcome := "incomplete"
		if f.Event.IsZero() {
			outcome = "identity-review"
		}

		if ambiguous {
			outcome = "native-review"
		}

		if target == "" && !ambiguous && !f.Event.IsZero() {
			claims := []calculation.ActivityClaim{}
			subject := f

			for _, position := range subjects.Candidates(item.value) {
				candidate := pending[position]
				if !candidate.ambiguous && compatibleSubjects(item.value, candidate.value) {
					claims = append(
						claims,
						calculation.ActivityClaim{
							Key:   candidate.row.Key,
							Value: candidate.value,
						},
					)
				}
			}
			// An individually complete observation establishes its own baseline. For a
			// collection of partials, incompatible required fields remain incomplete.
			base := []source.ActivityField(nil)
			if item.complete {
				base = f.Fields
			}

			selection, e := calculation.SelectActivityFields(base, claims, nil)
			if e != nil {
				return nil, e
			}

			terms, completeErr := calculation.CompleteActivityTerms(subject, selection.Fields)
			if completeErr != nil && len(selection.Conflicts) > 0 {
				outcome = "incomplete-conflict"
			}

			if completeErr == nil {
				selection.Baseline = slices.Clone(selection.Fields)

				target, e = links.CreateActivity(ctx, accountID, item.row, terms, selection)
				if e != nil {
					return nil, e
				}

				created = true
			}
		}

		if target != "" {
			outcome = "enriched"
			if created {
				outcome = "accepted"
			}

			terms, e := links.ActivityBaseline(ctx, target)
			if e != nil {
				return nil, e
			}

			claims, e := links.RetainedActivityClaims(ctx, target)
			if e != nil {
				return nil, e
			}

			compatible := calculation.CompatibleObservations(terms.Observation(), item.value)
			for _, claim := range claims {
				compatible = compatible && calculation.CompatibleObservations(claim.Value, item.value)
			}

			if !compatible {
				outcome = "terms-conflict"
			}

			if prior != "" && prior != target {
				target, outcome = prior, "native-review"
			}

			if compatible {
				for _, ref := range calculation.ActivityReferences(item.value) {
					if e = links.SaveEventLink(
						ctx,
						accountID,
						target,
						ref,
						f.EventDistinct && ref == f.Event,
					); e != nil {
						return nil, e
					}
				}

				for _, field := range f.Fields {
					if field.Name() == source.FieldRelationship && field.Known() {
						if e = links.SaveActivityRelationship(ctx, target, field.Fields().Relationship); e != nil {
							return nil, e
						}
					}
				}
			}

			affected[target] = true
		}

		if prior != "" && target == "" {
			target, outcome = prior, "native-review"
			affected[target] = true
		}

		if e = links.RelinkActivity(ctx, item.row, target, outcome); e != nil {
			return nil, e
		}
	}

	ids := make([]string, 0, len(affected))
	for id := range affected {
		ids = append(ids, id)
	}

	sort.Strings(ids)

	return ids, nil
}

// A distinct event reference distinguishes fills even when an alias is shared.
// Aliases can corroborate an earlier observation with only a trade ID. Several
// possible executions stay ambiguous instead of selecting an arbitrary fill.
func MatchActivityReferences(
	ctx context.Context,
	reader ActivityReferenceReader,
	account string,
	event source.Reference,
	aliases []source.Reference,
	distinct bool,
) (string, bool, error) {
	matches := map[string]bool{}
	for index, ref := range append([]source.Reference{event}, aliases...) {
		ids, err := reader.EventActivities(ctx, account, ref)
		if err != nil {
			return "", false, err
		}

		for _, id := range ids {
			if index > 0 && distinct {
				links, err := reader.DistinctActivityReferences(ctx, id)
				if err != nil {
					return "", false, err
				}

				different := false
				for _, r := range links {
					if r.Authority() == event.Authority() && r.Kind() == event.Kind() && r.Scope() == event.Scope() &&
						r != event {
						different = true
					}
				}

				if different {
					continue
				}
			}

			matches[id] = true
		}

		if index == 0 && len(matches) == 1 && distinct {
			for id := range matches {
				return id, false, nil
			}
		}
	}

	if len(matches) > 1 {
		return "", true, nil
	}

	for id := range matches {
		return id, false, nil
	}

	return "", false, nil
}
