// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"context"
	"crypto/rand"
	"reflect"
	"strings"
	"testing"
	"time"

	"go.trippwill.dev/wake/financial"
	"go.trippwill.dev/wake/provider"
	"go.trippwill.dev/wake/source"
)

func TestRetainedOptionObservationsExposeRootConflict(t *testing.T) {
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
				want := "10"
				if order == "100-first" {
					first, second, want = second, first, "100"
				}

				coverageReport(t, s, "base", base)

				page, err := s.Resources(t.Context(), "activity", "", 10)
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

				checkpoint, err := s.PublishCheckpoint(t.Context())
				if err != nil {
					t.Fatal(err)
				}

				coverageReport(t, s, "second", second)

				detail, err := s.InstrumentDetail(t.Context(), instrument)
				if err != nil || detail.Instrument.Status() != "conflict" ||
					detail.Instrument.Reported().Option().Fields().Multiplier.Value() != want {
					t.Fatal("conflicting root lost prior terms or uncertainty", detail, err)
				}

				count := 0
				for _, change := range publishedChanges(t, s, checkpoint).Items {
					if change.Resource.Kind == "instruments" && change.Resource.ID == instrument {
						count++
					}
				}

				if count != 1 {
					t.Fatal("expected one root conflict transition", count)
				}

				checkpoint, err = s.PublishCheckpoint(t.Context())
				if err != nil {
					t.Fatal(err)
				}

				coverageReport(t, s, "repeat", second)

				if changes := publishedChanges(t, s, checkpoint); len(changes.Items) != 0 {
					t.Fatal("replay published duplicate transition", changes.Items)
				}
				// Recreate the old missing conflict flag only in this disposable store.
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

				if _, err = s.StartRebuild(t.Context(), rand.Text(), "test", time.Now()); err != nil {
					t.Fatal(err)
				}

				for n := 0; ; n++ {
					changed, e := s.RunRecovery(t.Context())
					if e != nil || n == 100 {
						t.Fatal("rebuild did not finish", e)
					}

					if !changed {
						break
					}
				}

				rebuilt, err := s.InstrumentDetail(t.Context(), instrument)
				if err != nil || rebuilt.Instrument.Status() != "conflict" ||
					!reflect.DeepEqual(rebuilt.Instrument.Reported().Fields(), detail.Instrument.Reported().Fields()) {
					t.Fatal("rebuild did not recover conflict and retained terms", rebuilt, err)
				}
			})
		}
	}
}

func TestRetainedDebtObservationsExposeRootConflict(t *testing.T) {
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
	if err != nil || detail.Instrument.Status() != "conflict" ||
		detail.Instrument.Reported().Debt().Fields().Coupon.Value() != "4.125" {
		t.Fatal("debt conflict or retained coupon lost", detail, err)
	}
}
