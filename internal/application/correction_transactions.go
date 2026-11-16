// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package application

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.trippwill.dev/wake/internal/application/calculation"
	"go.trippwill.dev/wake/source"
)

const ErrStale calculation.Error = "the entity changed; reload and review before submitting"

// CorrectionStore owns transaction lifetime, including publication before commit.
// Capabilities are valid only in their callback. They cannot commit or nest work.
type CorrectionStore interface {
	ViewActivityCorrection(context.Context, func(ActivityCorrectionReader) error) error
	UpdateActivityCorrection(context.Context, func(ActivityCorrectionWriter) error) error
	ViewInstrumentCorrection(context.Context, func(InstrumentCorrectionReader) error) error
	UpdateInstrumentCorrection(context.Context, func(InstrumentCorrectionWriter) error) error
}

type ActivityCorrectionReader interface {
	ActivityRevision(context.Context, string) (int64, error)
	ActivityDecisions(context.Context, string) ([]DecisionRecord, error)
	ActivityObservation(context.Context, string, string) (source.ActivityObservation, error)
	ActivityInputs(
		context.Context,
		string,
		[]calculation.FieldSelection,
	) (calculation.ActivityCalculationInput, error)
}

type DecisionWriter interface {
	Submission(context.Context, string) (calculation.DecisionInput, string, bool, error)
	InsertDecision(
		context.Context,
		calculation.DecisionInput,
		calculation.DecisionInput,
		string,
		time.Time,
		string,
	) error
}

type ActivityCorrectionWriter interface {
	ActivityCorrectionReader
	DecisionWriter
	SaveActivity(context.Context, string, calculation.ActivityCalculation) error
}

func CheckCorrection(ctx context.Context, store CorrectionStore, decision calculation.Decision) error {
	f := decision.Fields()
	if _, err := calculation.NewDecision(f); err != nil {
		return err
	}

	if f.TargetKind == "instrument" {
		return store.ViewInstrumentCorrection(ctx, func(reader InstrumentCorrectionReader) error {
			_, _, err := prepareInstrumentCorrection(ctx, reader, f)
			return err
		})
	}

	return store.ViewActivityCorrection(ctx, func(reader ActivityCorrectionReader) error {
		_, _, _, err := prepareActivityCorrection(ctx, reader, f)
		return err
	})
}

func SaveDecision(
	ctx context.Context,
	store CorrectionStore,
	decision calculation.Decision,
	actor string,
	at time.Time,
) error {
	f := decision.Fields()
	if _, err := calculation.NewDecision(f); err != nil {
		return err
	}

	if actor == "" || len(actor) > 256 || at.IsZero() {
		return calculation.ErrInvalid
	}

	if f.TargetKind == "instrument" {
		return saveInstrumentDecision(ctx, store, f, actor, at)
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
	f calculation.DecisionInput,
) (calculation.ActivityCalculation, []calculation.DecisionInput, map[string]DecisionRecord, error) {
	revision, err := reader.ActivityRevision(ctx, f.TargetID)
	if err != nil {
		return calculation.ActivityCalculation{}, nil, nil, err
	}

	if revision != f.ExpectedRevision {
		return calculation.ActivityCalculation{}, nil, nil, ErrStale
	}

	records, err := reader.ActivityDecisions(ctx, f.TargetID)
	if err != nil {
		return calculation.ActivityCalculation{}, nil, nil, err
	}

	active := map[string]DecisionRecord{}
	for _, record := range records {
		if record.Active {
			active[record.Scope] = record
		}
	}

	fields, err := activityCorrectionFields(ctx, reader, f, active)
	if err != nil {
		return calculation.ActivityCalculation{}, nil, nil, err
	}

	prospective := map[string]calculation.FieldSelection{}
	for scope, record := range active {
		prospective[scope] = calculation.FieldSelection{
			Scope:    scope,
			Text:     record.Text,
			Currency: record.Currency,
		}
	}

	for _, field := range fields {
		if field.Action == "release" {
			if _, ok := active[field.Scope]; !ok {
				return calculation.ActivityCalculation{}, nil, nil, calculation.ErrInvalid
			}

			delete(prospective, field.Scope)
		} else {
			prospective[field.Scope] = calculation.FieldSelection{
				Scope:    field.Scope,
				Text:     field.Text,
				Currency: field.Currency,
			}
		}
	}

	selections := make([]calculation.FieldSelection, 0, len(prospective))
	for _, selection := range prospective {
		selections = append(selections, selection)
	}

	sort.Slice(selections, func(i, j int) bool { return selections[i].Scope < selections[j].Scope })

	in, err := reader.ActivityInputs(ctx, f.TargetID, selections)
	if err != nil {
		return calculation.ActivityCalculation{}, nil, nil, err
	}

	calculation, err := calculation.CalculateActivity(in)

	return calculation, fields, active, err
}

// An observation selection expands only its present fields into independently
// releasable decisions. Releasing it removes the active evidence scopes.
func activityCorrectionFields(
	ctx context.Context,
	reader ActivityCorrectionReader,
	f calculation.DecisionInput,
	active map[string]DecisionRecord,
) ([]calculation.DecisionInput, error) {
	if f.Scope != "observation" {
		return []calculation.DecisionInput{f}, nil
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
		return nil, calculation.ErrInvalid
	}

	sort.Strings(scopes)

	fields := make([]calculation.DecisionInput, 0, len(scopes))
	for _, scope := range scopes {
		field := f
		field.Scope = scope
		fields = append(fields, field)
	}

	return fields, nil
}
