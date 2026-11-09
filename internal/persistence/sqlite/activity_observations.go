// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"sort"

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/host"
	"go.trippwill.dev/wake/internal/persistence/sqlite/sqlc"
	"go.trippwill.dev/wake/source"
)

func activityReferences(value source.ActivityObservation) []source.Reference {
	f := value.Fields()
	out := []source.Reference{}
	if !f.Event.IsZero() {
		out = append(out, f.Event)
	}

	return append(out, f.EventAliases...)
}

func admitActivity(
	ctx context.Context,
	q *sqlc.Queries,
	account application.Account,
	retained host.Retained,
	version string,
	record Record,
) error {
	f := record.Observation.Fields()
	o := sqlc.PutObservationParams{
		ArtifactID: retained.ID,
		Version:    version,
		Location:   record.Location,
		Outcome:    "unselected",
		Rule:       application.RepeatRule,
	}
	if account.AuthorizesSource(retained.Connection, retained.Provider, f.Account) {
		o.Outcome = "identity-review"
		if !f.Event.IsZero() {
			o.Outcome = "incomplete"
		}

		for _, ref := range activityReferences(record.Observation) {
			if err := q.PutActivityReference(
				ctx,
				sqlc.PutActivityReferenceParams{
					ClaimID:   record.ClaimID,
					Ordinal:   record.Ordinal,
					AccountID: account.ID(),
					Reference: referenceKey(ref),
				},
			); err != nil {
				return err
			}
		}
	}

	return q.PutObservation(ctx, o)
}

