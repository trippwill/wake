// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package application

import (
	"context"
	"sort"
	"time"

	"go.trippwill.dev/wake/internal/application/calculation"
	"go.trippwill.dev/wake/internal/reference"
	"go.trippwill.dev/wake/source"
)

type InstrumentCorrectionReader interface {
	InstrumentRevision(context.Context, string) (int64, error)
	InstrumentDecisions(context.Context, string) ([]DecisionRecord, error)
	InstrumentBaseline(context.Context, string) (calculation.InstrumentSelection, source.InstrumentKind, error)
	CanonicalInstrument(context.Context, string) (string, error)
	InstrumentKind(context.Context, string) (source.InstrumentKind, error)
	InstrumentReference(context.Context, string, string) (reference.Record, string, error)
	CompetingInstrumentKinds(context.Context, string, string) ([]source.InstrumentKind, error)
}

type InstrumentCorrectionWriter interface {
	InstrumentCorrectionReader
	DecisionWriter
	SaveInstrument(context.Context, string, calculation.InstrumentCalculation) error
}

func saveInstrumentDecision(
	ctx context.Context,
	store CorrectionStore,
	f calculation.DecisionInput,
	actor string,
	at time.Time,
) error {
	return store.UpdateInstrumentCorrection(ctx, func(writer InstrumentCorrectionWriter) error {
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

		result, supersedes, err := prepareInstrumentCorrection(ctx, writer, f)
		if err != nil {
			return err
		}

		if err := writer.InsertDecision(ctx, f, f, actor, at, supersedes); err != nil {
			return err
		}

		return writer.SaveInstrument(ctx, f.TargetID, result)
	})
}

func prepareInstrumentCorrection(
	ctx context.Context,
	reader InstrumentCorrectionReader,
	f calculation.DecisionInput,
) (calculation.InstrumentCalculation, string, error) {
	target, err := reader.CanonicalInstrument(ctx, f.TargetID)
	if err != nil {
		return calculation.InstrumentCalculation{}, "", err
	}

	if target != f.TargetID {
		return calculation.InstrumentCalculation{}, "", ErrStale
	}

	revision, err := reader.InstrumentRevision(ctx, target)
	if err != nil {
		return calculation.InstrumentCalculation{}, "", err
	}

	if revision != f.ExpectedRevision {
		return calculation.InstrumentCalculation{}, "", ErrStale
	}

	return calculateInstrumentSelection(ctx, reader, target, &f)
}

func ProjectInstrument(
	ctx context.Context,
	reader InstrumentCorrectionReader,
	id string,
) (calculation.InstrumentCalculation, error) {
	result, _, err := calculateInstrumentSelection(ctx, reader, id, nil)
	return result, err
}

func calculateInstrumentSelection(
	ctx context.Context, reader InstrumentCorrectionReader, id string, proposed *calculation.DecisionInput,
) (calculation.InstrumentCalculation, string, error) {
	base, family, err := reader.InstrumentBaseline(ctx, id)
	if err != nil {
		return calculation.InstrumentCalculation{}, "", err
	}

	rows, err := reader.InstrumentDecisions(ctx, id)
	if err != nil {
		return calculation.InstrumentCalculation{}, "", err
	}

	active := make(map[string]DecisionRecord)
	for _, row := range rows {
		if row.Active {
			active[row.Scope] = row
		}
	}

	var supersedes string
	if proposed != nil {
		supersedes = active[proposed.Scope].ID
		if proposed.Action == "release" {
			if supersedes == "" {
				return calculation.InstrumentCalculation{}, "", calculation.ErrInvalid
			}

			delete(active, proposed.Scope)
		} else {
			active[proposed.Scope] = DecisionRecord{Text: proposed.Text, Currency: proposed.Currency}
		}
	}

	input := calculation.InstrumentCalculationInput{
		Rule: calculation.RetainedClaimsRule, ID: id, Family: family, Baseline: base,
		References: map[string]calculation.ReferenceSelection{}, CompetingFIGIs: map[string]bool{},
		Underliers: map[string]source.InstrumentKind{},
	}

	scopes := make([]string, 0, len(active))
	for scope := range active {
		scopes = append(scopes, scope)
	}

	sort.Strings(scopes)

	for _, scope := range scopes {
		d := active[scope]
		switch scope {
		case "reference":
			record, evidence, err := reader.InstrumentReference(ctx, id, d.Text)
			if err != nil {
				return calculation.InstrumentCalculation{}, "", err
			}

			selected, figi, compatible := reference.Select(family, []reference.Record{record})
			input.References[d.Text] = calculation.ReferenceSelection{
				InstrumentSelection: calculation.InstrumentSelection{
					Name:     selected.Name,
					Ticker:   selected.Ticker,
					FIGI:     figi,
					Evidence: evidence,
				},
				Eligible: compatible,
			}

			if compatible {
				kinds, err := reader.CompetingInstrumentKinds(ctx, id, figi)
				if err != nil {
					return calculation.InstrumentCalculation{}, "", err
				}

				for _, kind := range kinds {
					if !calculation.CompatibleInstrumentKind(family, kind) {
						input.CompetingFIGIs[figi] = true
					}
				}
			}
		case "underlier":
			if d.Text != "" {
				d.Text, err = reader.CanonicalInstrument(ctx, d.Text)
				if err != nil {
					return calculation.InstrumentCalculation{}, "", err
				}

				kind, err := reader.InstrumentKind(ctx, d.Text)
				if err != nil {
					return calculation.InstrumentCalculation{}, "", calculation.ErrInvalid
				}

				input.Underliers[d.Text] = kind
			}
		}

		input.Decisions = append(
			input.Decisions,
			calculation.FieldSelection{Scope: scope, Text: d.Text, Currency: d.Currency},
		)
	}

	result, err := calculation.CalculateInstrument(input)

	return result, supersedes, err
}
