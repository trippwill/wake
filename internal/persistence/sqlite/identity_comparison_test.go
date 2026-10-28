// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"bytes"
	"encoding/json"
	"html"
	"strings"
	"testing"
	"time"

	"go.trippwill.dev/wake/financial"
	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/reference"
	"go.trippwill.dev/wake/internal/transport/admin"
	"go.trippwill.dev/wake/source"
)

func renderIdentityComparison(
	t *testing.T,
	kind string,
	a, b []byte,
	field, label, oldValue, newValue string,
) {
	t.Helper()

	accepted, err := comparisonValues(kind, a)
	if err != nil {
		t.Fatal(err)
	}

	candidate, err := comparisonValues(kind, b)
	if err != nil {
		t.Fatal(err)
	}

	if accepted[field].Value == candidate[field].Value {
		t.Errorf("retained identity difference omitted: %s", field)
	}

	form := application.CorrectionForm{Fields: []string{"quantity", "price"}, Values: accepted}

	var body bytes.Buffer
	if err = admin.CandidateComparison(form, application.CorrectionCandidate{Fields: candidate}).
		Render(t.Context(), &body); err != nil {
		t.Fatal(err)
	}

	text := html.UnescapeString(body.String())
	for _, want := range []string{label, oldValue, newValue, "Different"} {
		if !strings.Contains(text, want) {
			t.Errorf("identity comparison hides %s: %q", field, want)
		}
	}
}

