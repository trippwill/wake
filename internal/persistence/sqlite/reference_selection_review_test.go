// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/reference"
)

func TestRetainedAutomaticFIGIPreventsCompetingAssignment(t *testing.T) {
	ctx := t.Context()
	s := coverageStore(t)
	first := equityRow("U1", "first-root", "100")
	second := strings.NewReplacer(`conid="100"`, `conid="200"`, `US0000000001`, `US0000000002`).
		Replace(equityRow("U1", "second-root", "100"))
	coverageReport(t, s, "two-roots", first+second)

	v, err := s.Dashboard(ctx)
	if err != nil || len(v.Executions) != 2 {
		t.Fatal(v, err)
	}

	var a, b string
	for _, execution := range v.Executions {
		if execution.Terms().Fields().Instrument.Reference().Value() == "100" {
			a = execution.Instrument()
		} else {
			b = execution.Instrument()
		}
	}

	apply := func(id, isin, figi string) {
		t.Helper()

		record := reference.Record{
			FIGI:       "listing-" + figi,
			ShareClass: figi,
			Name:       "Synthetic " + figi,
			Market:     "Equity",
		}
		jobs := []reference.Job{{IDType: "ID_ISIN", IDValue: isin}}
		request, _ := json.Marshal(jobs)
		response, _ := json.Marshal([]struct {
			Data []reference.Record `json:"data"`
		}{{Data: []reference.Record{record}}})

		results, e := s.SaveReference(
			ctx,
			reference.Evidence{At: time.Now(), Request: request, Response: response, Status: 200},
			jobs,
			[]reference.Result{
				{Status: "resolved", Records: []reference.Record{record}, Next: time.Now().Add(time.Hour)},
			},
		)
		if e != nil {
			t.Fatal(e)
		}

		if e = s.ApplyReference(ctx, id, results[0]); e != nil {
			t.Fatal(e)
		}
	}
	apply(a, "US0000000001", "X")
	apply(a, "US0000000001", "Y")

	form, err := s.CorrectionForm(ctx, "instrument", a)
	if err != nil {
		t.Fatal(err)
	}

	choice := ""
	for _, candidate := range form.Candidates {
		if strings.Contains(candidate.Label, "Synthetic Y") {
			choice = candidate.Value
		}
	}

	if choice == "" {
		t.Fatal("missing retained override candidate", form)
	}

	retainDecision(t, s, "instrument", a, "reference", choice, "", "set")

	before, err := s.InstrumentDetail(ctx, a)
	if err != nil || before.Instrument.FIGI() != "Y" {
		t.Fatal("override fixture", before, err)
	}

	apply(b, "US0000000002", "X")

	otherForm, err := s.CorrectionForm(ctx, "instrument", b)
	if err != nil || len(otherForm.Candidates) != 1 {
		t.Fatal(otherForm, err)
	}

	duplicate, err := application.NewDecision(
		application.DecisionInput{
			TargetKind:       "instrument",
			TargetID:         b,
			Scope:            "reference",
			Text:             otherForm.Candidates[0].Value,
			Action:           "set",
			Submission:       rand.Text(),
			ExpectedRevision: otherForm.Revision,
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	if err = s.SaveDecision(
		ctx,
		duplicate,
		"admin/synthetic-session",
		time.Now(),
	); !errors.Is(
		err,
		application.ErrInvalid,
	) {
		t.Fatal("operator acquired another root's hidden automatic FIGI", err)
	}

	// Releasing the override must remain possible while the competing source stays visible.
	retainDecision(t, s, "instrument", a, "reference", "", "", "release")

	restored, err := s.InstrumentDetail(ctx, a)
	competing, otherErr := s.InstrumentDetail(ctx, b)
	if err != nil || otherErr != nil || restored.Instrument.FIGI() != "X" ||
		competing.Instrument.FIGI() != "" ||
		competing.Instrument.Status() != "conflict" {
		t.Fatal("automatic identity reservation lost", restored, competing, err, otherErr)
	}
}
