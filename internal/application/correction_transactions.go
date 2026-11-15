// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package application

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.trippwill.dev/wake/source"
)

// CorrectionStore owns transaction lifetime, including publication before commit.
// Capabilities are valid only in their callback. They cannot commit or nest work.
type CorrectionStore interface {
	ViewActivityCorrection(context.Context, func(ActivityCorrectionReader) error) error
	UpdateActivityCorrection(context.Context, func(ActivityCorrectionWriter) error) error
	CheckInstrumentCorrection(context.Context, Decision) error
	SaveInstrumentDecision(context.Context, Decision, string, time.Time) error
}

type ActivityCorrectionReader interface {
	ActivityRevision(context.Context, string) (int64, error)
	ActivityDecisions(context.Context, string) ([]DecisionRecord, error)
	ActivityObservation(context.Context, string, string) (source.ActivityObservation, error)
	ActivityInputs(context.Context, string, []FieldSelection) (ActivityCalculationInput, error)
}

type ActivityCorrectionWriter interface {
	ActivityCorrectionReader
	Submission(context.Context, string) (DecisionInput, string, bool, error)
	InsertDecision(context.Context, DecisionInput, DecisionInput, string, time.Time, string) error
	SaveActivity(context.Context, string, ActivityCalculation) error
}

func CheckCorrection(ctx context.Context, store CorrectionStore, decision Decision) error {
	f := decision.Fields()
	if _, err := NewDecision(f); err != nil {
		return err
	}

	if f.TargetKind == "instrument" {
		return store.CheckInstrumentCorrection(ctx, decision)
	}

	return store.ViewActivityCorrection(ctx, func(reader ActivityCorrectionReader) error {
		_, _, _, err := prepareActivityCorrection(ctx, reader, f)
		return err
	})
}

func SaveDecision(
	ctx context.Context,
	store CorrectionStore,
	decision Decision,
	actor string,
	at time.Time,
) error {
	f := decision.Fields()
	if _, err := NewDecision(f); err != nil {
		return err
	}

	if actor == "" || len(actor) > 256 || at.IsZero() {
		return ErrInvalid
	}

	if f.TargetKind == "instrument" {
		return store.SaveInstrumentDecision(ctx, decision, actor, at)
	}

	return store.UpdateActivityCorrection(ctx, func(writer ActivityCorrectionWriter) error {
		prior, previousActor, found, err := writer.Submission(ctx, f.Submission)
		if err != nil {
			return err
		}

		if found {
			if prior == f && previousActor == actor {
				return nil
			}

			return ErrStale
		}

		calculation, fields, active, err := prepareActivityCorrection(ctx, writer, f)
		if err != nil {
			return err
		}

		for i, field := range fields {
			if i > 0 {
				field.Submission = f.Submission + "/" + strconv.Itoa(i)
			}

			if err = writer.InsertDecision(ctx, field, f, actor, at, active[field.Scope].ID); err != nil {
				return err
			}
		}

		return writer.SaveActivity(ctx, f.TargetID, calculation)
	})
}

func prepareActivityCorrection(
	ctx context.Context,
	reader ActivityCorrectionReader,
	f DecisionInput,
) (ActivityCalculation, []DecisionInput, map[string]DecisionRecord, error) {
	revision, err := reader.ActivityRevision(ctx, f.TargetID)
	if err != nil {
		return ActivityCalculation{}, nil, nil, err
	}

	if revision != f.ExpectedRevision {
		return ActivityCalculation{}, nil, nil, ErrStale
	}

	records, err := reader.ActivityDecisions(ctx, f.TargetID)
	if err != nil {
		return ActivityCalculation{}, nil, nil, err
	}

	active := map[string]DecisionRecord{}
	for _, record := range records {
		if record.Active {
			active[record.Scope] = record
		}
	}

	fields, err := activityCorrectionFields(ctx, reader, f, active)
	if err != nil {
		return ActivityCalculation{}, nil, nil, err
	}

	prospective := map[string]FieldSelection{}
	for scope, record := range active {
		prospective[scope] = FieldSelection{Scope: scope, Text: record.Text, Currency: record.Currency}
	}

	for _, field := range fields {
		if field.Action == "release" {
			if _, ok := active[field.Scope]; !ok {
				return ActivityCalculation{}, nil, nil, ErrInvalid
			}

			delete(prospective, field.Scope)
		} else {
			prospective[field.Scope] = FieldSelection{
				Scope:    field.Scope,
				Text:     field.Text,
				Currency: field.Currency,
			}
		}
	}

	selections := make([]FieldSelection, 0, len(prospective))
	for _, selection := range prospective {
		selections = append(selections, selection)
	}

	sort.Slice(selections, func(i, j int) bool { return selections[i].Scope < selections[j].Scope })

	in, err := reader.ActivityInputs(ctx, f.TargetID, selections)
	if err != nil {
		return ActivityCalculation{}, nil, nil, err
	}

	calculation, err := CalculateActivity(in)

	return calculation, fields, active, err
}

// An observation selection expands only its present fields into independently
// releasable decisions. Releasing it removes the active evidence scopes.
func activityCorrectionFields(
	ctx context.Context,
	reader ActivityCorrectionReader,
	f DecisionInput,
	active map[string]DecisionRecord,
) ([]DecisionInput, error) {
	if f.Scope != "observation" {
		return []DecisionInput{f}, nil
	}

	scopes := []string{}
	if f.Action == "release" {
		for scope := range active {
			if strings.HasPrefix(scope, "evidence/") {
				scopes = append(scopes, scope)
			}
		}
	} else {
		observation, err := reader.ActivityObservation(ctx, f.TargetID, f.Text)
		if err != nil {
			return nil, err
		}

		for _, field := range observation.Fields().Fields {
			scopes = append(scopes, "evidence/"+field.Key())
		}
	}

	if len(scopes) == 0 {
		return nil, ErrInvalid
	}

	sort.Strings(scopes)

	fields := make([]DecisionInput, 0, len(scopes))
	for _, scope := range scopes {
		field := f
		field.Scope = scope
		fields = append(fields, field)
	}

	return fields, nil
}