type pendingActivity struct {
	row       sqlc.ActivityToMatchRow
	value     source.ActivityObservation
	complete  bool
	ambiguous bool
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

	for _, r := range activityReferences(a) {
		if slices.Contains(activityReferences(b), r) {
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
		for _, ref := range activityReferences(item.value) {
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
		for _, ref := range activityReferences(pending[i].value) {
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
func linkActivityObservations(
	ctx context.Context,
	q *sqlc.Queries,
	artifact, accountID string,
) ([]string, error) {
	rows, err := q.ActivityToMatch(ctx, artifact)
	if err != nil || len(rows) == 0 {
		return nil, err
	}

	pending := make([]pendingActivity, 0, len(rows))
	for _, row := range rows {
		data, e := claimData("activity", row.Data, row.Location)
		if e != nil {
			return nil, e
		}

		value, e := decodeObservation(data)
		if e != nil {
			return nil, e
		}

		_, completeErr := application.CompleteActivityTerms(value.Fields(), value.Fields().Fields)
		pending = append(pending, pendingActivity{row: row, value: value, complete: completeErr == nil})
	}

	markAmbiguousSubjects(pending)

	// Complete observations establish the existing durable baseline before sparse
	// observations are considered. This does not require a complete row to exist.
	sort.SliceStable(pending, func(i, j int) bool { return pending[i].complete && !pending[j].complete })

	subjects := newActivitySubjectIndex(pending)

	affected := map[string]bool{}
	for _, item := range pending {
		f := item.value.Fields()

		prior, e := q.ActivityObservation(
			ctx,
			sqlc.ActivityObservationParams{
				ArtifactID: item.row.ArtifactID,
				Version:    item.row.Version,
				Location:   item.row.Location,
			},
		)
		if e != nil {
			return nil, e
		}

		if prior.Outcome == "unselected" {
			continue
		}

		created := false

		target, ambiguous, e := matchReferences(ctx, q, accountID, f.Event, f.EventAliases, f.EventDistinct)
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
			claims := []application.ActivityClaim{}
			subject := f

			for _, position := range subjects.candidates(item.value) {
				candidate := pending[position]
				if !candidate.ambiguous && compatibleSubjects(item.value, candidate.value) {
					claims = append(
						claims,
						application.ActivityClaim{
							Key: observationKey(
								sqlc.RecordValue{
									ArtifactID: candidate.row.ArtifactID,
									Version:    candidate.row.Version,
									Location:   candidate.row.Location,
								},
							),
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

			selection, e := application.SelectActivityFields(base, claims, nil)
			if e != nil {
				return nil, e
			}

			terms, completeErr := application.CompleteActivityTerms(subject, selection.Fields)
			if completeErr != nil && len(selection.Conflicts) > 0 {
				outcome = "incomplete-conflict"
			}

			if completeErr == nil {
				selection.Baseline = slices.Clone(selection.Fields)

				target, e = createActivity(
					ctx,
					q,
					accountID,
					item.row.ArtifactID,
					item.row.Location,
					terms,
					selection,
				)
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

			row, e := q.Execution(ctx, target)
			if e != nil {
				return nil, e
			}

			base, e := q.ActivityBase(ctx, target)
			if e != nil {
				return nil, e
			}

			terms, e := decodeActivityTerms(row.Kind, base.Data)
			if e != nil {
				return nil, e
			}

			claims, e := retainedActivityClaims(ctx, q, target)
			if e != nil {
				return nil, e
			}

			compatible := application.CompatibleObservations(terms.Observation(), item.value)
			for _, claim := range claims {
				compatible = compatible && application.CompatibleObservations(claim.Value, item.value)
			}

			if !compatible {
				outcome = "terms-conflict"
			}

			if prior.ExecutionID.Valid && prior.ExecutionID.String != target {
				target, outcome = prior.ExecutionID.String, "native-review"
			}

			if compatible {
				for _, ref := range activityReferences(item.value) {
					if e = q.PutEventLink(
						ctx,
						sqlc.PutEventLinkParams{
							AccountID:     accountID,
							Reference:     referenceKey(ref),
							ExecutionID:   target,
							DistinctEvent: boolInt(f.EventDistinct && ref == f.Event),
						},
					); e != nil {
						return nil, e
					}
				}

				for _, field := range f.Fields {
					if field.Name() == source.FieldRelationship && field.Known() {
						if e = putRelationships(
							ctx,
							q,
							target,
							[]source.Relationship{field.Fields().Relationship},
						); e != nil {
							return nil, e
						}
					}
				}
			}

			affected[target] = true
		}

		if prior.ExecutionID.Valid && target == "" {
			target, outcome = prior.ExecutionID.String, "native-review"
			affected[target] = true
		}

		if e = q.RelinkActivityObservation(
			ctx,
			sqlc.RelinkActivityObservationParams{
				ExecutionID: sql.NullString{String: target, Valid: target != ""},
				Outcome:     outcome,
				ClaimID:     item.row.ClaimID,
				Ordinal:     item.row.Ordinal,
			},
		); e != nil {
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

func createActivity(
	ctx context.Context,
	q *sqlc.Queries,
	account, artifact, location string,
	terms application.ActivityTerms,
	selection application.ActivitySelection,
) (string, error) {
	id := rand.Text()
	instrument, listing := "", ""

	var err error

	currency := ""
	if terms.Kind() == application.TradeActivity {
		currency = terms.Trade().Fields().Price.Currency().String()
	}

	if terms.Instrument().Kind() != "" {
		instrument, listing, err = acceptInstrument(ctx, q, artifact, location, terms.Instrument(), currency)
		if err != nil {
			return "", err
		}
	}

	data, err := encodeActivityTerms(terms)
	if err != nil {
		return "", err
	}

	switch terms.Kind() {
	case application.SecurityTransferActivity, application.CashActivity:
		err = q.PutMovementActivity(
			ctx,
			sqlc.PutMovementActivityParams{
				Kind:         string(terms.Kind()),
				ID:           id,
				AccountID:    account,
				InstrumentID: sql.NullString{String: instrument, Valid: instrument != ""},
				Accepted:     data,
				Rule:         application.RepeatRule,
			},
		)
	case application.TradeActivity:
		err = q.PutExecution(
			ctx,
			sqlc.PutExecutionParams{
				ID:           id,
				AccountID:    account,
				InstrumentID: sql.NullString{String: instrument, Valid: instrument != ""},
				ListingID:    sql.NullString{String: listing, Valid: listing != ""},
				Accepted:     data,
				Rule:         application.RepeatRule,
			},
		)
	case application.LifecycleActivity:
		err = q.PutLifecycleActivity(
			ctx,
			sqlc.PutLifecycleActivityParams{
				ID:           id,
				AccountID:    account,
				InstrumentID: sql.NullString{String: instrument, Valid: instrument != ""},
				Accepted:     data,
				Rule:         application.RepeatRule,
			},
		)
	case application.ConversionActivity:
		err = q.PutConversionActivity(
			ctx,
			sqlc.PutConversionActivityParams{
				ID:        id,
				AccountID: account,
				Accepted:  data,
				Rule:      application.RepeatRule,
			},
		)
	}

	if err != nil {
		return "", err
	}

	if err = q.PutActivityBase(
		ctx,
		sqlc.PutActivityBaseParams{
			ActivityID:   id,
			Data:         data,
			InstrumentID: sql.NullString{String: instrument, Valid: instrument != ""},
			ListingID:    sql.NullString{String: listing, Valid: listing != ""},
		},
	); err != nil {
		return "", err
	}

	if err = persistActivitySelection(ctx, q, id, selection); err != nil {
		return "", err
	}

	return id, nil
}

type activitySelectionData struct {
	Fields                    []activityFieldData
	Sources                   map[string][]string
	Conflicts, OperatorFields []string
}

func encodeActivityFields(fields []source.ActivityField) ([]byte, error) {
	out := make([]activityFieldData, 0, len(fields))
	for _, f := range fields {
		out = append(out, activityFieldWire(f))
	}

	return json.Marshal(out)
}

func decodeActivityFields(data []byte) ([]source.ActivityField, error) {
	var values []activityFieldData
	if json.Unmarshal(data, &values) != nil {
		return nil, ErrCorrupt
	}

	out := make([]source.ActivityField, 0, len(values))
	for _, v := range values {
		f, e := v.field()
		if e != nil {
			return nil, ErrCorrupt
		}

		out = append(out, f)
	}

	return out, nil
}

func persistActivitySelection(
	ctx context.Context,
	q *sqlc.Queries,
	id string,
	value application.ActivitySelection,
) error {
	base, err := encodeActivityFields(value.Baseline)
	if err != nil {
		return err
	}

	d := activitySelectionData{
		Sources:        value.Sources,
		Conflicts:      value.Conflicts,
		OperatorFields: value.OperatorFields,
	}
	for _, f := range value.Fields {
		d.Fields = append(d.Fields, activityFieldWire(f))
	}

	data, err := json.Marshal(d)
	if err != nil {
		return err
	}

	return q.SaveActivitySelection(
		ctx,
		sqlc.SaveActivitySelectionParams{ActivityID: id, Baseline: base, Selected: data},
	)
}

func loadActivitySelection(
	ctx context.Context,
	q *sqlc.Queries,
	id string,
) (application.ActivitySelection, error) {
	row, err := q.ActivitySelection(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return application.ActivitySelection{}, nil
	}

	if err != nil {
		return application.ActivitySelection{}, err
	}

	var d activitySelectionData
	if json.Unmarshal(row.Selected, &d) != nil {
		return application.ActivitySelection{}, ErrCorrupt
	}

	out := application.ActivitySelection{
		Sources:        d.Sources,
		Conflicts:      d.Conflicts,
		OperatorFields: d.OperatorFields,
	}

	out.Baseline, err = decodeActivityFields(row.Baseline)
	if err != nil {
		return out, err
	}

	for _, v := range d.Fields {
		f, e := v.field()
		if e != nil {
			return out, ErrCorrupt
		}

		out.Fields = append(out.Fields, f)
	}

	return out, nil
}
