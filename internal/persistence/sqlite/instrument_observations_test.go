// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"go.trippwill.dev/wake/financial"
	"go.trippwill.dev/wake/provider"
	"go.trippwill.dev/wake/source"
)

func TestRetainedOptionObservationsExposeActivityConflict(t *testing.T) {
	for _, kind := range []string{"trade", "lifecycle"} {
		for _, order := range []string{"10-first", "100-first", "same-artifact"} {
			t.Run(kind+"/"+order, func(t *testing.T) {
				s := coverageStore(t)

				row := syntheticOption
				if kind == "lifecycle" {
					row = strings.ReplaceAll(
						row,
						`transactionType="ExchTrade"`,
						`transactionType="BookTrade" code="Ex"`,
					)
				}

				base := strings.ReplaceAll(row, ` multiplier="10"`, "")

				first, second := row, strings.ReplaceAll(row, `multiplier="10"`, `multiplier="100"`)
				want := ""

				if order == "100-first" {
					first, second = second, first
				}

				coverageReport(t, s, "base", base)

				page, err := snapshotResources(t.Context(), s, "activity")
				if err != nil || len(page.Items) != 1 {
					t.Fatal(page, err)
				}

				activity := page.Items[0].ID

				var instrument string
				if err = s.db.QueryRow(`SELECT instrument_id FROM executions WHERE id=?`, activity).
					Scan(&instrument); err != nil {
					t.Fatal(err)
				}

				if order != "same-artifact" {
					coverageReport(t, s, "first", first)
				} else {
					second = first + second
					want = ""
				}

				baseline, err := snapshotState(t.Context(), s)
				if err != nil {
					t.Fatal(err)
				}

				coverageReport(t, s, "second", second)

				detail, err := s.InstrumentDetail(t.Context(), instrument)
				if err != nil || detail.Instrument.Status().NeedsReview() ||
					detail.Instrument.Reported().Option().Fields().Multiplier.Value() != "" {
					t.Fatal("conflicting root lost prior terms or uncertainty", detail, err)
				}

				accepted, e := s.Resource(t.Context(), "activity", activity)
				if e != nil || !accepted.Activity.Conflict || accepted.Activity.Option == nil ||
					publishedString(accepted.Activity.Option.Multiplier) != want {
					t.Fatalf(
						"same-activity terms lost want=%s activity=%+v option=%+v err=%v",
						want,
						accepted.Activity,
						accepted.Activity.Option,
						e,
					)
				}

				count := 0
				for _, change := range snapshotChanges(t, s, baseline).Items {
					if change.Resource.Kind == "instruments" && change.Resource.ID == instrument {
						count++
					}
				}

				if count != 0 {
					t.Fatal("financial disagreement changed root identity", count)
				}

				baseline, err = snapshotState(t.Context(), s)
				if err != nil {
					t.Fatal(err)
				}

				coverageReport(t, s, "repeat", second)

				if changes := snapshotChanges(t, s, baseline); len(changes.Items) != 0 {
					t.Fatal("replay published duplicate transition", changes.Items)
				}
				// Re-evaluate identity separately from the retained financial dispute.
				if _, err = s.db.Exec(
					`UPDATE instruments SET reference_status='provisional' WHERE id=?`,
					instrument,
				); err != nil {
					t.Fatal(err)
				}

				if _, err = s.db.Exec(
					`UPDATE instrument_selections SET status='provisional' WHERE instrument_id=?`,
					instrument,
				); err != nil {
					t.Fatal(err)
				}

				recalculateFixture(t, s)

				rebuilt, err := s.InstrumentDetail(t.Context(), instrument)
				if err != nil || rebuilt.Instrument.Status().NeedsReview() ||
					!reflect.DeepEqual(rebuilt.Instrument.Reported().Fields(), detail.Instrument.Reported().Fields()) {
					t.Fatal("recalculation changed identity while recovering financial conflict", rebuilt, err)
				}
			})
		}
	}
}

