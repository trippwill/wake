// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"context"
	"encoding/json"
	"slices"

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/persistence/sqlite/sqlc"
	"go.trippwill.dev/wake/provider"
	"go.trippwill.dev/wake/source"
)

// Apply report policies only to a selected interpretation of the authoritative
// source. Missing fields on their own do not establish a reporting expectation.
func reportingPolicy(
	ctx context.Context, q *sqlc.Queries, registrations []provider.Registration,
	accountID string, selection application.ActivitySelection,
) (provider.ActivityReportingPolicy, string, error) {
	support := selection.Sources[string(source.FieldFamily)]
	if len(support) == 0 || len(registrations) == 0 {
		return nil, "", nil
	}

	var origin [3]string
	if err := json.Unmarshal([]byte(support[0]), &origin); err != nil {
		return nil, "", ErrCorrupt
	}

	for _, registration := range registrations {
		policy, ok := registration.Normalizer().(provider.ActivityReportingPolicy)
		if !ok || registration.Version() != origin[1] {
			continue
		}

		account, err := q.AccountByID(ctx, accountID)
		if err != nil {
			return nil, "", err
		}

		if account.Provider == registration.Descriptor().ID() {
			return policy, registration.Version(), nil
		}
	}

	return nil, "", nil
}

func explainActivityFields(
	a *application.PublishedActivity, selection application.ActivitySelection,
	policy provider.ActivityReportingPolicy, version string,
) []application.PublishedFieldDetail {
	out := []application.PublishedFieldDetail{}

	explicit := map[source.ActivityFieldName]bool{}
	for _, field := range selection.Fields {
		if !field.Known() && len(selection.Sources[field.Key()]) != 0 {
			explicit[field.Name()] = true
		}
	}

	fieldDetail := func(path, key string) application.PublishedFieldDetail {
		detail := application.PublishedFieldDetail{Path: path, Reason: application.FieldNotReported}
		switch {
		case slices.Contains(selection.OperatorFields, key):
			detail.Reason = application.FieldOperatorWithheld
		case slices.Contains(selection.Conflicts, key):
			detail.Reason = application.FieldUnresolved
		case explicit[source.ActivityFieldName(key)]:
			detail.Reason = application.FieldReportedUnavailable
		}

		return detail
	}

	add := func(path string, name source.ActivityFieldName, absent bool) {
		if !absent {
			return
		}

		detail := fieldDetail(path, string(name))
		if policy != nil && name != "" {
			expectation := policy.ActivityFieldExpectation(a.Family, name)
			if expectation == provider.Expected || expectation == provider.NotExpected {
				detail.Expectation = &expectation
				detail.Interpretation = &version
			}
		}

		out = append(out, detail)
	}

	terms := func(t application.PublishedInstrumentTerms) {
		addTerm := func(name string, absent bool) {
			if absent {
				out = append(out, fieldDetail("/instrument_terms/"+name, t.Family+"."+name))
			}
		}

		switch t.Family {
		case "option":
			f := t.Option
			addTerm("right", f.Right == nil)
			addTerm("expiry", f.Expiry == nil)
			addTerm("strike", f.Strike == nil)
			addTerm("multiplier", f.Multiplier == nil)
			addTerm("effective_from", f.EffectiveFrom == nil)
			addTerm("deliverables", f.Deliverables == nil)
		case "debt":
			f := t.Debt
			addTerm("type", f.Type == nil)
			addTerm("currency", f.Currency == nil)
			addTerm("maturity", f.Maturity == nil)
			addTerm("issue_date", f.IssueDate == nil)

			if f.DatedDate == nil && f.Type != nil && *f.Type == string(source.TreasuryBill) {
				out = append(out, application.PublishedFieldDetail{
					Path: "/instrument_terms/dated_date", Reason: application.FieldNotApplicable,
				})
			} else {
				addTerm("dated_date", f.DatedDate == nil)
			}

			addTerm("coupon_rate", f.CouponRate == nil)
			addTerm("payments_per_year", f.PaymentsPerYear == nil)
		}
	}

	money := func(f application.PublishedTradeCash) {
		add("/gross_amount", source.FieldGrossAmount, f.GrossAmount == nil)
		add("/proceeds", source.FieldProceeds, f.Proceeds == nil)
		add("/net_cash", source.FieldNetCash, f.NetCash == nil)
		add("/net_cash_with_billable", source.FieldNetCashWithBillable, f.NetCashWithBillable == nil)
	}

	add("/at", source.FieldAt, a.At == nil)

	if a.ReportedRealizedPnL != nil {
		r := a.ReportedRealizedPnL
		add("/reported_realized_pnl/amount", source.FieldRealizedPnL, r.Amount == nil)
		add("/reported_realized_pnl/cost_basis", source.FieldReportedCostBasis, r.CostBasis == nil)
	}

	switch a.Family {
	case "trade":
		f := a.Trade
		add("/position_effect", source.FieldPositionEffect, f.PositionEffect == nil)
		add("/quotation_basis", "", f.QuotationBasis == nil)
		add("/settlement_date", source.FieldSettlementDate, f.SettlementDate == nil)
		add("/order", source.FieldOrder, f.Order == nil)
		add("/accrued_interest", source.FieldAccruedInterest, f.AccruedInterest == nil)
		money(f.PublishedTradeCash)
		terms(f.InstrumentTerms)
	case "conversion":
		f := a.Conversion
		add("/sent/amount", source.FieldSent, f.Sent.Amount == nil)
		add("/received/amount", source.FieldReceived, f.Received.Amount == nil)
		add("/rate", source.FieldRate, f.Rate == nil)
		add("/rate_base", source.FieldRateBase, f.RateBase == nil)
		add("/order", source.FieldOrder, f.Order == nil)
		money(f.PublishedTradeCash)
	case "lifecycle":
		add("/order", source.FieldOrder, a.Lifecycle.Order == nil)
		terms(a.Lifecycle.InstrumentTerms)
	case "cash":
		f := a.Cash
		add("/settlement_date", source.FieldSettlementDate, f.SettlementDate == nil)
		add("/report_date", source.FieldReportDate, f.ReportDate == nil)
		add("/ex_date", source.FieldExDate, f.ExDate == nil)
		add("/available_for_trading_date", source.FieldAvailableForTradingDate, f.AvailableForTradingDate == nil)
		add("/description", source.FieldDescription, f.Description == nil)
		add("/action", source.FieldCashAction, f.Action == nil)
	case "security-transfer":
		f := a.Transfer
		add("/settlement_date", source.FieldSettlementDate, f.SettlementDate == nil)
		add("/report_date", source.FieldReportDate, f.ReportDate == nil)
		add("/price", source.FieldTransferPrice, f.Price == nil)
		add("/position_value", source.FieldPositionValue, f.PositionValue == nil)
		add("/reported_cost", source.FieldTransferCost, f.ReportedCost == nil)
		add("/reported_pnl", source.FieldTransferPnL, f.ReportedPnL == nil)
		terms(f.InstrumentTerms)
	}

	return out
}