func TestComparisonPreservesQualifiedOrdersForEveryActivityKind(t *testing.T) {
	s := coverageStore(t)
	lifecycle := strings.NewReplacer("option-fill", "outcome-fill", "option-trade", "outcome-trade", `transactionType="ExchTrade"`, `transactionType="BookTrade" code="A"`).
		Replace(syntheticOption)
	coverageReport(t, s, "identity-history", syntheticOption+syntheticFX+lifecycle)

	view, err := s.Dashboard(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	first, _ := source.NewReference("synthetic", "order", "source-one", "same-native-value")
	second, _ := source.NewReference("synthetic", "order", "source-two", "same-native-value")
	for _, kind := range []string{"trade", "lifecycle", "conversion"} {
		t.Run(kind, func(t *testing.T) {
			encode := func(order source.Reference) []byte {
				var (
					data []byte
					e    error
				)
				switch kind {
				case "trade":
					f := view.Executions[0].Terms().Fields()
					f.Order = order

					v, x := source.NewTrade(f)
					if x != nil {
						t.Fatal(x)
					}

					data, e = encodeTrade(v)
				case "lifecycle":
					f := view.Lifecycles[0].Terms().Fields()
					f.Order = order

					v, x := source.NewLifecycle(f)
					if x != nil {
						t.Fatal(x)
					}

					data, e = encodeLifecycle(v)
				case "conversion":
					f := view.Conversions[0].Terms().Fields()
					f.Order = order

					v, x := source.NewConversion(f)
					if x != nil {
						t.Fatal(x)
					}

					data, e = encodeConversion(v)
				}

				if e != nil {
					t.Fatal(e)
				}

				return data
			}
			renderIdentityComparison(
				t,
				kind,
				encode(first),
				encode(second),
				"reported.order",
				"Reported source order",
				"source-one",
				"source-two",
			)
		})
	}
}

func TestComparisonPreservesReportedUnderlierIdentity(t *testing.T) {
	s := coverageStore(t)
	lifecycle := strings.NewReplacer("option-fill", "outcome-fill", "option-trade", "outcome-trade", `transactionType="ExchTrade"`, `transactionType="BookTrade" code="A"`).
		Replace(syntheticOption)
	coverageReport(t, s, "underlier-history", syntheticOption+lifecycle)

	view, err := s.Dashboard(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	for _, kind := range []string{"trade", "lifecycle"} {
		for _, identity := range []string{"reference", "identifier"} {
			t.Run(kind+"/"+identity, func(t *testing.T) {
				encode := func(changed bool) []byte {
					f := view.Executions[0].Terms().Fields()
					instrument := f.Instrument.Fields()
					option := instrument.Option.Fields()

					refValue, idValue := "underlier-one", "US0000000001"
					if changed {
						if identity == "reference" {
							refValue = "underlier-two"
						} else {
							idValue = "US0000000002"
						}
					}

					ref, _ := source.NewReference("synthetic", "instrument", "underlier-scope", refValue)
					id, _ := source.NewIdentifier(source.ISIN, idValue, "synthetic-isin")

					underlier, e := source.NewInstrument(
						source.InstrumentInput{Kind: source.Equity, Reference: ref, Identifiers: []source.Identifier{id}},
					)
					if e != nil {
						t.Fatal(e)
					}

					option.Underlier = underlier

					instrument.Option, e = source.NewOptionTerms(option)
					if e != nil {
						t.Fatal(e)
					}

					root, e := source.NewInstrument(instrument)
					if e != nil {
						t.Fatal(e)
					}

					var data []byte
					if kind == "trade" {
						f.Instrument = root

						v, x := source.NewTrade(f)
						if x != nil {
							t.Fatal(x)
						}

						data, e = encodeTrade(v)
					} else {
						l := view.Lifecycles[0].Terms().Fields()
						l.Instrument = root

						v, x := source.NewLifecycle(l)
						if x != nil {
							t.Fatal(x)
						}

						data, e = encodeLifecycle(v)
					}

					if e != nil {
						t.Fatal(e)
					}

					return data
				}

				old, newValue := "underlier-one", "underlier-two"
				if identity == "identifier" {
					old, newValue = "US0000000001", "US0000000002"
				}

				renderIdentityComparison(
					t,
					kind,
					encode(false),
					encode(true),
					"option.reported_underlier",
					"Reported option underlier",
					old,
					newValue,
				)
			})
		}
	}
}

func TestReferenceComparisonUsesSelectedShareClassIdentity(t *testing.T) {
	s := coverageStore(t)
	coverageReport(t, s, "identity-equity", equityRow("U1", "fill", "100"))

	view, err := s.Dashboard(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	id := view.Instruments[0].ID()
	jobs := []reference.Job{{IDType: "ID_ISIN", IDValue: "US0000000001"}}
	request, _ := json.Marshal(jobs)
	response := []byte(
		`[{"data":[{"figi":"listing-a","shareClassFIGI":"root-a","name":"Same name","ticker":"SYN","marketSector":"Equity"},{"figi":"listing-b","shareClassFIGI":"root-b","name":"Same name","ticker":"SYN","marketSector":"Equity"}]}]`,
	)

	results, err := s.SaveReference(
		t.Context(),
		reference.Evidence{At: time.Now(), Request: request, Response: response, Status: 200},
		jobs,
		[]reference.Result{{Status: "ambiguous", Next: time.Now().Add(time.Hour)}},
	)
	if err != nil {
		t.Fatal(err)
	}

	if err = s.ApplyReference(t.Context(), id, results[0]); err != nil {
		t.Fatal(err)
	}

	form, err := s.CorrectionForm(t.Context(), "instrument", id)
	if err != nil || len(form.Candidates) != 2 {
		t.Fatal(form, err)
	}

	retainDecision(t, s, "instrument", id, "reference", form.Candidates[0].Value, "", "set")

	form, err = s.CorrectionForm(t.Context(), "instrument", id)
	if err != nil {
		t.Fatal(err)
	}

	candidate := form.Candidates[1]
	if form.Values["name"] != candidate.Fields["name"] || form.Values["ticker"] != candidate.Fields["ticker"] {
		t.Fatal("fixture names do not match")
	}

	if form.Values["reference_identity"].Value != "root-a" ||
		candidate.Fields["reference_identity"].Value != "root-b" {
		t.Fatal("comparison omits selected share-class identities", form.Values, candidate.Fields)
	}

	var body bytes.Buffer
	if err = admin.CandidateComparison(form, candidate).Render(t.Context(), &body); err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{"Reference identity", "root-a", "root-b", "Different"} {
		if !strings.Contains(body.String(), want) {
			t.Error("reference comparison hides", want)
		}
	}
}

func TestUnknownDebtCurrencyComparisonRendersUnknown(t *testing.T) {
	s := coverageStore(t)
	coverageReport(t, s, "unknown-currency", syntheticDebt)

	view, err := s.Dashboard(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	f := view.Executions[0].Terms().Fields()
	instrument := f.Instrument.Fields()
	debt := instrument.Debt.Fields()
	debt.Currency = financial.Currency{}

	instrument.Debt, err = source.NewDebtTerms(debt)
	if err != nil {
		t.Fatal(err)
	}

	f.Instrument, err = source.NewInstrument(instrument)
	if err != nil {
		t.Fatal(err)
	}

	trade, err := source.NewTrade(f)
	if err != nil {
		t.Fatal(err)
	}

	data, err := encodeTrade(trade)
	if err != nil {
		t.Fatal(err)
	}

	values, err := comparisonValues("trade", data)
	if err != nil {
		t.Fatal(err)
	}

	if values["debt.currency"].Value != "" {
		t.Error("unknown currency is not absent", values["debt.currency"])
	}

	var body bytes.Buffer
	if err = admin.CandidateComparison(application.CorrectionForm{Values: values}, application.CorrectionCandidate{Fields: values}).
		Render(t.Context(), &body); err != nil {
		t.Fatal(err)
	}

	if strings.ContainsRune(body.String(), '\x00') || !strings.Contains(body.String(), "Unknown") {
		t.Fatal("unknown currency renders invalid characters")
	}
}

func TestTradeIdentityDisagreementAlwaysHasComparisonContext(t *testing.T) {
	s := coverageStore(t)
	coverageReport(t, s, "identity-contract", equityRow("U1", "fill", "100"))

	view, err := s.Dashboard(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	base := view.Executions[0].Terms()
	for _, test := range []struct {
		name, field string
		change      func(*source.TradeInput)
	}{
		{"kind", "reported.trade_kind", func(f *source.TradeInput) { f.Kind = source.StockBooking }},
		{"account", "reported.account", func(f *source.TradeInput) {
			f.Account, _ = source.NewReference("synthetic", "account", "", "other-account")
		}},
		{"instrument-reference", "reported.instrument", func(f *source.TradeInput) {
			i := f.Instrument.Fields()
			i.Reference, _ = source.NewReference("ibkr", "instrument", "", "other-instrument")
			f.Instrument, _ = source.NewInstrument(i)
		}},
		{"instrument-identifier", "reported.instrument", func(f *source.TradeInput) {
			i := f.Instrument.Fields()
			identifier, _ := source.NewIdentifier(source.ISIN, "US0000000002", "synthetic-isin")
			i.Identifiers = []source.Identifier{identifier}
			f.Instrument, _ = source.NewInstrument(i)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := base.Fields()
			test.change(&f)

			changed, e := source.NewTrade(f)
			if e != nil {
				t.Fatal(e)
			}

			if application.Compatible(base, changed) {
				t.Fatal("fixture is not a known financial disagreement")
			}

			a, e := encodeTrade(base)
			if e != nil {
				t.Fatal(e)
			}

			b, e := encodeTrade(changed)
			if e != nil {
				t.Fatal(e)
			}

			left, e := comparisonValues("trade", a)
			if e != nil {
				t.Fatal(e)
			}

			right, e := comparisonValues("trade", b)
			if e != nil {
				t.Fatal(e)
			}

			if left[test.field] == right[test.field] {
				t.Fatal("known financial disagreement has no comparison context", test.name)
			}

			var body bytes.Buffer
			if e = admin.CandidateComparison(application.CorrectionForm{Values: left}, application.CorrectionCandidate{Fields: right}).
				Render(t.Context(), &body); e != nil {
				t.Fatal(e)
			}

			if !strings.Contains(body.String(), "Different") {
				t.Fatal("known disagreement renders as matching")
			}
		})
	}
}
