// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"context"
	"slices"

	"go.trippwill.dev/wake/internal/application"
)

// CheckCorrection reads the current projection and uses the same constructors as saving.
// It never writes a decision, advances a revision, or schedules processing.
func (s *Store) CheckCorrection(ctx context.Context, decision application.Decision) error {
	f := decision.Fields()

	form, err := s.CorrectionForm(ctx, f.TargetKind, f.TargetID)
	if err != nil {
		return err
	}

	if form.Revision != f.ExpectedRevision {
		return application.ErrStale
	}

	if f.Action != "set" || !slices.Contains(form.Fields, f.Scope) {
		return application.ErrInvalid
	}

	if slices.Contains([]string{"instrument", "listing", "underlier"}, f.Scope) {
		if f.Text == "" && f.Scope != "instrument" {
			return nil
		}

		for _, link := range form.Links {
			if link.Scope == f.Scope && link.Value == f.Text {
				if f.TargetKind == "activity" && f.Scope == "instrument" {
					for _, active := range form.Decisions {
						if active.Active && active.Scope == "listing" && active.Text != "" {
							listing, e := s.queries.Listing(ctx, active.Text)
							if e != nil {
								return e
							}

							if listing.InstrumentID != f.Text {
								return application.ErrInvalid
							}
						}
					}
				}

				return nil
			}
		}

		return application.ErrInvalid
	}

	if f.TargetKind == "instrument" {
		if f.Scope == "name" && validDecisionLabel(f.Text, 1024) ||
			f.Scope == "ticker" && (f.Text == "" || validDecisionLabel(f.Text, 256)) {
			return nil
		}

		return application.ErrInvalid
	}

	row, err := s.queries.Execution(ctx, f.TargetID)
	if err != nil {
		return err
	}

	switch row.Kind {
	case "trade":
		value, e := decodeTrade(row.Accepted)
		if e != nil {
			return e
		}

		_, err = application.ApplyTradeField(value, f.Scope, f.Text, f.Currency)
	case "lifecycle":
		value, e := decodeLifecycle(row.Accepted)
		if e != nil {
			return e
		}

		_, err = application.ApplyLifecycleField(value, f.Scope, f.Text, f.Currency)
	case "conversion":
		value, e := decodeConversion(row.Accepted)
		if e != nil {
			return e
		}

		_, err = application.ApplyConversionField(value, f.Scope, f.Text, f.Currency)
	default:
		return application.ErrInvalid
	}

	if err != nil {
		return application.ErrInvalid
	}

	return nil
}
