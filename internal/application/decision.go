// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0
package application

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"go.trippwill.dev/wake/financial"
	"go.trippwill.dev/wake/source"
)

const ErrStale Error = "the entity changed; reload and review before submitting"

type (
	DecisionInput struct {
		TargetKind, TargetID, Scope, Text, Currency, Action, Submission, Rationale string
		ExpectedRevision                                                           int64
	}
	Decision struct{ input DecisionInput }
)

func NewDecision(f DecisionInput) (Decision, error) {
	if (f.TargetKind != "activity" && f.TargetKind != "instrument") || f.TargetID == "" || f.Scope == "" ||
		f.ExpectedRevision < 1 ||
		(f.Action != "set" && f.Action != "release") ||
		len(f.Submission) < 16 ||
		len(f.Submission) > 128 ||
		len(f.Text) > 2048 ||
		len(f.Rationale) > 1024 {
		return Decision{}, ErrInvalid
	}

	for _, s := range []string{f.TargetID, f.Scope, f.Submission, f.Rationale, f.Text} {
		if !utf8.ValidString(s) || strings.TrimSpace(s) != s || strings.IndexFunc(s, unicode.IsControl) >= 0 {
			return Decision{}, ErrInvalid
		}
	}

	if f.Action == "release" {
		f.Text = ""
		f.Currency = ""
	}

	return Decision{f}, nil
}
func (d Decision) Fields() DecisionInput { return d.input }

type (
	DecisionRecord struct {
		ID, Scope, Text, Currency, Action, Actor, At, Rationale, Supersedes string
		Active                                                              bool
	}
	CorrectionCandidate struct{ Value, Label string }
	CorrectionForm      struct {
		Links           []CorrectionCandidate
		Kind, ID, Label string
		Revision        int64
		Fields          []string
		Candidates      []CorrectionCandidate
		Decisions       []DecisionRecord
	}
)

func activityFields(kind string) []string {
	switch kind {
	case "trade":
		return []string{
			"quantity",
			"price",
			"side",
			"trade_date",
			"executed_at",
			"settlement_date",
			"gross",
			"proceeds",
			"net",
			"net_billable",
			"accrued_interest",
			"position_effect",
			"quotation_basis",
			"option.strike",
			"option.multiplier",
			"option.right",
			"option.expiry",
			"debt.coupon",
			"debt.maturity",
			"instrument",
			"listing",
		}
	case "lifecycle":
		return []string{
			"quantity",
			"kind",
			"trade_date",
			"executed_at",
			"instrument",
			"option.strike",
			"option.multiplier",
			"option.right",
			"option.expiry",
		}
	case "conversion":
		return []string{
			"sent_amount",
			"received_amount",
			"rate",
			"trade_date",
			"executed_at",
			"gross",
			"proceeds",
			"net",
			"net_billable",
		}
	}

	return nil
}
func ActivityFields(kind string) []string { return activityFields(kind) }
func ApplyTradeField(t source.Trade, scope, text, currency string) (source.Trade, error) {
	f := t.Fields()

	var err error
	switch scope {
	case "quantity":
		f.Quantity, err = financial.NewQuantity(text)
	case "price":
		c, e := financial.ParseCurrency(currency)
		if e != nil {
			return source.Trade{}, ErrInvalid
		}

		f.Price, err = financial.NewPrice(text, c)
	case "side":
		f.Side = source.Side(text)
	case "trade_date":
		f.TradeDate, err = financial.ParseDate(text)
	case "executed_at":
		f.ExecutedAt, err = optionalTimestamp(text)
	case "settlement_date":
		f.SettlementDate, err = optionalDate(text)
	case "gross", "proceeds", "net", "net_billable", "accrued_interest":
		m, e := optionalMoney(text, currency)
		err = e

		switch scope {
		case "gross":
			f.Gross = m
		case "proceeds":
			f.Proceeds = m
		case "net":
			f.Net = m
		case "net_billable":
			f.NetWithBillable = m
		case "accrued_interest":
			f.AccruedInterest = m
		}
	case "position_effect":
		f.PositionEffect = source.PositionEffect(text)
	case "quotation_basis":
		f.QuotationBasis = source.QuotationBasis(text)
	default:
		f.Instrument, err = applyInstrumentTerm(f.Instrument, scope, text, currency)
	}

	if err != nil {
		return source.Trade{}, ErrInvalid
	}

	return source.NewTrade(f)
}

