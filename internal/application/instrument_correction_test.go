// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package application

import (
	"context"
	"errors"
	"testing"

	"go.trippwill.dev/wake/internal/application/calculation"
	"go.trippwill.dev/wake/internal/reference"
	"go.trippwill.dev/wake/source"
)

type ineligibleReferenceReader struct {
	InstrumentCorrectionReader
}

func (ineligibleReferenceReader) InstrumentBaseline(
	context.Context,
	string,
) (calculation.InstrumentSelection, source.InstrumentKind, error) {
	return calculation.InstrumentSelection{}, source.Option, nil
}

func (ineligibleReferenceReader) InstrumentDecisions(context.Context, string) ([]DecisionRecord, error) {
	return []DecisionRecord{{Scope: "reference", Text: "candidate", Active: true}}, nil
}

func (ineligibleReferenceReader) InstrumentReference(
	context.Context,
	string,
	string,
) (reference.Record, string, error) {
	return reference.Record{FIGI: "equity", ShareClass: "share-class", Market: "Equity"}, "evidence", nil
}

func TestProjectInstrumentRejectsIneligibleReferenceWithoutLookingUpCompetitors(t *testing.T) {
	t.Parallel()

	_, err := ProjectInstrument(t.Context(), ineligibleReferenceReader{}, "option")
	if !errors.Is(err, calculation.ErrInvalid) {
		t.Fatal("wanted invalid reference selection, got", err)
	}
}
