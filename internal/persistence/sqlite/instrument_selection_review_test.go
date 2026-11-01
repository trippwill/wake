// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"strings"
	"testing"
)

func TestDistinctExecutionsRetainTheirOwnOptionTerms(t *testing.T) {
	s := coverageStore(t)
	coverageReport(t, s, "first-option", syntheticOption)

	v, err := s.Dashboard(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	instrument := v.Executions[0].Instrument()
	changed := strings.NewReplacer(`execID="option-fill"`, `execID="other-fill"`, `tradeID="option-trade"`, `tradeID="other-trade"`, `strike="100.00000000001"`, `strike="110"`).
		Replace(syntheticOption)
	coverageReport(t, s, "second-option", changed)

	detail, err := s.InstrumentDetail(t.Context(), instrument)
	if err != nil || detail.Instrument.Status().NeedsReview() ||
		detail.Instrument.Reported().Option().Fields().Strike.Amount() != "" {
		t.Fatal("activity terms contaminated identity", detail, err)
	}

	v, err = s.Dashboard(t.Context())
	if err != nil || len(v.Executions) != 2 {
		t.Fatal("distinct executions lost", v, err)
	}

	terms := map[string]bool{}
	for _, e := range v.Executions {
		terms[e.Terms().Fields().Instrument.Option().Fields().Strike.Amount()] = true
		if e.Instrument() != instrument || e.State().NeedsReview() {
			t.Fatal(e)
		}
	}

	if !terms["100.00000000001"] || !terms["110"] {
		t.Fatal(terms)
	}
}

func TestSourceUnderlierEnrichmentSurvivesOverrideRelease(t *testing.T) {
	s := coverageStore(t)
	unknown := strings.NewReplacer(` underlyingConid="100"`, "", ` underlyingSymbol="SYN"`, "").
		Replace(syntheticOption)
	override := strings.Replace(equityRow("U1", "override", "100"), `conid="100"`, `conid="999"`, 1)
	coverageReport(t, s, "without-source-underlier", unknown+override)

	v, err := s.Dashboard(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	var optionID, overrideID string
	for _, e := range v.Executions {
		if e.Terms().Fields().Instrument.Kind() == "option" {
			optionID = e.Instrument()
		} else {
			overrideID = e.Instrument()
		}
	}

	if optionID == "" || overrideID == "" {
		t.Fatal("missing fixture instruments")
	}

	retainDecision(t, s, "instrument", optionID, "underlier", overrideID, "", "set")
	coverageReport(t, s, "reported-source-underlier", syntheticOption)

	detail, err := s.InstrumentDetail(t.Context(), optionID)
	if err != nil || detail.Underlier != overrideID {
		t.Fatal("source overwrote operator link", detail, err)
	}

	retainDecision(t, s, "instrument", optionID, "underlier", "", "", "release")

	detail, err = s.InstrumentDetail(t.Context(), optionID)
	if err != nil || detail.Underlier == "" || detail.Underlier == overrideID {
		t.Fatal("release lost automatic source underlier", detail, err)
	}

	source, err := s.InstrumentDetail(t.Context(), detail.Underlier)
	if err != nil || source.Instrument.Reported().Reference().Value() != "100" {
		t.Fatal("wrong source underlier", source, err)
	}
}