func ApplyLifecycleField(l source.Lifecycle, scope, text, currency string) (source.Lifecycle, error) {
	f := l.Fields()

	var err error
	switch scope {
	case "quantity":
		f.Quantity, err = financial.NewQuantity(text)
	case "kind":
		f.Kind = source.LifecycleKind(text)
	case "trade_date":
		f.Date, err = financial.ParseDate(text)
	case "executed_at":
		f.At, err = optionalTimestamp(text)
	default:
		f.Instrument, err = applyInstrumentTerm(f.Instrument, scope, text, currency)
	}

	if err != nil {
		return source.Lifecycle{}, ErrInvalid
	}

	return source.NewLifecycle(f)
}

func ApplyConversionField(v source.Conversion, scope, text, currency string) (source.Conversion, error) {
	f := v.Fields()

	var err error
	switch scope {
	case "sent_amount":
		f.Sent, err = source.NewCurrencyAmount(f.Sent.Currency(), text)
	case "received_amount":
		f.Received, err = source.NewCurrencyAmount(f.Received.Currency(), text)
	case "rate":
		f.Rate, err = financial.NewPrice(text, f.Rate.Currency())
	case "trade_date":
		f.Date, err = financial.ParseDate(text)
	case "executed_at":
		f.At, err = optionalTimestamp(text)
	case "gross", "proceeds", "net", "net_billable":
		m, e := optionalMoney(text, currency)
		err = e

		switch scope {
		case "gross":
			f.Gross = m
		case "proceeds":
			f.Proceeds = m
		case "net":
			f.Net = m
		case "net_billable":
			f.NetWithBillable = m
		}
	default:
		return source.Conversion{}, ErrInvalid
	}

	if err != nil {
		return source.Conversion{}, ErrInvalid
	}

	return source.NewConversion(f)
}

func optionalMoney(text, currency string) (financial.Money, error) {
	if text == "" && currency == "" {
		return financial.Money{}, nil
	}

	c, err := financial.ParseCurrency(currency)
	if err != nil {
		return financial.Money{}, err
	}

	return financial.NewMoney(text, c)
}

func optionalDate(text string) (financial.Date, error) {
	if text == "" {
		return financial.Date{}, nil
	}

	return financial.ParseDate(text)
}

func optionalTimestamp(text string) (financial.Timestamp, error) {
	if text == "" {
		return financial.Timestamp{}, nil
	}

	return financial.ParseTimestamp(text)
}

func applyInstrumentTerm(i source.Instrument, scope, text, currency string) (source.Instrument, error) {
	f := i.Fields()

	var err error
	switch {
	case strings.HasPrefix(scope, "option.") && i.Kind() == source.Option:
		o := i.Option().Fields()
		switch scope {
		case "option.strike":
			c, e := financial.ParseCurrency(currency)
			if e != nil {
				return source.Instrument{}, ErrInvalid
			}

			o.Strike, err = financial.NewPrice(text, c)
		case "option.multiplier":
			o.Multiplier, err = optionalQuantity(text)
		case "option.right":
			o.Right = source.OptionRight(text)
		case "option.expiry":
			o.Expiry, err = optionalDate(text)
		default:
			return source.Instrument{}, ErrInvalid
		}

		if err == nil {
			f.Option, err = source.NewOptionTerms(o)
		}
	case strings.HasPrefix(scope, "debt.") && i.Kind() == source.Debt:
		d := i.Debt().Fields()
		switch scope {
		case "debt.coupon":
			d.Coupon, err = optionalQuantity(text)
		case "debt.maturity":
			d.Maturity, err = optionalDate(text)
		case "debt.payments":
			d.PaymentsPerYear, err = strconv.Atoi(text)
		default:
			return source.Instrument{}, ErrInvalid
		}

		if err == nil {
			f.Debt, err = source.NewDebtTerms(d)
		}
	default:
		return source.Instrument{}, ErrInvalid
	}

	if err != nil {
		return source.Instrument{}, ErrInvalid
	}

	return source.NewInstrument(f)
}

func optionalQuantity(text string) (financial.Quantity, error) {
	if text == "" {
		return financial.Quantity{}, nil
	}

	return financial.NewQuantity(text)
}
