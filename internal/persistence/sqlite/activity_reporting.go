// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"context"
	"slices"

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/persistence/sqlite/sqlc"
	"go.trippwill.dev/wake/source"
)

func reportedRealizedFields(
	fields []source.ActivityField,
	conflicts, operators []string,
) *application.PublishedRealizedPnL {
	var out *application.PublishedRealizedPnL
	for _, field := range fields {
		if field.Name() != source.FieldRealizedPnL && field.Name() != source.FieldReportedCostBasis {
			continue
		}

		if out == nil {
			out = &application.PublishedRealizedPnL{Selection: string(application.AutomaticSelection)}
		}

		if slices.Contains(conflicts, "identity") || slices.Contains(conflicts, field.Key()) {
			out.Conflict = true
		}

		if slices.Contains(operators, field.Key()) {
			out.Selection = string(application.OperatorSelection)
		}

		if !field.Known() {
			continue
		}

		f := field.Fields()
		if field.Name() == source.FieldRealizedPnL {
			out.Amount = publicMoney(f.Money)
			out.ReportDate = publicDate(f.ReportedOn)
		} else {
			out.CostBasis = publicMoney(f.Money)
			out.CostBasisReportDate = publicDate(f.ReportedOn)
		}
	}

	return out
}

func publishedRealized(
	ctx context.Context,
	q *sqlc.Queries,
	id string,
) (*application.PublishedRealizedPnL, error) {
	value, err := loadActivitySelection(ctx, q, id)
	if err != nil {
		return nil, err
	}

	return reportedRealizedFields(value.Fields, value.Conflicts, value.OperatorFields), nil
}

func realizedComparison(value *application.PublishedRealizedPnL) map[string]application.CorrectionValue {
	out := map[string]application.CorrectionValue{}
	if value == nil {
		return out
	}

	for key, money := range map[string]*application.PublishedMoney{"realized_pnl": value.Amount, "reported_cost_basis": value.CostBasis} {
		if money != nil {
			out[key] = application.CorrectionValue{Value: money.Amount, Currency: money.Currency}
		} else {
			out[key] = application.CorrectionValue{}
		}
	}

	for key, date := range map[string]*string{"realized_report_date": value.ReportDate, "cost_basis_report_date": value.CostBasisReportDate} {
		if date != nil {
			out[key] = application.CorrectionValue{Value: *date}
		} else {
			out[key] = application.CorrectionValue{}
		}
	}

	return out
}