func TestRetainedDebtObservationsExposeActivityConflict(t *testing.T) {
	s := coverageStore(t)
	coverageReport(t, s, "base", syntheticDebt)

	view, err := s.Dashboard(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	base := view.Executions[0]
	for _, coupon := range []string{"4.125", "5.25"} {
		f := base.Terms().Fields()
		i := f.Instrument.Fields()
		d := i.Debt.Fields()

		d.Coupon, err = financial.NewQuantity(coupon)
		if err != nil {
			t.Fatal(err)
		}

		i.Debt, err = source.NewDebtTerms(d)
		if err != nil {
			t.Fatal(err)
		}

		f.Instrument, err = source.NewInstrument(i)
		if err != nil {
			t.Fatal(err)
		}

		trade, e := source.NewTrade(f)
		if e != nil {
			t.Fatal(e)
		}

		coverageClaims(
			t,
			s,
			coupon,
			claimNormalizer(
				func(ctx context.Context, _ provider.Artifact, claims provider.Claims) (provider.Result, error) {
					if e := claims.PutTrade(ctx, trade); e != nil {
						return provider.Result{}, e
					}

					return provider.NewResult(provider.Counts{Rows: 1, Trades: 1})
				},
			),
		)
	}

	detail, err := s.InstrumentDetail(t.Context(), base.Instrument())
	if err != nil || detail.Instrument.Status().NeedsReview() ||
		detail.Instrument.Reported().Debt().Fields().Coupon.Value() != "" {
		t.Fatal("debt terms contaminated identity", detail, err)
	}

	accepted, e := s.Resource(t.Context(), "activity", base.ID())
	if e != nil || !accepted.Activity.Conflict || accepted.Activity.Debt == nil ||
		publishedString(accepted.Activity.Debt.Coupon) != "" {
		t.Fatalf(
			"activity=%+v debt=%+v option=%+v err=%v",
			accepted.Activity,
			accepted.Activity.Debt,
			accepted.Activity.Option,
			e,
		)
	}
}

func TestMissingInstrumentReferencesStillExposeRetainedConflict(t *testing.T) {
	s := coverageStore(t)
	coverageReport(t, s, "base", strings.ReplaceAll(syntheticOption, ` multiplier="10"`, ""))

	view, err := s.Dashboard(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	base := view.Executions[0]
	for _, multiplier := range []string{"10", "100"} {
		f := base.Terms().Fields()
		i := f.Instrument.Fields()
		i.Reference = source.Reference{}
		o := i.Option.Fields()

		o.Multiplier, err = financial.NewQuantity(multiplier)
		if err != nil {
			t.Fatal(err)
		}

		i.Option, err = source.NewOptionTerms(o)
		if err != nil {
			t.Fatal(err)
		}

		f.Instrument, err = source.NewInstrument(i)
		if err != nil {
			t.Fatal(err)
		}

		trade, e := source.NewTrade(f)
		if e != nil {
			t.Fatal(e)
		}

		coverageClaims(
			t,
			s,
			multiplier,
			claimNormalizer(
				func(ctx context.Context, _ provider.Artifact, claims provider.Claims) (provider.Result, error) {
					if e := claims.PutTrade(ctx, trade); e != nil {
						return provider.Result{}, e
					}

					return provider.NewResult(provider.Counts{Rows: 1, Trades: 1})
				},
			),
		)
	}

	detail, err := s.InstrumentDetail(t.Context(), base.Instrument())
	if err != nil || detail.Instrument.Status().NeedsReview() ||
		detail.Instrument.Reported().Option().Fields().Multiplier.Value() != "" {
		t.Fatal("missing-reference terms contaminated identity", detail, err)
	}

	accepted, e := s.Resource(t.Context(), "activity", base.ID())
	if e != nil || !accepted.Activity.Conflict || accepted.Activity.Option == nil ||
		publishedString(accepted.Activity.Option.Multiplier) != "" {
		t.Fatalf(
			"activity=%+v debt=%+v option=%+v err=%v",
			accepted.Activity,
			accepted.Activity.Debt,
			accepted.Activity.Option,
			e,
		)
	}
}

func publishedString(value *string) string {
	if value == nil {
		return ""
	}

	return *value
}
